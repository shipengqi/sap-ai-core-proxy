package stream

import (
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

// geminiStreamCaptureLimit caps how much of a streamGenerateContent response is
// buffered for usage accounting. A capture that exceeds it is dropped instead of
// grown, so a huge response costs neither unbounded memory nor a wrong number.
const geminiStreamCaptureLimit = 512 * 1024

// PipeGemini forwards a Gemini streaming response as raw chunked bytes.
// Gemini may return a chunked JSON array or SSE when alt=sse is requested.
func PipeGemini(c *gin.Context, upstream *http.Response) {
	defer func() { _ = upstream.Body.Close() }()

	usage.MarkStream(c)
	if strings.HasPrefix(upstream.Header.Get("Content-Type"), "text/event-stream") {
		pipeGeminiSSE(c, upstream)
		return
	}

	c.Header("Content-Type", upstream.Header.Get("Content-Type"))
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)

	// Gemini's chunked framing leaves no complete JSON object per read, so the
	// body is captured while it is forwarded (never instead of being forwarded)
	// and parsed once the stream ends. If the cap is hit the capture is dropped,
	// which surfaces as usage_missing for this request rather than as a wrong
	// token count.
	var capture []byte
	truncated := false

	buf := make([]byte, 4096)
	for {
		n, err := upstream.Body.Read(buf)
		if n > 0 {
			if _, writeErr := c.Writer.Write(buf[:n]); writeErr != nil {
				return
			}
			if ok {
				flusher.Flush()
			}
			if !truncated {
				if len(capture)+n > geminiStreamCaptureLimit {
					truncated = true
					capture = nil
					// Attribute the loss to the cap rather than to the upstream:
					// the request still counts as usage_missing (the number is
					// unknown) but P1 can tell the two apart.
					usage.MarkCaptureDropped(c)
				} else {
					capture = append(capture, buf[:n]...)
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			usage.MarkUpstreamStatus(c, http.StatusBadGateway)
			break
		}
	}

	if !truncated && len(capture) > 0 {
		usage.ObserveGeminiStream(c, capture)
	}
}

func pipeGeminiSSE(c *gin.Context, upstream *http.Response) {
	streamHeaders(c)
	writeFailed := false
	err := readSSE(upstream.Body, func(frame sseFrame) bool {
		if frame.Data != "" && frame.Data != "[DONE]" {
			usage.ObserveGeminiChunk(c, []byte(frame.Data))
		}
		_, err := io.WriteString(c.Writer, frame.Raw+"\n")
		writeFailed = err != nil
		if !writeFailed {
			c.Writer.Flush()
		}
		return !writeFailed
	})
	if err != nil && !writeFailed {
		streamError(c, "gemini", err.Error())
	}
}

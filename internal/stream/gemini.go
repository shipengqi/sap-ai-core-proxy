package stream

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

// geminiStreamCaptureLimit caps how much of a streamGenerateContent response is
// buffered for usage accounting. A capture that exceeds it is dropped instead of
// grown, so a huge response costs neither unbounded memory nor a wrong number.
const geminiStreamCaptureLimit = 512 * 1024

// PipeGemini forwards a Gemini streaming response as raw chunked bytes.
// Gemini does not use SSE — it returns chunked JSON arrays.
func PipeGemini(c *gin.Context, upstream *http.Response) {
	defer func() { _ = upstream.Body.Close() }()

	usage.MarkStream(c)

	c.Header("Content-Type", upstream.Header.Get("Content-Type"))
	c.Header("Transfer-Encoding", "chunked")
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
			_, _ = c.Writer.Write(buf[:n])
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
			break
		}
	}

	if !truncated && len(capture) > 0 {
		usage.ObserveGeminiStream(c, capture)
	}
}

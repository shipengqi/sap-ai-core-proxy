// Package stream provides helpers for forwarding SAP AI Core streaming responses
// to the caller in the correct wire format per provider.
package stream

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

// PipeOpenAI forwards an OpenAI-format SSE response from upstream to the Gin
// response writer. It reads `data: ...` lines and flushes after each one.
func PipeOpenAI(c *gin.Context, upstream *http.Response) {
	defer func() { _ = upstream.Body.Close() }()

	usage.MarkStream(c)

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		_ = fmt.Errorf("response writer does not support flushing")
		return
	}

	scanner := bufio.NewScanner(upstream.Body)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			_, _ = io.WriteString(c.Writer, "\n")
			flusher.Flush()
			continue
		}
		_, _ = fmt.Fprintf(c.Writer, "%s\n", line)
		flusher.Flush()

		// The final chunk carries the usage block when the caller asked for it
		// (stream_options.include_usage). Intermediate chunks carry
		// "usage": null, which the observer treats as a no-op. Only lines that
		// contain the substring are decoded, so the common chunk costs nothing.
		if strings.HasPrefix(line, "data:") {
			usage.ObserveOpenAIChunk(c, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			continue
		}
		// A bare JSON body (an upstream 200 carrying an error object instead of
		// SSE) still has to be inspected, or the failure would count as a success.
		//
		// This heuristic assumes a single-line (compact) JSON error body; a
		// pretty-printed body would not be detected and the request would be
		// counted as uninstrumented. Real SAP AI Core responses are compact, so
		// the exposure is small; buffering to brace-balance is deliberately left
		// out until the P1 numbers say it matters.
		if strings.HasPrefix(line, "{") {
			usage.ObserveOpenAIChunk(c, line)
		}
	}
}

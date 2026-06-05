// Package stream provides helpers for forwarding SAP AI Core streaming responses
// to the caller in the correct wire format per provider.
package stream

import (
	"bufio"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// PipeOpenAI forwards an OpenAI-format SSE response from upstream to the Gin
// response writer. It reads `data: ...` lines and flushes after each one.
func PipeOpenAI(c *gin.Context, upstream *http.Response) {
	defer upstream.Body.Close()

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
	}
}

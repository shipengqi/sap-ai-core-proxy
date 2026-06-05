package stream

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// PipeGemini forwards a Gemini streaming response as raw chunked bytes.
// Gemini does not use SSE — it returns chunked JSON arrays.
func PipeGemini(c *gin.Context, upstream *http.Response) {
	defer func() { _ = upstream.Body.Close() }()

	c.Header("Content-Type", upstream.Header.Get("Content-Type"))
	c.Header("Transfer-Encoding", "chunked")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, err := upstream.Body.Read(buf)
		if n > 0 {
			_, _ = c.Writer.Write(buf[:n])
			if ok {
				flusher.Flush()
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
	}
}

package stream

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// PipeAnthropic forwards an Anthropic-format SSE response from upstream.
// It injects "event: {type}" lines if the upstream omits them (SAP AI Core
// Bedrock-style responses sometimes drop the event: prefix).
func PipeAnthropic(c *gin.Context, upstream *http.Response) {
	defer func() { _ = upstream.Body.Close() }()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return
	}

	scanner := bufio.NewScanner(upstream.Body)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	var lastWasEvent bool

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			_, _ = fmt.Fprintf(c.Writer, "\n")
			flusher.Flush()
			lastWasEvent = false
			continue
		}

		if strings.HasPrefix(line, "event:") {
			lastWasEvent = true
			_, _ = fmt.Fprintf(c.Writer, "%s\n", line)
			flusher.Flush()
			continue
		}

		if strings.HasPrefix(line, "data:") {
			// If no preceding event: line was sent for this chunk, infer it from
			// the "type" field in the JSON payload.
			if !lastWasEvent {
				payload := strings.TrimPrefix(line, "data:")
				payload = strings.TrimSpace(payload)
				if eventType := extractEventType(payload); eventType != "" {
					_, _ = fmt.Fprintf(c.Writer, "event: %s\n", eventType)
				}
			}
			_, _ = fmt.Fprintf(c.Writer, "%s\n", line)
			flusher.Flush()
			lastWasEvent = false
			continue
		}

		// Forward any other lines verbatim (e.g. retry:).
		_, _ = fmt.Fprintf(c.Writer, "%s\n", line)
		flusher.Flush()
		lastWasEvent = false
	}
}

func extractEventType(jsonPayload string) string {
	var m struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(jsonPayload), &m); err == nil {
		return m.Type
	}
	return ""
}

package stream

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

// PipeAnthropic forwards an Anthropic-format SSE response from upstream.
// It injects "event: {type}" lines if the upstream omits them (SAP AI Core
// Bedrock-style responses sometimes drop the event: prefix).
func PipeAnthropic(c *gin.Context, upstream *http.Response) {
	defer func() { _ = upstream.Body.Close() }()

	usage.MarkStream(c)

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
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))

			// If no preceding event: line was sent for this chunk, infer it from
			// the "type" field in the JSON payload.
			if !lastWasEvent {
				if eventType := extractEventType(payload); eventType != "" {
					_, _ = fmt.Fprintf(c.Writer, "event: %s\n", eventType)
				}
			}
			_, _ = fmt.Fprintf(c.Writer, "%s\n", line)
			flusher.Flush()
			lastWasEvent = false

			// Observe only after the bytes are on the wire: accounting must never
			// delay or alter what the client receives.
			usage.ObserveAnthropicEvent(c, payload)
			continue
		}

		// Forward any other lines verbatim (e.g. retry:).
		_, _ = fmt.Fprintf(c.Writer, "%s\n", line)
		flusher.Flush()
		lastWasEvent = false

		// An upstream that answers 200 with a bare JSON error body (no SSE framing)
		// would otherwise be forwarded without ever being inspected, so a failed
		// stream would be counted as a success.
		//
		// This heuristic assumes a single-line (compact) JSON error body; a
		// pretty-printed body would not be detected and the request would be
		// counted as uninstrumented. Real SAP AI Core responses are compact, so
		// the exposure is small; buffering to brace-balance is deliberately left
		// out until the P1 numbers say it matters.
		if strings.HasPrefix(line, "{") {
			usage.ObserveAnthropicEvent(c, line)
		}
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

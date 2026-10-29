package stream

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

const maxSSEFrameBytes = 8 << 20

type sseFrame struct{ Event, Data, Raw string }

// readSSE handles complete frames, including multi-line data and a final frame
// without a blank line. Returning false stops reading and closes the upstream.
func readSSE(r io.Reader, consume func(sseFrame) bool) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxSSEFrameBytes)
	var frame sseFrame
	var raw strings.Builder
	var data []string
	flush := func() bool {
		frame.Raw = raw.String()
		frame.Data = strings.Join(data, "\n")
		proceed := true
		if frame.Raw != "" {
			proceed = consume(frame)
		}
		frame = sseFrame{}
		raw = strings.Builder{}
		data = nil
		return proceed
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if !flush() {
				return nil
			}
			continue
		}
		if raw.Len()+len(line)+1 > maxSSEFrameBytes {
			return fmt.Errorf("upstream SSE frame exceeds %d bytes", maxSSEFrameBytes)
		}
		raw.WriteString(line)
		raw.WriteByte('\n')
		if strings.HasPrefix(line, "event:") {
			frame.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	flush()
	return nil
}

func streamHeaders(c *gin.Context) {
	usage.MarkStream(c)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	if c.Request == nil || c.Request.ProtoMajor < 2 {
		c.Header("Connection", "keep-alive")
	}
	c.Header("X-Accel-Buffering", "no")
}

func emitSSE(c *gin.Context, event string, payload any) bool {
	b, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	if event != "" {
		if _, err = fmt.Fprintf(c.Writer, "event: %s\n", event); err != nil {
			return false
		}
	}
	if _, err = fmt.Fprintf(c.Writer, "data: %s\n\n", b); err != nil {
		return false
	}
	c.Writer.Flush()
	return true
}

func streamError(c *gin.Context, protocol, message string) {
	payload := map[string]any{"error": map[string]any{"type": "upstream_error", "message": message}}
	event := ""
	if protocol == "anthropic" || protocol == "responses" {
		event = "error"
		payload["type"] = "error"
	}
	b, _ := json.Marshal(payload)
	usage.ObserveOpenAIChunk(c, string(b))
	emitSSE(c, event, payload)
	if protocol == "openai" {
		_, _ = io.WriteString(c.Writer, "data: [DONE]\n\n")
		c.Writer.Flush()
	}
}

// PipeResponses forwards the native protocol and observes IDs/usage without
// buffering a generation. The callback can maintain response affinity.
func PipeResponses(c *gin.Context, upstream *http.Response, observe func([]byte)) {
	defer func() { _ = upstream.Body.Close() }()
	streamHeaders(c)
	terminal := false
	writeFailed := false
	err := readSSE(upstream.Body, func(frame sseFrame) bool {
		if frame.Data != "" && frame.Data != "[DONE]" {
			var ev struct {
				Type     string          `json:"type"`
				Response json.RawMessage `json:"response"`
				Error    json.RawMessage `json:"error"`
			}
			if json.Unmarshal([]byte(frame.Data), &ev) != nil {
				streamError(c, "responses", "invalid upstream Responses event")
				terminal = true
				return false
			}
			if observe != nil {
				observe([]byte(frame.Data))
			}
			if len(ev.Response) > 0 {
				usage.ObserveJSONBody(c, ev.Response)
			}
			switch ev.Type {
			case "response.completed", "response.incomplete", "response.failed", "error":
				terminal = true
			}
			if ev.Type == "response.failed" || ev.Type == "error" {
				usage.MarkUpstreamStatus(c, 502)
			}
		}
		_, writeErr := io.WriteString(c.Writer, frame.Raw+"\n")
		writeFailed = writeErr != nil
		if !writeFailed {
			c.Writer.Flush()
		}
		return !terminal && !writeFailed
	})
	if !terminal && !writeFailed {
		if err != nil {
			streamError(c, "responses", err.Error())
		} else {
			streamError(c, "responses", "upstream stream ended before a terminal event")
		}
	}
}

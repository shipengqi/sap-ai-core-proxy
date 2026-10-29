package stream

import (
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
	"io"
	"net/http"
	"strings"
)

func PipeAnthropic(c *gin.Context, upstream *http.Response) {
	defer func() { _ = upstream.Body.Close() }()
	streamHeaders(c)
	terminal, writeFailed := false, false
	err := readSSE(upstream.Body, func(frame sseFrame) bool {
		payload := frame.Data
		if payload == "" && strings.HasPrefix(strings.TrimSpace(frame.Raw), "{") {
			payload = strings.TrimSpace(frame.Raw)
		}
		if payload != "" {
			var ev struct {
				Type  string          `json:"type"`
				Error json.RawMessage `json:"error"`
			}
			if json.Unmarshal([]byte(payload), &ev) != nil {
				streamError(c, "anthropic", "invalid upstream Anthropic event")
				terminal = true
				return false
			}
			usage.ObserveAnthropicEvent(c, payload)
			kind := ev.Type
			if kind == "" {
				kind = frame.Event
			}
			if kind == "error" || len(ev.Error) > 0 && string(ev.Error) != "null" {
				terminal = true
				emitSSE(c, "error", json.RawMessage(payload))
				return false
			}
			if kind == "message_stop" {
				terminal = true
			}
			if frame.Event == "" && ev.Type != "" {
				if _, e := fmt.Fprintf(c.Writer, "event: %s\n", ev.Type); e != nil {
					writeFailed = true
					return false
				}
			}
		}
		_, e := io.WriteString(c.Writer, frame.Raw+"\n")
		writeFailed = e != nil
		if !writeFailed {
			c.Writer.Flush()
		}
		return !terminal && !writeFailed
	})
	if !terminal && !writeFailed {
		if err != nil {
			streamError(c, "anthropic", err.Error())
		} else {
			streamError(c, "anthropic", "upstream stream ended before message_stop")
		}
	}
}

// Package stream forwards and converts provider streaming protocols.
package stream

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

func PipeOpenAI(c *gin.Context, upstream *http.Response) {
	defer func() { _ = upstream.Body.Close() }()
	streamHeaders(c)
	terminal, writeFailed := false, false
	err := readSSE(upstream.Body, func(frame sseFrame) bool {
		payload := frame.Data
		if payload == "" && strings.HasPrefix(strings.TrimSpace(frame.Raw), "{") {
			payload = strings.TrimSpace(frame.Raw)
		}
		if payload != "" && payload != "[DONE]" {
			var ev struct {
				Error json.RawMessage `json:"error"`
			}
			if json.Unmarshal([]byte(payload), &ev) != nil {
				streamError(c, "openai", "invalid upstream OpenAI event")
				terminal = true
				return false
			}
			usage.ObserveOpenAIChunk(c, payload)
			if len(ev.Error) > 0 && string(ev.Error) != "null" {
				emitSSE(c, "", json.RawMessage(payload))
				_, _ = io.WriteString(c.Writer, "data: [DONE]\n\n")
				c.Writer.Flush()
				terminal = true
				return false
			}
		}
		if payload == "[DONE]" {
			terminal = true
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
			streamError(c, "openai", err.Error())
		} else {
			streamError(c, "openai", "upstream stream ended before [DONE]")
		}
	}
}

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

// PipeAnthropicToOpenAI preserves tool deltas and reports upstream failure.
func PipeAnthropicToOpenAI(c *gin.Context, upstream *http.Response, id, model string, includeUsage ...bool) {
	defer func() { _ = upstream.Body.Close() }()
	streamHeaders(c)
	terminal, writeFailed := false, false
	write := func(delta any, finish any) bool {
		ok := emitSSE(c, "", map[string]any{"id": id, "object": "chat.completion.chunk", "model": model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		writeFailed = !ok
		return ok
	}
	toolCount := 0
	currentTool := -1
	argsSeen := false
	stopReason := ""
	sawTool := false
	var inputTokens, outputTokens int
	err := readSSE(upstream.Body, func(frame sseFrame) bool {
		payload := frame.Data
		if payload == "" {
			if strings.HasPrefix(strings.TrimSpace(frame.Raw), "{") {
				payload = strings.TrimSpace(frame.Raw)
			} else {
				return true
			}
		}
		var ev struct {
			Type         string `json:"type"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Message struct {
				Usage struct {
					Input  int `json:"input_tokens"`
					Output int `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Usage struct {
				Output int `json:"output_tokens"`
			} `json:"usage"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(payload), &ev) != nil {
			streamError(c, "openai", "invalid upstream Anthropic event")
			terminal = true
			return false
		}
		usage.ObserveAnthropicEvent(c, payload)
		kind := ev.Type
		if kind == "" {
			kind = frame.Event
		}
		if len(ev.Error) > 0 && string(ev.Error) != "null" || kind == "error" {
			streamError(c, "openai", "upstream Anthropic stream failed: "+string(ev.Error))
			terminal = true
			return false
		}
		switch kind {
		case "message_start":
			inputTokens = ev.Message.Usage.Input
			outputTokens = ev.Message.Usage.Output
			write(map[string]any{"role": "assistant", "content": ""}, nil)
		case "content_block_start":
			if ev.ContentBlock.Type == "tool_use" {
				sawTool = true
				currentTool = toolCount
				toolCount++
				argsSeen = false
				var delta any
				_ = json.Unmarshal([]byte(toolCallStartDelta(currentTool, ev.ContentBlock.ID, ev.ContentBlock.Name)), &delta)
				write(delta, nil)
			}
		case "content_block_delta":
			switch ev.Delta.Type {
			case "text_delta":
				write(map[string]any{"content": ev.Delta.Text}, nil)
			case "thinking_delta":
				write(map[string]any{"reasoning_content": ev.Delta.Thinking}, nil)
			case "input_json_delta":
				if currentTool >= 0 {
					argsSeen = true
					write(map[string]any{"tool_calls": []any{map[string]any{"index": currentTool, "function": map[string]any{"arguments": ev.Delta.PartialJSON}}}}, nil)
				}
			}
		case "content_block_stop":
			if currentTool >= 0 && !argsSeen {
				write(map[string]any{"tool_calls": []any{map[string]any{"index": currentTool, "function": map[string]any{"arguments": "{}"}}}}, nil)
			}
			currentTool = -1
		case "message_delta":
			stopReason = ev.Delta.StopReason
			if ev.Usage.Output > outputTokens {
				outputTokens = ev.Usage.Output
			}
		case "message_stop":
			terminal = true
			if !write(map[string]any{}, openAIFinishReason(stopReason, sawTool)) {
				return false
			}
			if len(includeUsage) > 0 && includeUsage[0] {
				if !emitSSE(c, "", map[string]any{"id": id, "object": "chat.completion.chunk", "model": model, "choices": []any{}, "usage": map[string]int{"prompt_tokens": inputTokens, "completion_tokens": outputTokens, "total_tokens": inputTokens + outputTokens}}) {
					return false
				}
			}
			_, _ = io.WriteString(c.Writer, "data: [DONE]\n\n")
			c.Writer.Flush()
			return false
		}
		return !writeFailed
	})
	if !terminal && !writeFailed {
		if err != nil {
			streamError(c, "openai", err.Error())
		} else {
			streamError(c, "openai", "upstream stream ended before message_stop")
		}
	}
}

// toolCallStartDelta builds the first OpenAI delta for a tool call, which carries
// the index, id and function name.
func toolCallStartDelta(index int, id, name string) string {
	return fmt.Sprintf(`{"tool_calls":[{"index":%d,"id":%s,"type":"function","function":{"name":%s,"arguments":""}}]}`,
		index, mustJSON(id), mustJSON(name))
}

// openAIFinishReason maps an Anthropic stop_reason to an OpenAI finish_reason.
func openAIFinishReason(stopReason string, sawToolUse bool) string {
	switch stopReason {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "stop_sequence", "end_turn":
		return "stop"
	}
	// Some upstreams omit message_delta entirely; fall back to what we observed.
	if sawToolUse {
		return "tool_calls"
	}
	return "stop"
}

func mustJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `""`
	}
	return string(b)
}

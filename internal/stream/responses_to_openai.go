package stream

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

// responsesStreamResponse is the subset of the Responses payload the bridge
// reads back out of a stream.
type responsesStreamResponse struct {
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Output []struct {
		Type string `json:"type"`
	} `json:"output"`
	Usage struct {
		InputTokens        int `json:"input_tokens"`
		OutputTokens       int `json:"output_tokens"`
		TotalTokens        int `json:"total_tokens"`
		InputTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		OutputTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}

// PipeResponsesToOpenAI converts a Responses API SSE stream into chat.completion
// chunks. It backs the chat/completions bridge: models that only accept function
// tools on /responses (gpt-6.x) are called there, and the caller still expects
// the OpenAI chat streaming shape.
//
// SAP emits bare "data: {...}" lines (no "event:" lines), so the event name is
// read from the payload's "type" field.
func PipeResponsesToOpenAI(c *gin.Context, upstream *http.Response, id, model string, includeUsage bool) {
	defer func() { _ = upstream.Body.Close() }()

	streamHeaders(c)
	terminal, writeFailed := false, false
	writeChunk := func(chunk map[string]interface{}) bool {
		ok := emitSSE(c, "", chunk)
		writeFailed = !ok
		return ok
	}
	writeDelta := func(delta map[string]interface{}, finishReason interface{}) bool {
		return writeChunk(map[string]interface{}{
			"id": id, "object": "chat.completion.chunk", "model": model,
			"choices": []map[string]interface{}{{"index": 0, "delta": delta, "finish_reason": finishReason}},
		})
	}
	writeError := func(message string) {
		terminal = true
		streamError(c, "openai", message)
	}
	if !writeDelta(map[string]interface{}{"role": "assistant", "content": ""}, nil) {
		return
	}

	// Tool calls are addressed by their position in the caller's tool_calls
	// array, while upstream identifies them by item ID.
	toolIndex := map[string]int{}
	nextToolIndex := 0

	arguments := map[string]*strings.Builder{}
	err := readSSE(upstream.Body, func(frame sseFrame) bool {
		payload := strings.TrimSpace(frame.Data)
		if payload == "" || payload == "[DONE]" {
			return true
		}
		var ev struct {
			Type   string `json:"type"`
			Delta  string `json:"delta"`
			ItemID string `json:"item_id"`
			Item   struct {
				ID        string `json:"id"`
				Type      string `json:"type"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"item"`
			Response responsesStreamResponse `json:"response"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			writeError("invalid upstream responses event")
			return false
		}

		if ev.Type == "" {
			ev.Type = frame.Event
		}
		switch ev.Type {
		case "response.output_text.delta":
			if ev.Delta != "" {
				writeDelta(map[string]interface{}{"content": ev.Delta}, nil)
			}
		case "response.output_item.added":
			if ev.Item.Type != "function_call" {
				return true
			}
			if _, exists := toolIndex[ev.Item.ID]; exists {
				return true
			}
			idx := nextToolIndex
			nextToolIndex++
			toolIndex[ev.Item.ID] = idx
			arguments[ev.Item.ID] = &strings.Builder{}
			writeDelta(map[string]interface{}{
				"tool_calls": []map[string]interface{}{{
					"index": idx,
					"id":    ev.Item.CallID,
					"type":  "function",
					"function": map[string]string{
						"name":      ev.Item.Name,
						"arguments": ev.Item.Arguments,
					},
				}},
			}, nil)
			arguments[ev.Item.ID].WriteString(ev.Item.Arguments)
		case "response.refusal.delta":
			writeDelta(map[string]interface{}{"refusal": ev.Delta}, nil)
		case "response.function_call_arguments.delta":
			idx, ok := toolIndex[ev.ItemID]
			if !ok {
				writeError("upstream tool arguments reference an unknown item")
				return false
			}
			writeDelta(map[string]interface{}{
				"tool_calls": []map[string]interface{}{{
					"index":    idx,
					"function": map[string]string{"arguments": ev.Delta},
				}},
			}, nil)
			if arguments[ev.ItemID].Len()+len(ev.Delta) > maxSSEFrameBytes {
				writeError("upstream tool arguments exceed 8 MiB")
				return false
			}
			arguments[ev.ItemID].WriteString(ev.Delta)
		case "response.output_item.done":
			if ev.Item.Type == "function_call" {
				args, exists := arguments[ev.Item.ID]
				if !exists || args.String() != ev.Item.Arguments {
					writeError("upstream tool arguments disagree with the completed item")
					return false
				}
			}
		case "response.completed", "response.incomplete":
			if ev.Type == "response.incomplete" {
				ev.Response.Status = "incomplete"
			}
			if ev.Type == "response.completed" && (ev.Response.Status == "failed" || ev.Response.Status == "cancelled") {
				writeError("upstream responses stream failed")
				return false
			}
			if !writeDelta(map[string]interface{}{}, responsesFinishReason(ev.Response)) {
				return false
			}
			u := ev.Response.Usage
			total := u.TotalTokens
			if total == 0 {
				total = u.InputTokens + u.OutputTokens
			}
			chunk := map[string]interface{}{
				"id":      id,
				"object":  "chat.completion.chunk",
				"model":   model,
				"choices": []map[string]interface{}{},
				"usage": map[string]interface{}{
					"prompt_tokens":     u.InputTokens,
					"completion_tokens": u.OutputTokens,
					"total_tokens":      total,
					"prompt_tokens_details": map[string]int{
						"cached_tokens": u.InputTokensDetails.CachedTokens,
					},
					"completion_tokens_details": map[string]int{
						"reasoning_tokens": u.OutputTokensDetails.ReasoningTokens,
					},
				},
			}
			b, _ := json.Marshal(chunk)
			usage.ObserveOpenAIChunk(c, string(b))
			if includeUsage {
				writeChunk(chunk)
			}
			terminal = true
			if !writeFailed {
				_, writeErr := c.Writer.WriteString("data: [DONE]\n\n")
				writeFailed = writeErr != nil
				if !writeFailed {
					c.Writer.Flush()
				}
			}
			return false
		case "response.failed", "error":
			writeError("upstream responses stream failed: " + payload)
			return false
		}
		return !writeFailed
	})
	if !terminal && !writeFailed {
		if err != nil {
			writeError("reading upstream responses stream: " + err.Error())
		} else {
			writeError("upstream responses stream ended before a terminal event")
		}
	}

}

func responsesFinishReason(resp responsesStreamResponse) string {
	if resp.Status == "incomplete" {
		if resp.IncompleteDetails != nil && resp.IncompleteDetails.Reason == "content_filter" {
			return "content_filter"
		}
		return "length"
	}
	for _, item := range resp.Output {
		if item.Type == "function_call" {
			return "tool_calls"
		}
	}
	return "stop"
}

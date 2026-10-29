package stream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

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

	usage.MarkStream(c)

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return
	}

	writeChunk := func(chunk map[string]interface{}) {
		b, err := json.Marshal(chunk)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", b)
		flusher.Flush()
	}
	writeDelta := func(delta map[string]interface{}, finishReason interface{}) {
		writeChunk(map[string]interface{}{
			"id":     id,
			"object": "chat.completion.chunk",
			"model":  model,
			"choices": []map[string]interface{}{
				{"index": 0, "delta": delta, "finish_reason": finishReason},
			},
		})
	}
	writeDone := func() {
		_, _ = io.WriteString(c.Writer, "data: [DONE]\n\n")
		flusher.Flush()
	}

	// OpenAI streams the role first; clients use it to open the assistant turn.
	writeDelta(map[string]interface{}{"role": "assistant", "content": ""}, nil)

	// Tool calls are addressed by their position in the caller's tool_calls
	// array, while upstream identifies them by item ID.
	toolIndex := map[string]int{}
	nextToolIndex := 0

	scanner := bufio.NewScanner(upstream.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
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
		if err := json.Unmarshal(payload, &ev); err != nil {
			continue
		}

		switch ev.Type {
		case "response.output_text.delta":
			if ev.Delta != "" {
				writeDelta(map[string]interface{}{"content": ev.Delta}, nil)
			}
		case "response.output_item.added":
			if ev.Item.Type != "function_call" {
				continue
			}
			idx := nextToolIndex
			nextToolIndex++
			toolIndex[ev.Item.ID] = idx
			writeDelta(map[string]interface{}{
				"tool_calls": []map[string]interface{}{{
					"index": idx,
					"id":    ev.Item.CallID,
					"type":  "function",
					"function": map[string]string{
						"name":      ev.Item.Name,
						"arguments": "",
					},
				}},
			}, nil)
		case "response.function_call_arguments.delta":
			idx, ok := toolIndex[ev.ItemID]
			if !ok {
				continue
			}
			writeDelta(map[string]interface{}{
				"tool_calls": []map[string]interface{}{{
					"index":    idx,
					"function": map[string]string{"arguments": ev.Delta},
				}},
			}, nil)
		case "response.completed":
			writeDelta(map[string]interface{}{}, responsesFinishReason(ev.Response))
			if includeUsage {
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
				writeChunk(chunk)
			}
			writeDone()
			return
		case "response.failed", "error":
			b, _ := json.Marshal(map[string]interface{}{
				"error": map[string]interface{}{
					"message": "upstream responses stream failed",
					"type":    "upstream_error",
					"details": json.RawMessage(payload),
				},
			})
			_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", b)
			flusher.Flush()
			writeDone()
			return
		}
	}

	writeDone()
}

func responsesFinishReason(resp responsesStreamResponse) string {
	for _, item := range resp.Output {
		if item.Type == "function_call" {
			return "tool_calls"
		}
	}
	if resp.Status == "incomplete" && resp.IncompleteDetails != nil && resp.IncompleteDetails.Reason == "max_output_tokens" {
		return "length"
	}
	return "stop"
}

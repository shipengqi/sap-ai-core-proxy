package stream

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
)

// OrchestrateStream drives a streaming inference response end-to-end.
// It sets SSE headers, selects the right parser, maps events to the target
// response format, handles errors, and flushes after each write.
func OrchestrateStream(resp *http.Response, ctx StreamContext, w http.ResponseWriter) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("response writer does not support flushing")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	headersSent := false

	writeSSE := func(eventType, jsonData string) {
		if ctx.ResponseFormat == "anthropic" {
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, jsonData)
		} else {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", jsonData)
		}
		flusher.Flush()
		headersSent = true
	}

	sendJSON := func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}

	created := int(time.Now().Unix())

	var orchErr error

	switch ctx.APIFormat {
	case "converse":
		orchErr = runConverseStream(resp, ctx, writeSSE, sendJSON, created, &headersSent)
	case "invoke":
		orchErr = runInvokeStream(resp, ctx, writeSSE, sendJSON, created, &headersSent)
	case "gemini":
		orchErr = runGeminiStream(resp, ctx, writeSSE, sendJSON, created, &headersSent)
	default:
		return fmt.Errorf("unknown apiFormat: %s", ctx.APIFormat)
	}

	if orchErr != nil {
		var sapErr *sapclient.SapAPIError
		if errors.As(orchErr, &sapErr) && !headersSent {
			// Pre-stream error — send format-specific JSON error response
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(sapErr.StatusCode)
			if ctx.ResponseFormat == "anthropic" {
				json.NewEncoder(w).Encode(map[string]any{
					"type":  "error",
					"error": map[string]any{"type": "api_error", "message": sapErr.Message},
				})
			} else {
				json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{
						"message": sapErr.Message,
						"type":    "api_error",
						"param":   nil,
						"code":    fmt.Sprintf("%d", sapErr.StatusCode),
					},
				})
			}
			return nil
		}
		// Mid-stream error — signal end to client
		slog.Error("stream error", "error", orchErr)
		if headersSent {
			_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
		}
		return orchErr
	}

	return nil
}

// ---- Converse stream ----

func runConverseStream(
	resp *http.Response,
	ctx StreamContext,
	writeSSE func(string, string),
	sendJSON func(any) string,
	created int,
	headersSent *bool,
) error {
	defer func() { _ = resp.Body.Close() }()

	var inputTokens, outputTokens int
	stopReason := "end_turn"

	if ctx.ResponseFormat == "anthropic" {
		writeSSE("message_start", sendJSON(map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": ctx.CompletionID, "type": "message", "role": "assistant",
				"content": []any{}, "model": ctx.Model,
				"stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
			},
		}))
		writeSSE("ping", sendJSON(map[string]any{"type": "ping"}))
	}

	err := ParseConverseStream(resp.Body, func(ev ConverseEvent) error {
		switch ev.Type {
		case ConverseMetadata:
			if ev.InputTokens > 0 {
				inputTokens = ev.InputTokens
			}
			if ev.OutputTokens > 0 {
				outputTokens = ev.OutputTokens
			}

		case ConverseTextBlockStart:
			if ctx.ResponseFormat == "anthropic" {
				writeSSE("content_block_start", sendJSON(map[string]any{
					"type": "content_block_start", "index": ev.Index,
					"content_block": map[string]any{"type": "text", "text": ""},
				}))
			}

		case ConverseTextDelta, ConverseReasoningDelta:
			if ctx.ResponseFormat == "anthropic" {
				writeSSE("content_block_delta", sendJSON(map[string]any{
					"type": "content_block_delta", "index": ev.Index,
					"delta": map[string]any{"type": "text_delta", "text": ev.Text},
				}))
			} else {
				writeSSE("", sendJSON(openAIChunk(ctx.CompletionID, created, ctx.Model, map[string]any{"content": ev.Text}, nil)))
			}

		case ConverseTextBlockStop:
			if ctx.ResponseFormat == "anthropic" {
				writeSSE("content_block_stop", sendJSON(map[string]any{
					"type": "content_block_stop", "index": ev.Index,
				}))
			}

		case ConverseToolBlockStart:
			if ctx.ResponseFormat == "anthropic" {
				writeSSE("content_block_start", sendJSON(map[string]any{
					"type": "content_block_start", "index": ev.Index,
					"content_block": map[string]any{"type": "tool_use", "id": ev.ID, "name": ev.Name, "input": map[string]any{}},
				}))
			}

		case ConverseToolInputDelta:
			if ctx.ResponseFormat == "anthropic" {
				writeSSE("content_block_delta", sendJSON(map[string]any{
					"type": "content_block_delta", "index": ev.Index,
					"delta": map[string]any{"type": "input_json_delta", "partial_json": ev.PartialJSON},
				}))
			}

		case ConverseToolBlockStop:
			if ctx.ResponseFormat == "anthropic" {
				writeSSE("content_block_stop", sendJSON(map[string]any{
					"type": "content_block_stop", "index": ev.Index,
				}))
			}

		case ConverseMessageStop:
			stopReason = ev.StopReason
		}
		return nil
	})
	if err != nil {
		return err
	}

	if ctx.ResponseFormat == "anthropic" {
		writeSSE("message_delta", sendJSON(map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": outputTokens},
		}))
		writeSSE("message_stop", sendJSON(map[string]any{"type": "message_stop"}))
	} else {
		if inputTokens > 0 || outputTokens > 0 {
			chunk := openAIChunk(ctx.CompletionID, created, ctx.Model, map[string]any{}, strPtr("stop"))
			chunk["usage"] = map[string]any{
				"prompt_tokens":     inputTokens,
				"completion_tokens": outputTokens,
				"total_tokens":      inputTokens + outputTokens,
			}
			writeSSE("", sendJSON(chunk))
		}
		writeSSE("", "[DONE]")
	}
	return nil
}

// ---- Invoke stream ----

func runInvokeStream(
	resp *http.Response,
	ctx StreamContext,
	writeSSE func(string, string),
	sendJSON func(any) string,
	created int,
	headersSent *bool,
) error {
	defer func() { _ = resp.Body.Close() }()

	var inputTokens, outputTokens int
	var blockStarted bool
	resolvedMsgID := ctx.CompletionID

	err := ParseInvokeStream(resp.Body, func(ev InvokeEvent) error {
		switch ev.Type {
		case InvokeMessageStart:
			inputTokens = ev.InputTokens
			if ev.MessageID != "" {
				resolvedMsgID = ev.MessageID
			}
			if ctx.ResponseFormat == "anthropic" {
				writeSSE("message_start", sendJSON(map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id": resolvedMsgID, "type": "message", "role": "assistant",
						"content": []any{}, "model": ctx.Model,
						"stop_reason": nil, "stop_sequence": nil,
						"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": 1},
					},
				}))
				writeSSE("ping", sendJSON(map[string]any{"type": "ping"}))
			} else {
				writeSSE("", sendJSON(openAIChunk(ctx.CompletionID, created, ctx.Model, map[string]any{"role": "assistant"}, nil)))
			}

		case InvokeBlockStart:
			blockStarted = true
			if ctx.ResponseFormat == "anthropic" {
				writeSSE("content_block_start", sendJSON(map[string]any{
					"type": "content_block_start", "index": ev.Index,
					"content_block": ev.ContentBlock,
				}))
			}

		case InvokeBlockDelta:
			if ctx.ResponseFormat == "anthropic" {
				writeSSE("content_block_delta", sendJSON(map[string]any{
					"type": "content_block_delta", "index": ev.Index,
					"delta": ev.Delta,
				}))
			} else {
				if deltaType, _ := ev.Delta["type"].(string); deltaType == "text_delta" {
					if text, ok := ev.Delta["text"].(string); ok && text != "" {
						writeSSE("", sendJSON(openAIChunk(ctx.CompletionID, created, ctx.Model, map[string]any{"content": text}, nil)))
					}
				}
			}

		case InvokeBlockStop:
			if ctx.ResponseFormat == "anthropic" {
				writeSSE("content_block_stop", sendJSON(map[string]any{
					"type": "content_block_stop", "index": ev.Index,
				}))
			}

		case InvokeMessageDelta:
			outputTokens = ev.OutputTokens
			if ctx.ResponseFormat == "anthropic" {
				writeSSE("message_delta", sendJSON(map[string]any{
					"type":  "message_delta",
					"delta": map[string]any{"stop_reason": ev.StopReason, "stop_sequence": ev.StopSequence},
					"usage": map[string]any{"output_tokens": outputTokens},
				}))
			} else {
				fr := mapStopReason(ev.StopReason)
				writeSSE("", sendJSON(openAIChunk(ctx.CompletionID, created, ctx.Model, map[string]any{}, &fr)))
			}

		case InvokeMessageStop:
			if ctx.ResponseFormat == "anthropic" {
				writeSSE("message_stop", sendJSON(map[string]any{"type": "message_stop"}))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Empty-block fallback for Anthropic
	if ctx.ResponseFormat == "anthropic" && !blockStarted {
		writeSSE("message_start", sendJSON(map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": resolvedMsgID, "type": "message", "role": "assistant",
				"content": []any{}, "model": ctx.Model,
				"stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": 1},
			},
		}))
		writeSSE("content_block_start", sendJSON(map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""},
		}))
		writeSSE("content_block_stop", sendJSON(map[string]any{"type": "content_block_stop", "index": 0}))
		writeSSE("message_delta", sendJSON(map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": outputTokens},
		}))
		writeSSE("message_stop", sendJSON(map[string]any{"type": "message_stop"}))
	}

	if ctx.ResponseFormat == "openai" {
		if inputTokens > 0 || outputTokens > 0 {
			chunk := map[string]any{
				"id": ctx.CompletionID, "object": "chat.completion.chunk",
				"created": created, "model": ctx.Model, "choices": []any{},
				"usage": map[string]any{
					"prompt_tokens":     inputTokens,
					"completion_tokens": outputTokens,
					"total_tokens":      inputTokens + outputTokens,
				},
			}
			writeSSE("", sendJSON(chunk))
		}
		writeSSE("", "[DONE]")
	}

	return nil
}

// ---- Gemini stream ----

func runGeminiStream(
	resp *http.Response,
	ctx StreamContext,
	writeSSE func(string, string),
	sendJSON func(any) string,
	created int,
	headersSent *bool,
) error {
	defer resp.Body.Close()

	var promptTokens, outputTokens int

	// Initial role chunk
	writeSSE("", sendJSON(openAIChunk(ctx.CompletionID, created, ctx.Model, map[string]any{"role": "assistant"}, nil)))

	err := ParseGeminiStream(resp.Body, func(ev GeminiEvent) error {
		switch ev.Type {
		case GeminiTextDelta:
			writeSSE("", sendJSON(openAIChunk(ctx.CompletionID, created, ctx.Model, map[string]any{"content": ev.Text}, nil)))
		case GeminiMetadata:
			promptTokens = ev.PromptTokens
			outputTokens = ev.OutputTokens
		}
		return nil
	})
	if err != nil {
		return err
	}

	stop := "stop"
	writeSSE("", sendJSON(openAIChunk(ctx.CompletionID, created, ctx.Model, map[string]any{}, &stop)))

	if promptTokens > 0 || outputTokens > 0 {
		chunk := map[string]any{
			"id": ctx.CompletionID, "object": "chat.completion.chunk",
			"created": created, "model": ctx.Model, "choices": []any{},
			"usage": map[string]any{
				"prompt_tokens":     promptTokens,
				"completion_tokens": outputTokens,
				"total_tokens":      promptTokens + outputTokens,
			},
		}
		writeSSE("", sendJSON(chunk))
	}
	writeSSE("", "[DONE]")

	return nil
}

// ---- Helpers ----

func openAIChunk(id string, created int, model string, delta map[string]any, finishReason *string) map[string]any {
	return map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         delta,
			"finish_reason": finishReason,
		}},
	}
}

func mapStopReason(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return "stop"
	}
}

func strPtr(s string) *string { return &s }

func randomID(n int) string {
	b := make([]byte, n/2+1)
	rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

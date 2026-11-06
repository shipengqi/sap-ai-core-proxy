package stream

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

// PipeOrchestrationToOpenAI unwraps only final_result. Intermediate module
// results can contain prompts or unfiltered output and must never be forwarded.
// SAP's protocol and fixtures: SAP/ai-sdk-js packages/orchestration and
// test-util/data/orchestration/orchestration-chat-completion-stream-chunks.txt.
func PipeOrchestrationToOpenAI(c *gin.Context, upstream *http.Response, includeUsage bool) {
	defer func() { _ = upstream.Body.Close() }()
	streamHeaders(c)
	terminal, writeFailed := false, false
	finished := make(map[int]bool)
	writeError := func(message string) bool {
		terminal = true
		streamError(c, "openai", message)
		return false
	}
	err := readSSE(upstream.Body, func(frame sseFrame) bool {
		if frame.Data == "" {
			return true
		}
		if frame.Data == "[DONE]" {
			if len(finished) == 0 {
				return writeError("Orchestration stream ended without a completion")
			}
			for _, done := range finished {
				if !done {
					return writeError("Orchestration stream ended before a choice finished")
				}
			}
			terminal = true
			_, err := io.WriteString(c.Writer, "data: [DONE]\n\n")
			writeFailed = err != nil
			c.Writer.Flush()
			return false
		}
		var envelope struct {
			FinalResult json.RawMessage `json:"final_result"`
			Error       json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(frame.Data), &envelope) != nil {
			return writeError("invalid upstream Orchestration event")
		}
		if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
			payload := map[string]any{"error": orchestrationError(envelope.Error)}
			data, _ := json.Marshal(payload)
			usage.ObserveOpenAIChunk(c, string(data))
			if emitSSE(c, "", payload) {
				_, _ = io.WriteString(c.Writer, "data: [DONE]\n\n")
				c.Writer.Flush()
			}
			terminal = true
			return false
		}
		if len(envelope.FinalResult) == 0 || string(envelope.FinalResult) == "null" {
			return true
		}
		var chunk map[string]json.RawMessage
		if json.Unmarshal(envelope.FinalResult, &chunk) != nil || chunk == nil {
			return writeError("invalid Orchestration final_result")
		}
		// SAP sends an empty placeholder before the first LLM chunk.
		if string(chunk["id"]) == `""` && string(chunk["object"]) == `""` {
			return true
		}
		var id, object string
		if json.Unmarshal(chunk["id"], &id) != nil || id == "" || json.Unmarshal(chunk["object"], &object) != nil || object != "chat.completion.chunk" {
			return writeError("invalid Orchestration completion identity")
		}
		var choices []map[string]json.RawMessage
		if json.Unmarshal(chunk["choices"], &choices) != nil || choices == nil {
			return writeError("invalid Orchestration choices")
		}
		for _, choice := range choices {
			var index int
			if choice == nil || len(choice["index"]) == 0 || string(choice["index"]) == "null" || json.Unmarshal(choice["index"], &index) != nil || index < 0 {
				return writeError("invalid Orchestration choice index")
			}
			var reason *string
			if len(choice["finish_reason"]) > 0 && json.Unmarshal(choice["finish_reason"], &reason) != nil {
				return writeError("invalid Orchestration finish_reason")
			}
			if reason == nil || *reason == "" {
				choice["finish_reason"] = json.RawMessage("null")
				if _, seen := finished[index]; !seen {
					finished[index] = false
				}
			} else {
				finished[index] = true
			}
			var delta map[string]json.RawMessage
			if len(choice["delta"]) > 0 && json.Unmarshal(choice["delta"], &delta) != nil {
				return writeError("invalid Orchestration delta")
			}
			if delta == nil {
				delta = make(map[string]json.RawMessage)
			}
			if string(delta["role"]) == `""` {
				delete(delta, "role")
			}
			choice["delta"], _ = json.Marshal(delta)
		}
		chunk["choices"], _ = json.Marshal(choices)
		usage.ObserveOpenAIChunk(c, string(envelope.FinalResult))
		tokenUsage := chunk["usage"]
		delete(chunk, "usage")
		if len(choices) > 0 {
			writeFailed = !emitSSE(c, "", chunk)
			if writeFailed {
				return false
			}
		}
		if includeUsage && len(tokenUsage) > 0 && string(tokenUsage) != "null" {
			chunk["choices"] = json.RawMessage("[]")
			chunk["usage"] = tokenUsage
			writeFailed = !emitSSE(c, "", chunk)
		}
		return !writeFailed
	})
	if !terminal && !writeFailed {
		if err != nil {
			streamError(c, "openai", err.Error())
		} else {
			streamError(c, "openai", "upstream Orchestration stream ended before [DONE]")
		}
	}
}

// SAP can return either one module error or a list of module errors. OpenAI
// clients expect a single error object; retain module metadata and all messages.
func orchestrationError(raw json.RawMessage) map[string]json.RawMessage {
	var errors []map[string]json.RawMessage
	var single map[string]json.RawMessage
	if json.Unmarshal(raw, &single) == nil && single != nil {
		errors = append(errors, single)
	} else {
		_ = json.Unmarshal(raw, &errors)
	}
	if len(errors) == 0 || errors[0] == nil {
		return map[string]json.RawMessage{"type": json.RawMessage(`"upstream_error"`), "message": json.RawMessage(`"Orchestration stream failed"`)}
	}
	result := errors[0]
	var messages []string
	for _, entry := range errors {
		var message string
		if json.Unmarshal(entry["message"], &message) == nil && message != "" {
			messages = append(messages, message)
		}
	}
	if len(messages) == 0 {
		messages = append(messages, "Orchestration stream failed")
	}
	result["message"], _ = json.Marshal(strings.Join(messages, "; "))
	result["type"] = json.RawMessage(`"upstream_error"`)
	return result
}

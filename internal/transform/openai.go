// Package transform provides shared request/response conversion helpers
// between Anthropic and OpenAI API formats for SAP AI Core routing.
package transform

import (
	"encoding/json"
)

// AnthropicToOpenAIBody converts an Anthropic messages request to the SAP AI Core
// OpenAI-compatible chat completions body (sent to /chat/completions?api-version=...).
func AnthropicToOpenAIBody(raw map[string]json.RawMessage, streaming bool) []byte {
	sap := make(map[string]json.RawMessage)

	// Forward messages as-is.
	if v, ok := raw["messages"]; ok {
		msgs := prependSystemMessage(raw, v)
		sap["messages"] = msgs
	}

	// Map Anthropic field names to OpenAI equivalents.
	if v, ok := raw["temperature"]; ok {
		sap["temperature"] = v
	}
	if v, ok := raw["top_p"]; ok {
		sap["top_p"] = v
	}
	if v, ok := raw["stop_sequences"]; ok {
		sap["stop"] = v
	}
	if v, ok := raw["tools"]; ok {
		sap["tools"] = v
	}
	if v, ok := raw["tool_choice"]; ok {
		sap["tool_choice"] = v
	}

	// max_tokens → max_completion_tokens.
	if v, ok := raw["max_tokens"]; ok {
		sap["max_completion_tokens"] = v
	}

	sap["stream"], _ = json.Marshal(streaming)
	if streaming {
		sap["stream_options"], _ = json.Marshal(map[string]bool{"include_usage": true})
	}

	// Intentionally excluded: anthropic_version, thinking, metadata, top_k — not supported by OpenAI endpoint.

	out, _ := json.Marshal(sap)
	return out
}

// prependSystemMessage moves the top-level "system" field into the messages array
// as a leading {"role":"system","content":"..."} message (OpenAI convention).
func prependSystemMessage(raw map[string]json.RawMessage, messagesRaw json.RawMessage) json.RawMessage {
	sysRaw, ok := raw["system"]
	if !ok {
		return messagesRaw
	}

	// system may be a string or an array of content blocks — flatten to string first.
	filtered := map[string]json.RawMessage{"system": sysRaw}
	filtered = FlattenSystem(filtered)
	sysRaw = filtered["system"]

	var sysStr string
	if err := json.Unmarshal(sysRaw, &sysStr); err != nil || sysStr == "" {
		return messagesRaw
	}

	sysMsg, _ := json.Marshal(map[string]string{"role": "system", "content": sysStr})

	var msgs []json.RawMessage
	if err := json.Unmarshal(messagesRaw, &msgs); err != nil {
		return messagesRaw
	}

	combined := append([]json.RawMessage{sysMsg}, msgs...)
	out, _ := json.Marshal(combined)
	return out
}

// OpenAIToAnthropicResponse converts an OpenAI chat completion response to
// Anthropic messages API format.
func OpenAIToAnthropicResponse(data []byte, model string) []byte {
	var openAI struct {
		ID      string `json:"id"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(data, &openAI); err != nil {
		return data
	}

	content := ""
	if len(openAI.Choices) > 0 {
		content = openAI.Choices[0].Message.Content
	}

	stopReason := "end_turn"
	if len(openAI.Choices) > 0 {
		switch openAI.Choices[0].FinishReason {
		case "length":
			stopReason = "max_tokens"
		case "tool_calls":
			stopReason = "tool_use"
		}
	}

	anthropic := map[string]interface{}{
		"id":    "msg_" + openAI.ID,
		"type":  "message",
		"role":  "assistant",
		"model": model,
		"content": []map[string]string{
			{"type": "text", "text": content},
		},
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage": map[string]int{
			"input_tokens":  openAI.Usage.PromptTokens,
			"output_tokens": openAI.Usage.CompletionTokens,
		},
	}

	out, _ := json.Marshal(anthropic)
	return out
}

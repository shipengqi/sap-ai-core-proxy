package transform

import (
	"encoding/json"
	"strings"
)

// This file bridges Anthropic content blocks to the OpenAI tool-calling wire shape.
// It is the reverse of openai_tools.go and is used when the native Anthropic surface
// (/anthropic/v1/messages) targets a non-Claude model served by SAP AI Core's
// OpenAI-compatible endpoint.

// ConvertAnthropicToolMessagesToOpenAI rewrites Anthropic content blocks into the
// OpenAI message shapes:
//
//	assistant + [{"type":"tool_use",...}]              -> assistant + tool_calls[]
//	user      + [{"type":"tool_result",...}]           -> {"role":"tool","tool_call_id":...}
//	user      + [{"type":"image","source":{...}}]      -> user + [{"type":"image_url",...}]
//
// Messages that carry no tool, image or thinking blocks pass through byte-for-byte.
// A user message mixing tool results with other content is split: the tool results
// become standalone tool messages (OpenAI requires them to directly follow the
// assistant turn) and any remaining content follows as a user message.
//
// The map is modified in place and returned for chaining.
func ConvertAnthropicToolMessagesToOpenAI(m map[string]json.RawMessage) map[string]json.RawMessage {
	msgsRaw, ok := m["messages"]
	if !ok {
		return m
	}

	var msgs []json.RawMessage
	if err := json.Unmarshal(msgsRaw, &msgs); err != nil {
		return m
	}

	out := make([]json.RawMessage, 0, len(msgs))
	changed := false

	for _, rawMsg := range msgs {
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(rawMsg, &msg); err != nil {
			out = append(out, rawMsg)
			continue
		}

		var role string
		_ = json.Unmarshal(msg["role"], &role)

		var blocks []json.RawMessage
		if contentRaw, ok := msg["content"]; ok {
			if json.Unmarshal(contentRaw, &blocks) != nil {
				// Plain string content — nothing to translate.
				out = append(out, rawMsg)
				continue
			}
		}
		if !hasAnthropicOnlyBlocks(blocks) {
			out = append(out, rawMsg)
			continue
		}

		switch role {
		case "assistant":
			out = append(out, anthropicAssistantToOpenAI(msg, blocks))
		case "user":
			out = append(out, anthropicUserToOpenAI(blocks)...)
		default:
			out = append(out, rawMsg)
		}
		changed = true
	}

	if !changed {
		return m
	}
	m["messages"] = marshalRaw(out)
	return m
}

// ConvertAnthropicToolsToOpenAI rewrites Anthropic tool declarations and tool_choice
// into the OpenAI shape:
//
//	tools:       {"name","description","input_schema"} -> {"type":"function","function":{"name","description","parameters"}}
//	tool_choice: {"type":"tool","name":"x"} -> {"type":"function","function":{"name":"x"}}
//	             {"type":"auto"} -> "auto", {"type":"any"} -> "required"
//
// disable_parallel_tool_use is folded into parallel_tool_calls:false.
//
// The map is modified in place and returned for chaining.
func ConvertAnthropicToolsToOpenAI(m map[string]json.RawMessage) map[string]json.RawMessage {
	toolsRaw, hasTools := m["tools"]
	if !hasTools {
		delete(m, "tool_choice")
		return m
	}

	m["tools"] = convertToolDeclarationsToOpenAI(toolsRaw)

	choiceRaw, hasChoice := m["tool_choice"]
	if !hasChoice {
		return m
	}
	choice, parallelDisabled, ok := convertToolChoiceToOpenAI(choiceRaw)
	if !ok {
		delete(m, "tool_choice")
		return m
	}
	m["tool_choice"] = choice
	if parallelDisabled {
		m["parallel_tool_calls"] = json.RawMessage("false")
	}
	return m
}

// hasAnthropicOnlyBlocks reports whether any block needs translation. Pure text
// arrays are valid on both sides, so they are left alone.
func hasAnthropicOnlyBlocks(blocks []json.RawMessage) bool {
	for _, raw := range blocks {
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &probe) != nil {
			continue
		}
		switch probe.Type {
		case "tool_use", "tool_result", "image", "thinking", "redacted_thinking":
			return true
		}
	}
	return false
}

// anthropicAssistantToOpenAI converts an Anthropic assistant turn into an OpenAI
// assistant message carrying tool_calls.
func anthropicAssistantToOpenAI(msg map[string]json.RawMessage, blocks []json.RawMessage) json.RawMessage {
	var textParts []string
	var toolCalls []map[string]interface{}

	for _, raw := range blocks {
		var block struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if json.Unmarshal(raw, &block) != nil {
			continue
		}
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "tool_use":
			args := "{}"
			if len(block.Input) > 0 && string(block.Input) != "null" {
				args = string(block.Input)
			}
			toolCalls = append(toolCalls, map[string]interface{}{
				"id":   block.ID,
				"type": "function",
				"function": map[string]string{
					"name":      block.Name,
					"arguments": args,
				},
			})
		}
		// thinking / redacted_thinking blocks have no OpenAI equivalent and are dropped.
	}

	converted := make(map[string]json.RawMessage, len(msg)+2)
	for k, v := range msg {
		converted[k] = v
	}
	if len(textParts) > 0 {
		converted["content"] = marshalRaw(strings.Join(textParts, ""))
	} else if len(toolCalls) > 0 {
		// OpenAI reports null content on a tool-call turn.
		converted["content"] = json.RawMessage("null")
	} else {
		converted["content"] = marshalRaw("")
	}
	if len(toolCalls) > 0 {
		converted["tool_calls"] = marshalRaw(toolCalls)
	}
	return marshalRaw(converted)
}

// anthropicUserToOpenAI converts an Anthropic user turn into zero or more OpenAI
// messages: tool messages first, then an optional user message.
func anthropicUserToOpenAI(blocks []json.RawMessage) []json.RawMessage {
	var out []json.RawMessage
	var parts []json.RawMessage
	hasNonTextPart := false

	for _, raw := range blocks {
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &probe) != nil {
			continue
		}

		switch probe.Type {
		case "tool_result":
			var result struct {
				ToolUseID string          `json:"tool_use_id"`
				Content   json.RawMessage `json:"content"`
			}
			if json.Unmarshal(raw, &result) != nil {
				continue
			}
			out = append(out, marshalRaw(map[string]json.RawMessage{
				"role":         json.RawMessage(`"tool"`),
				"tool_call_id": marshalRaw(result.ToolUseID),
				"content":      toolResultToText(result.Content),
			}))

		case "image":
			part, ok := anthropicImageToOpenAIPart(raw)
			if !ok {
				continue
			}
			parts = append(parts, part)
			hasNonTextPart = true

		case "text":
			var block struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(raw, &block) != nil {
				continue
			}
			parts = append(parts, marshalRaw(map[string]string{"type": "text", "text": block.Text}))
		}
	}

	if len(parts) == 0 {
		return out
	}

	content := marshalRaw(parts)
	if !hasNonTextPart {
		// All text — collapse to a plain string, which every OpenAI endpoint accepts.
		var joined strings.Builder
		for _, raw := range parts {
			var part struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(raw, &part) == nil {
				joined.WriteString(part.Text)
			}
		}
		content = marshalRaw(joined.String())
	}

	out = append(out, marshalRaw(map[string]json.RawMessage{
		"role":    json.RawMessage(`"user"`),
		"content": content,
	}))
	return out
}

// toolResultToText flattens an Anthropic tool_result content value into the plain
// string OpenAI tool messages require.
func toolResultToText(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`""`)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return marshalRaw(s)
	}

	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return json.RawMessage(`""`)
	}
	var parts []string
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return marshalRaw(strings.Join(parts, "\n"))
}

// anthropicImageToOpenAIPart converts an Anthropic image block into an OpenAI
// image_url content part.
func anthropicImageToOpenAIPart(raw json.RawMessage) (json.RawMessage, bool) {
	var block struct {
		Source struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
			URL       string `json:"url"`
		} `json:"source"`
	}
	if json.Unmarshal(raw, &block) != nil {
		return nil, false
	}

	var url string
	switch block.Source.Type {
	case "base64":
		mediaType := block.Source.MediaType
		if mediaType == "" {
			mediaType = "image/jpeg"
		}
		url = "data:" + mediaType + ";base64," + block.Source.Data
	case "url":
		url = block.Source.URL
	default:
		return nil, false
	}
	if url == "" {
		return nil, false
	}

	return marshalRaw(map[string]interface{}{
		"type":      "image_url",
		"image_url": map[string]string{"url": url},
	}), true
}

// convertToolDeclarationsToOpenAI rewrites an Anthropic tools array into OpenAI
// function declarations. Entries already in OpenAI shape pass through untouched.
func convertToolDeclarationsToOpenAI(raw json.RawMessage) json.RawMessage {
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return raw
	}

	out := make([]json.RawMessage, 0, len(tools))
	changed := false
	for _, tool := range tools {
		var obj map[string]json.RawMessage
		if json.Unmarshal(tool, &obj) != nil {
			out = append(out, tool)
			continue
		}
		if _, alreadyOpenAI := obj["function"]; alreadyOpenAI {
			out = append(out, tool)
			continue
		}

		var anthropic struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"input_schema"`
		}
		if json.Unmarshal(tool, &anthropic) != nil || anthropic.Name == "" {
			out = append(out, tool)
			continue
		}

		fn := map[string]json.RawMessage{"name": marshalRaw(anthropic.Name)}
		if anthropic.Description != "" {
			fn["description"] = marshalRaw(anthropic.Description)
		}
		schema := anthropic.InputSchema
		if len(schema) == 0 || string(schema) == "null" {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		fn["parameters"] = schema

		out = append(out, marshalRaw(map[string]json.RawMessage{
			"type":     json.RawMessage(`"function"`),
			"function": marshalRaw(fn),
		}))
		changed = true
	}

	if !changed {
		return raw
	}
	return marshalRaw(out)
}

// convertToolChoiceToOpenAI rewrites an Anthropic tool_choice value into OpenAI form.
// It also reports whether parallel tool use must be disabled.
func convertToolChoiceToOpenAI(raw json.RawMessage) (json.RawMessage, bool, bool) {
	var obj struct {
		Type                   string `json:"type"`
		Name                   string `json:"name"`
		DisableParallelToolUse bool   `json:"disable_parallel_tool_use"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return nil, false, false
	}

	switch obj.Type {
	case "auto":
		return json.RawMessage(`"auto"`), obj.DisableParallelToolUse, true
	case "any":
		return json.RawMessage(`"required"`), obj.DisableParallelToolUse, true
	case "tool":
		if obj.Name == "" {
			return json.RawMessage(`"auto"`), obj.DisableParallelToolUse, true
		}
		choice, err := json.Marshal(map[string]interface{}{
			"type":     "function",
			"function": map[string]string{"name": obj.Name},
		})
		if err != nil {
			return nil, false, false
		}
		return choice, obj.DisableParallelToolUse, true
	}
	return nil, false, false
}

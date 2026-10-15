package transform

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// This file bridges OpenAI tool-calling wire shapes to the Anthropic content-block
// shapes SAP AI Core Bedrock accepts. It is used only by the OpenAI surface
// (/openai/v1/*); the native Anthropic surface passes messages through untouched.

// ConvertOpenAIToolMessagesToAnthropic rewrites OpenAI tool-calling messages into
// Anthropic content blocks:
//
//	{"role":"assistant","tool_calls":[...]}   -> {"role":"assistant","content":[{"type":"text",...},{"type":"tool_use",...}]}
//	{"role":"tool","tool_call_id":"...",...}  -> {"role":"user","content":[{"type":"tool_result","tool_use_id":"..."}]}
//
// Anthropic rejects a bare "tool" role outright, so without this step every follow-up
// turn of an OpenAI tool-calling loop fails with `Unexpected role "tool"`.
//
// Consecutive tool results are merged into a single user message, because Anthropic
// requires every tool_result belonging to one assistant turn to arrive together.
//
// The map is modified in place and returned for chaining.
func ConvertOpenAIToolMessagesToAnthropic(m map[string]json.RawMessage) map[string]json.RawMessage {
	msgsRaw, ok := m["messages"]
	if !ok {
		return m
	}

	var msgs []json.RawMessage
	if err := json.Unmarshal(msgsRaw, &msgs); err != nil {
		return m
	}

	out := make([]json.RawMessage, 0, len(msgs))
	// pending maps a tool name to the tool_use ids not yet answered, so legacy
	// {"role":"function","name":"..."} messages (which carry no tool_call_id) resolve.
	pending := map[string][]string{}
	// knownToolUseIDs guards against emitting a tool_result for a tool_use that does
	// not exist in this request — Anthropic rejects those with a 400.
	knownToolUseIDs := map[string]bool{}
	changed := false

	for _, rawMsg := range msgs {
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(rawMsg, &msg); err != nil {
			out = append(out, rawMsg)
			continue
		}

		var role string
		_ = json.Unmarshal(msg["role"], &role)

		switch role {
		case "assistant":
			converted, ids, ok := assistantMessageWithToolUse(msg)
			if !ok {
				out = append(out, rawMsg)
				continue
			}
			for name, id := range ids {
				pending[name] = append(pending[name], id)
				knownToolUseIDs[id] = true
			}
			out = append(out, converted)
			changed = true

		case "tool", "function":
			block, ok := toolResultBlock(msg, pending, knownToolUseIDs)
			if !ok {
				slog.Warn("dropping tool message that cannot be mapped to a tool_use id", "role", role)
				changed = true
				continue
			}
			if len(out) > 0 {
				if merged, ok := appendToolResultBlock(out[len(out)-1], block); ok {
					out[len(out)-1] = merged
					changed = true
					continue
				}
			}
			wrapper, _ := json.Marshal(map[string]json.RawMessage{
				"role":    json.RawMessage(`"user"`),
				"content": marshalRaw([]json.RawMessage{block}),
			})
			out = append(out, wrapper)
			changed = true

		default:
			out = append(out, rawMsg)
		}
	}

	if !changed {
		return m
	}
	m["messages"] = marshalRaw(out)
	return m
}

// ConvertOpenAIToolsToAnthropic rewrites OpenAI tool declarations and tool_choice
// into the Anthropic shapes Bedrock expects:
//
//	tools:       {"type":"function","function":{"name","description","parameters"}}
//	          -> {"name","description","input_schema"}
//	tool_choice: {"type":"function","function":{"name":"x"}} -> {"type":"tool","name":"x"}
//	             "auto" -> {"type":"auto"}, "required" -> {"type":"any"}
//	             "none" -> tools are dropped (Anthropic has no "none")
//
// parallel_tool_calls:false is folded into tool_choice.disable_parallel_tool_use.
//
// The map is modified in place and returned for chaining.
func ConvertOpenAIToolsToAnthropic(m map[string]json.RawMessage) map[string]json.RawMessage {
	parallelDisabled := false
	if raw, ok := m["parallel_tool_calls"]; ok {
		var parallel bool
		if json.Unmarshal(raw, &parallel) == nil && !parallel {
			parallelDisabled = true
		}
		delete(m, "parallel_tool_calls")
	}

	toolsRaw, hasTools := m["tools"]
	choiceRaw, hasChoice := m["tool_choice"]

	var choiceStr string
	if hasChoice {
		_ = json.Unmarshal(choiceRaw, &choiceStr)
	}

	// Anthropic has no "none". Emulate it by removing the declarations entirely —
	// leaving them in place would still let the model call the tools.
	if choiceStr == "none" {
		delete(m, "tools")
		delete(m, "tool_choice")
		return m
	}

	if !hasTools {
		// tool_choice is meaningless without tools and Anthropic rejects it.
		delete(m, "tool_choice")
		return m
	}

	m["tools"] = convertToolDeclarations(toolsRaw)

	choice, ok := convertToolChoice(choiceRaw, hasChoice, choiceStr, parallelDisabled)
	if !ok && parallelDisabled {
		choice, ok = buildToolChoice("auto", "", true), true
	}
	if ok {
		m["tool_choice"] = choice
	} else {
		delete(m, "tool_choice")
	}
	return m
}

// assistantMessageWithToolUse converts one OpenAI assistant message carrying
// tool_calls into an Anthropic assistant message with tool_use blocks.
// It also returns the tool name -> tool_use id mapping for later result matching.
func assistantMessageWithToolUse(msg map[string]json.RawMessage) (json.RawMessage, map[string]string, bool) {
	callsRaw, ok := msg["tool_calls"]
	if !ok {
		return nil, nil, false
	}

	var calls []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal(callsRaw, &calls); err != nil || len(calls) == 0 {
		return nil, nil, false
	}

	// Any assistant text must precede the tool_use blocks.
	blocks := contentBlocks(msg["content"])
	ids := make(map[string]string, len(calls))

	for i, call := range calls {
		id := call.ID
		if id == "" {
			id = fmt.Sprintf("toolu_autogen_%d", i)
		}
		if call.Function.Name != "" {
			ids[call.Function.Name] = id
		}
		block, err := json.Marshal(map[string]json.RawMessage{
			"type":  json.RawMessage(`"tool_use"`),
			"id":    marshalRaw(id),
			"name":  marshalRaw(call.Function.Name),
			"input": toolArguments(call.Function.Name, call.Function.Arguments),
		})
		if err != nil {
			continue
		}
		blocks = append(blocks, block)
	}

	converted := make(map[string]json.RawMessage, len(msg))
	for k, v := range msg {
		// tool_calls is OpenAI-only; Anthropic rejects unknown message fields.
		if k == "tool_calls" {
			continue
		}
		converted[k] = v
	}
	converted["content"] = marshalRaw(blocks)

	b, err := json.Marshal(converted)
	if err != nil {
		return nil, nil, false
	}
	return b, ids, true
}

// toolResultBlock converts one OpenAI tool/function message into an Anthropic
// tool_result block. It returns false when no matching tool_use can be found.
func toolResultBlock(msg map[string]json.RawMessage, pending map[string][]string, known map[string]bool) (json.RawMessage, bool) {
	var id string
	if raw, ok := msg["tool_call_id"]; ok {
		_ = json.Unmarshal(raw, &id)
	}
	if id == "" {
		// Legacy {"role":"function","name":"..."} carries no id — consume the oldest
		// unanswered tool_use with a matching name.
		var name string
		if raw, ok := msg["name"]; ok {
			_ = json.Unmarshal(raw, &name)
		}
		ids := pending[name]
		if len(ids) == 0 {
			return nil, false
		}
		id = ids[0]
		pending[name] = ids[1:]
	}
	if !known[id] {
		return nil, false
	}

	block, err := json.Marshal(map[string]json.RawMessage{
		"type":        json.RawMessage(`"tool_result"`),
		"tool_use_id": marshalRaw(id),
		"content":     toolResultContent(msg["content"]),
	})
	if err != nil {
		return nil, false
	}
	return block, true
}

// appendToolResultBlock appends block to prev when prev is a user message made
// entirely of tool_result blocks, merging consecutive tool results into one turn.
func appendToolResultBlock(prev, block json.RawMessage) (json.RawMessage, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(prev, &obj) != nil {
		return nil, false
	}
	var role string
	if json.Unmarshal(obj["role"], &role) != nil || role != "user" {
		return nil, false
	}
	contentRaw, ok := obj["content"]
	if !ok {
		return nil, false
	}
	var blocks []json.RawMessage
	if json.Unmarshal(contentRaw, &blocks) != nil || len(blocks) == 0 {
		return nil, false
	}
	// Only merge into a pure tool_result turn: Anthropic requires tool_result
	// blocks to lead the user message, so appending after text would be invalid.
	for _, b := range blocks {
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(b, &probe) != nil || probe.Type != "tool_result" {
			return nil, false
		}
	}

	obj["content"] = marshalRaw(append(blocks, block))
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, false
	}
	return b, true
}

// contentBlocks normalises an OpenAI message content field into Anthropic blocks.
// A plain string becomes a single text block; an existing array is kept as-is.
func contentBlocks(raw json.RawMessage) []json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil
		}
		return []json.RawMessage{marshalRaw(map[string]string{"type": "text", "text": s})}
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		return arr
	}
	return nil
}

// toolArguments turns OpenAI's JSON-encoded argument string into the JSON object
// Anthropic expects for tool_use.input.
func toolArguments(name, args string) json.RawMessage {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	slog.Warn("tool_call arguments are not valid JSON, sending an empty object", "tool", name)
	return json.RawMessage(`{}`)
}

// toolResultContent normalises an OpenAI tool message content field into the value
// Anthropic accepts for tool_result.content (a string, or an array of blocks).
func toolResultContent(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`""`)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return marshalRaw(s)
	}

	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return json.RawMessage(`""`)
	}
	var blocks []json.RawMessage
	for _, part := range parts {
		var p struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		if json.Unmarshal(part, &p) != nil {
			continue
		}
		switch p.Type {
		case "text":
			blocks = append(blocks, marshalRaw(map[string]string{"type": "text", "text": p.Text}))
		case "image_url":
			if block, ok := dataURIToAnthropicImage(p.ImageURL.URL); ok {
				blocks = append(blocks, block)
			}
		}
	}
	if len(blocks) == 0 {
		return json.RawMessage(`""`)
	}
	return marshalRaw(blocks)
}

// dataURIToAnthropicImage converts a base64 data URI into an Anthropic image block.
// Remote URLs are not supported by SAP AI Core Bedrock and are rejected.
func dataURIToAnthropicImage(url string) (json.RawMessage, bool) {
	if !strings.HasPrefix(url, "data:") {
		return nil, false
	}
	mimeType := "image/jpeg"
	data := url
	if idx := strings.Index(url, ";base64,"); idx >= 0 {
		mimeType = url[5:idx]
		data = url[idx+8:]
	}
	b, err := json.Marshal(map[string]interface{}{
		"type": "image",
		"source": map[string]string{
			"type":       "base64",
			"media_type": mimeType,
			"data":       data,
		},
	})
	if err != nil {
		return nil, false
	}
	return b, true
}

// convertToolDeclarations rewrites an OpenAI tools array into Anthropic tool
// declarations. Entries that are already Anthropic-shaped pass through untouched.
func convertToolDeclarations(raw json.RawMessage) json.RawMessage {
	var tools []json.RawMessage
	if err := json.Unmarshal(raw, &tools); err != nil {
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
		if _, alreadyAnthropic := obj["name"]; alreadyAnthropic {
			out = append(out, tool)
			continue
		}
		fnRaw, ok := obj["function"]
		if !ok {
			out = append(out, tool)
			continue
		}
		var fn struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		}
		if json.Unmarshal(fnRaw, &fn) != nil || fn.Name == "" {
			out = append(out, tool)
			continue
		}

		anthropic := map[string]json.RawMessage{"name": marshalRaw(fn.Name)}
		if fn.Description != "" {
			anthropic["description"] = marshalRaw(fn.Description)
		}
		schema := fn.Parameters
		if len(schema) == 0 || string(schema) == "null" {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		anthropic["input_schema"] = schema

		out = append(out, marshalRaw(anthropic))
		changed = true
	}

	if !changed {
		return raw
	}
	return marshalRaw(out)
}

// convertToolChoice rewrites an OpenAI tool_choice value into Anthropic form.
func convertToolChoice(raw json.RawMessage, has bool, asString string, parallelDisabled bool) (json.RawMessage, bool) {
	if !has {
		return nil, false
	}

	switch asString {
	case "auto":
		return buildToolChoice("auto", "", parallelDisabled), true
	case "required":
		return buildToolChoice("any", "", parallelDisabled), true
	case "none":
		// Handled by the caller, which drops the tools instead.
		return nil, false
	}

	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return nil, false
	}

	switch obj.Type {
	case "function":
		if obj.Function.Name == "" {
			return buildToolChoice("auto", "", parallelDisabled), true
		}
		return buildToolChoice("tool", obj.Function.Name, parallelDisabled), true
	case "tool":
		if obj.Function.Name == "" {
			return buildToolChoice("auto", "", parallelDisabled), true
		}
		return buildToolChoice("tool", obj.Function.Name, parallelDisabled), true
	case "auto":
		return buildToolChoice("auto", "", parallelDisabled), true
	case "any", "required":
		return buildToolChoice("any", "", parallelDisabled), true
	}
	return nil, false
}

func buildToolChoice(kind, name string, parallelDisabled bool) json.RawMessage {
	choice := map[string]interface{}{"type": kind}
	if kind == "tool" && name != "" {
		choice["name"] = name
	}
	if parallelDisabled {
		choice["disable_parallel_tool_use"] = true
	}
	return marshalRaw(choice)
}

func marshalRaw(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

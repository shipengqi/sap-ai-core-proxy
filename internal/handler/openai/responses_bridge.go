package openai

import (
	"encoding/json"
	"strings"
)

// responsesPath is the Responses endpoint on a deployment URL.
const responsesPath = "/responses?api-version=2024-02-01"

// chatToResponsesBody rewrites an OpenAI chat/completions request into the
// Responses API shape.
//
// This exists for one reason: some models (gpt-6.x) accept function tools only
// on /responses and answer every tool-bearing /chat/completions request with
// 400. The caller reaches this only after upstream refused the chat body, so a
// lossy mapping is acceptable — but anything that changes the meaning of a
// message (tool calls, tool results, images) is carried over explicitly.
func chatToResponsesBody(raw map[string]json.RawMessage) []byte {
	out := map[string]interface{}{}
	instructions := make([]string, 0, 1)
	input := make([]interface{}, 0, 8)

	type toolCall struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	type message struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCalls  []toolCall      `json:"tool_calls"`
		ToolCallID string          `json:"tool_call_id"`
	}

	var msgs []message
	if v, ok := raw["messages"]; ok {
		_ = json.Unmarshal(v, &msgs)
	}
	for _, m := range msgs {
		switch m.Role {
		case "system", "developer":
			if text, ok := plainText(m.Content); ok && text != "" {
				instructions = append(instructions, text)
			}
		case "tool":
			input = append(input, map[string]interface{}{
				"type":    "function_call_output",
				"call_id": m.ToolCallID,
				"output":  plainTextOrEmpty(m.Content),
			})
		default:
			if content, ok := responsesContent(m.Content, m.Role); ok {
				input = append(input, map[string]interface{}{"role": m.Role, "content": content})
			}
			// An assistant turn's tool calls are separate input items in the
			// Responses API, and they must follow the message they came from.
			for _, tc := range m.ToolCalls {
				input = append(input, map[string]interface{}{
					"type":      "function_call",
					"call_id":   tc.ID,
					"name":      tc.Function.Name,
					"arguments": tc.Function.Arguments,
				})
			}
		}
	}
	if len(instructions) > 0 {
		out["instructions"] = strings.Join(instructions, "\n\n")
	}
	out["input"] = input

	if v, ok := raw["tools"]; ok {
		out["tools"] = responsesTools(v)
	}
	if v, ok := raw["tool_choice"]; ok {
		out["tool_choice"] = responsesToolChoice(v)
	}
	if v, ok := raw["max_completion_tokens"]; ok {
		out["max_output_tokens"] = v
	} else if v, ok := raw["max_tokens"]; ok {
		out["max_output_tokens"] = v
	}
	// "none" is not a value these models accept, and omitting the block keeps
	// the model's default effort; sending the literal would 400.
	if v, ok := raw["reasoning_effort"]; ok {
		var effort string
		if json.Unmarshal(v, &effort) == nil && effort != "" && effort != "none" {
			out["reasoning"] = map[string]interface{}{"effort": effort}
		}
	}
	if v, ok := raw["response_format"]; ok {
		if format := responsesTextFormat(v); format != nil {
			out["text"] = map[string]interface{}{"format": format}
		}
	}
	for _, k := range []string{"temperature", "top_p", "parallel_tool_calls", "user"} {
		if v, ok := raw[k]; ok {
			out[k] = v
		}
	}
	var streaming bool
	if v, ok := raw["stream"]; ok {
		_ = json.Unmarshal(v, &streaming)
	}
	out["stream"] = streaming
	// The Responses API persists responses by default. A proxy must not leave
	// the caller's prompts on a third-party store as a side effect of a retry.
	out["store"] = false

	body, _ := json.Marshal(out)
	return body
}

// responsesContent converts one message's content into the shape the Responses
// API expects: a plain string when it is text only, or typed parts when images
// are involved.
func responsesContent(raw json.RawMessage, role string) (interface{}, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, text != ""
	}

	var parts []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		ImageURL json.RawMessage `json:"image_url"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return nil, false
	}
	textType := "input_text"
	if role == "assistant" {
		textType = "output_text"
	}
	converted := make([]interface{}, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case "text":
			converted = append(converted, map[string]interface{}{"type": textType, "text": p.Text})
		case "image_url":
			var ref struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(p.ImageURL, &ref) == nil && ref.URL != "" {
				converted = append(converted, map[string]interface{}{"type": "input_image", "image_url": ref.URL})
			} else {
				var url string
				if json.Unmarshal(p.ImageURL, &url) == nil && url != "" {
					converted = append(converted, map[string]interface{}{"type": "input_image", "image_url": url})
				}
			}
		}
	}
	return converted, len(converted) > 0
}

// responsesTools flattens the chat tool shape ({type:function,function:{...}})
// into the Responses shape ({type:function,name,...}). Already-flat entries are
// passed through, so the bridge also accepts Responses-style tools.
func responsesTools(raw json.RawMessage) []interface{} {
	var tools []map[string]json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return nil
	}
	converted := make([]interface{}, 0, len(tools))
	for _, t := range tools {
		fields := t
		if inner, ok := t["function"]; ok {
			var fn map[string]json.RawMessage
			if json.Unmarshal(inner, &fn) != nil {
				continue
			}
			fields = fn
		}
		item := map[string]interface{}{"type": "function"}
		for _, k := range []string{"name", "description", "parameters", "strict"} {
			if v, ok := fields[k]; ok {
				item[k] = v
			}
		}
		converted = append(converted, item)
	}
	return converted
}

func responsesToolChoice(raw json.RawMessage) interface{} {
	var mode string
	if json.Unmarshal(raw, &mode) == nil {
		return mode
	}
	var named struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &named) == nil && named.Function.Name != "" {
		return map[string]interface{}{"type": "function", "name": named.Function.Name}
	}
	return nil
}

func responsesTextFormat(raw json.RawMessage) interface{} {
	var format struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Name   string          `json:"name"`
			Schema json.RawMessage `json:"schema"`
			Strict bool            `json:"strict"`
		} `json:"json_schema"`
	}
	if json.Unmarshal(raw, &format) != nil {
		return nil
	}
	switch format.Type {
	case "json_object":
		return map[string]interface{}{"type": "json_object"}
	case "json_schema":
		if len(format.JSONSchema.Schema) == 0 {
			return nil
		}
		return map[string]interface{}{
			"type":   "json_schema",
			"name":   format.JSONSchema.Name,
			"schema": format.JSONSchema.Schema,
			"strict": format.JSONSchema.Strict,
		}
	}
	return nil
}

// plainText returns the text of a string or a text-block array content field.
func plainText(raw json.RawMessage) (string, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, true
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return "", false
	}
	var sb strings.Builder
	for _, p := range parts {
		if p.Type == "text" || p.Type == "input_text" || p.Type == "output_text" {
			sb.WriteString(p.Text)
		}
	}
	return sb.String(), true
}

func plainTextOrEmpty(raw json.RawMessage) string {
	text, _ := plainText(raw)
	return text
}

// responsesToChatCompletion converts a Responses API body into a chat.completion
// so the caller sees the surface it asked for.
func responsesToChatCompletion(data []byte, model string) []byte {
	var resp struct {
		ID                string `json:"id"`
		Status            string `json:"status"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
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
	if err := json.Unmarshal(data, &resp); err != nil {
		return data
	}

	var content strings.Builder
	toolCalls := make([]interface{}, 0, 2)
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" {
					content.WriteString(part.Text)
				}
			}
		case "function_call":
			args := item.Arguments
			if args == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, map[string]interface{}{
				"id":   item.CallID,
				"type": "function",
				"function": map[string]string{
					"name":      item.Name,
					"arguments": args,
				},
			})
		}
	}

	finishReason := "stop"
	switch {
	case len(toolCalls) > 0:
		finishReason = "tool_calls"
	case resp.Status == "incomplete" && resp.IncompleteDetails != nil && resp.IncompleteDetails.Reason == "max_output_tokens":
		finishReason = "length"
	}

	message := map[string]interface{}{"role": "assistant", "content": content.String()}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
		if content.Len() == 0 {
			message["content"] = nil
		}
	}

	usage := map[string]interface{}{
		"prompt_tokens":     resp.Usage.InputTokens,
		"completion_tokens": resp.Usage.OutputTokens,
		"total_tokens":      resp.Usage.TotalTokens,
		"prompt_tokens_details": map[string]int{
			"cached_tokens": resp.Usage.InputTokensDetails.CachedTokens,
		},
		"completion_tokens_details": map[string]int{
			"reasoning_tokens": resp.Usage.OutputTokensDetails.ReasoningTokens,
		},
	}
	if usage["total_tokens"] == 0 {
		usage["total_tokens"] = resp.Usage.InputTokens + resp.Usage.OutputTokens
	}

	id := resp.ID
	if id == "" {
		id = "chatcmpl-responses"
	}
	out, _ := json.Marshal(map[string]interface{}{
		"id":     "chatcmpl-" + id,
		"object": "chat.completion",
		"model":  model,
		"choices": []map[string]interface{}{
			{"index": 0, "message": message, "finish_reason": finishReason},
		},
		"usage": usage,
	})
	return out
}

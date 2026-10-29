package transform

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

type GeminiFunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}
type GeminiFunctionResponse struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name"`
	Response any    `json:"response"`
}
type GeminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       json.RawMessage         `json:"inlineData,omitempty"`
	FunctionCall     *GeminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *GeminiFunctionResponse `json:"functionResponse,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
}
type GeminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []GeminiPart `json:"parts"`
}
type GeminiResponse struct {
	Candidates []struct {
		Index        int           `json:"index"`
		Content      GeminiContent `json:"content"`
		FinishReason string        `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		Input    int `json:"promptTokenCount"`
		Output   int `json:"candidatesTokenCount"`
		Total    int `json:"totalTokenCount"`
		Cached   int `json:"cachedContentTokenCount"`
		Thinking int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
	PromptFeedback json.RawMessage `json:"promptFeedback"`
	Error          json.RawMessage `json:"error"`
}

func GeminiCallID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "call_gemini"
	}
	return "call_" + hex.EncodeToString(b[:])
}
func GeminiFinishReason(reason string, tools bool) string {
	switch reason {
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "OTHER":
		return "content_filter"
	}
	if tools {
		return "tool_calls"
	}
	return "stop"
}
func GeminiUsage(g GeminiResponse) map[string]any {
	u := g.UsageMetadata
	output := u.Output + u.Thinking
	total := u.Total
	if total == 0 {
		total = u.Input + output
	}
	return map[string]any{"prompt_tokens": u.Input, "completion_tokens": output, "total_tokens": total, "prompt_tokens_details": map[string]int{"cached_tokens": u.Cached}, "completion_tokens_details": map[string]int{"reasoning_tokens": u.Thinking}}
}
func GeminiToolCall(part GeminiPart) map[string]any {
	fc := part.FunctionCall
	id := fc.ID
	if id == "" {
		id = GeminiCallID()
	}
	args := fc.Args
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	call := map[string]any{"id": id, "type": "function", "function": map[string]string{"name": fc.Name, "arguments": string(args)}}
	if part.ThoughtSignature != "" {
		call["extra_content"] = map[string]any{"google": map[string]string{"thought_signature": part.ThoughtSignature}}
	}
	return call
}

func OpenAIToGeminiBody(raw map[string]json.RawMessage) ([]byte, error) {
	type call struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
		Extra struct {
			Google struct {
				Signature string `json:"thought_signature"`
			} `json:"google"`
		} `json:"extra_content"`
	}
	var messages []struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCalls  []call          `json:"tool_calls"`
		ToolCallID string          `json:"tool_call_id"`
	}
	if json.Unmarshal(raw["messages"], &messages) != nil || len(messages) == 0 {
		return nil, fmt.Errorf("messages must be a non-empty array")
	}
	contents := make([]GeminiContent, 0, len(messages))
	system := []GeminiPart{}
	names := map[string]string{}
	for _, m := range messages {
		if m.Role == "tool" {
			name, ok := names[m.ToolCallID]
			if !ok {
				return nil, fmt.Errorf("tool result references unknown call %q", m.ToolCallID)
			}
			var output any
			if json.Unmarshal(m.Content, &output) != nil {
				return nil, fmt.Errorf("invalid tool output")
			}
			if s, ok := output.(string); ok {
				var parsed any
				if json.Unmarshal([]byte(s), &parsed) == nil {
					output = parsed
				}
			}
			if _, ok := output.(map[string]any); !ok {
				output = map[string]any{"result": output}
			}
			part := GeminiPart{FunctionResponse: &GeminiFunctionResponse{ID: m.ToolCallID, Name: name, Response: output}}
			if len(contents) > 0 && contents[len(contents)-1].Role == "user" {
				contents[len(contents)-1].Parts = append(contents[len(contents)-1].Parts, part)
			} else {
				contents = append(contents, GeminiContent{Role: "user", Parts: []GeminiPart{part}})
			}
			continue
		}
		parts, err := geminiMessageParts(m.Content)
		if err != nil {
			return nil, err
		}
		if m.Role == "system" || m.Role == "developer" {
			system = append(system, parts...)
			continue
		}
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		} else if m.Role != "user" {
			return nil, fmt.Errorf("unsupported message role %q", m.Role)
		}
		for _, tc := range m.ToolCalls {
			if tc.Type != "function" && tc.Type != "" {
				return nil, fmt.Errorf("unsupported tool type %q", tc.Type)
			}
			if tc.ID == "" || tc.Function.Name == "" {
				return nil, fmt.Errorf("tool call requires id and name")
			}
			if _, duplicate := names[tc.ID]; duplicate {
				return nil, fmt.Errorf("duplicate tool call id %q", tc.ID)
			}
			args := json.RawMessage(tc.Function.Arguments)
			var obj map[string]any
			if json.Unmarshal(args, &obj) != nil || obj == nil {
				return nil, fmt.Errorf("tool arguments must be a JSON object")
			}
			names[tc.ID] = tc.Function.Name
			parts = append(parts, GeminiPart{FunctionCall: &GeminiFunctionCall{ID: tc.ID, Name: tc.Function.Name, Args: args}, ThoughtSignature: tc.Extra.Google.Signature})
		}
		if len(parts) > 0 {
			contents = append(contents, GeminiContent{Role: role, Parts: parts})
		}
	}
	out := map[string]any{"contents": contents}
	if len(system) > 0 {
		out["systemInstruction"] = GeminiContent{Parts: system}
	}
	config := map[string]any{}
	for from, to := range map[string]string{"temperature": "temperature", "top_p": "topP", "max_tokens": "maxOutputTokens", "max_completion_tokens": "maxOutputTokens", "n": "candidateCount"} {
		if v, ok := raw[from]; ok {
			config[to] = v
		}
	}
	if v, ok := raw["max_completion_tokens"]; ok {
		config["maxOutputTokens"] = v
	}
	if v, ok := raw["stop"]; ok {
		var s string
		var list []string
		if json.Unmarshal(v, &s) == nil {
			list = []string{s}
		} else if json.Unmarshal(v, &list) != nil {
			return nil, fmt.Errorf("stop must be a string or string array")
		}
		config["stopSequences"] = list
	}
	if v, ok := raw["response_format"]; ok {
		var format struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Schema json.RawMessage `json:"schema"`
			} `json:"json_schema"`
		}
		if json.Unmarshal(v, &format) != nil {
			return nil, fmt.Errorf("invalid response_format")
		}
		if format.Type == "json_object" || format.Type == "json_schema" {
			config["responseMimeType"] = "application/json"
			if format.Type == "json_schema" {
				config["responseJsonSchema"] = format.JSONSchema.Schema
			}
		}
	}
	if v, ok := raw["reasoning_effort"]; ok {
		var effort string
		if json.Unmarshal(v, &effort) != nil {
			return nil, fmt.Errorf("reasoning_effort must be a string")
		}
		if effort != "none" {
			return nil, fmt.Errorf("reasoning_effort mapping for this Gemini deployment is not configured; use the native Gemini API")
		}
		config["thinkingConfig"] = map[string]int{"thinkingBudget": 0}
	}
	if len(config) > 0 {
		out["generationConfig"] = config
	}
	if v, ok := raw["tools"]; ok {
		var tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description,omitempty"`
				Parameters  json.RawMessage `json:"parameters,omitempty"`
			} `json:"function"`
		}
		if json.Unmarshal(v, &tools) != nil {
			return nil, fmt.Errorf("tools must be an array")
		}
		declarations := []any{}
		for _, t := range tools {
			if t.Type != "function" || t.Function.Name == "" {
				return nil, fmt.Errorf("only function tools are supported")
			}
			declarations = append(declarations, t.Function)
		}
		if len(declarations) > 0 {
			out["tools"] = []any{map[string]any{"functionDeclarations": declarations}}
		}
	}
	if v, ok := raw["parallel_tool_calls"]; ok && string(v) == "false" && raw["tools"] != nil {
		return nil, fmt.Errorf("parallel_tool_calls:false cannot be enforced by this Gemini bridge")
	}
	if v, ok := raw["tool_choice"]; ok {
		var mode string
		fc := map[string]any{}
		if json.Unmarshal(v, &mode) == nil {
			switch mode {
			case "auto":
				fc["mode"] = "AUTO"
			case "none":
				fc["mode"] = "NONE"
			case "required":
				fc["mode"] = "ANY"
			default:
				return nil, fmt.Errorf("invalid tool_choice")
			}
		} else {
			var named struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			if json.Unmarshal(v, &named) != nil || named.Function.Name == "" {
				return nil, fmt.Errorf("invalid tool_choice")
			}
			fc["mode"] = "ANY"
			fc["allowedFunctionNames"] = []string{named.Function.Name}
		}
		out["toolConfig"] = map[string]any{"functionCallingConfig": fc}
	}
	return json.Marshal(out)
}

func geminiMessageParts(raw json.RawMessage) ([]GeminiPart, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if text == "" {
			return nil, nil
		}
		return []GeminiPart{{Text: text}}, nil
	}
	var parts []struct {
		Type  string `json:"type"`
		Text  string `json:"text"`
		Image struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return nil, fmt.Errorf("invalid message content")
	}
	result := []GeminiPart{}
	for _, p := range parts {
		switch p.Type {
		case "text":
			result = append(result, GeminiPart{Text: p.Text})
		case "image_url":
			url := p.Image.URL
			if !strings.HasPrefix(url, "data:") {
				return nil, fmt.Errorf("remote images require the native Gemini API")
			}
			fields := strings.SplitN(strings.TrimPrefix(url, "data:"), ";base64,", 2)
			if len(fields) != 2 {
				return nil, fmt.Errorf("image must be a base64 data URI")
			}
			b, _ := json.Marshal(map[string]string{"mimeType": fields[0], "data": fields[1]})
			result = append(result, GeminiPart{InlineData: b})
		default:
			return nil, fmt.Errorf("unsupported content type %q", p.Type)
		}
	}
	return result, nil
}

func GeminiToOpenAIResponse(data []byte, model string) []byte {
	var g GeminiResponse
	if json.Unmarshal(data, &g) != nil {
		return geminiError("invalid Gemini JSON")
	}
	if len(g.Error) > 0 && string(g.Error) != "null" {
		return data
	}
	if len(g.Candidates) == 0 {
		return geminiError("Gemini returned no candidates: " + string(g.PromptFeedback))
	}
	choices := []any{}
	for index, c := range g.Candidates {
		text, reasoning := "", ""
		calls := []any{}
		for _, p := range c.Content.Parts {
			if p.Thought {
				reasoning += p.Text
			} else {
				text += p.Text
			}
			if p.FunctionCall != nil {
				calls = append(calls, GeminiToolCall(p))
			}
		}
		m := map[string]any{"role": "assistant", "content": text}
		if len(calls) > 0 {
			m["tool_calls"] = calls
			if text == "" {
				m["content"] = nil
			}
		}
		if reasoning != "" {
			m["reasoning_content"] = reasoning
		}
		choices = append(choices, map[string]any{"index": index, "message": m, "finish_reason": GeminiFinishReason(c.FinishReason, len(calls) > 0)})
	}
	b, _ := json.Marshal(map[string]any{"id": "chatcmpl-" + GeminiCallID(), "object": "chat.completion", "model": model, "choices": choices, "usage": GeminiUsage(g)})
	return b
}
func geminiError(message string) []byte {
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"type": "upstream_error", "message": message}})
	return b
}

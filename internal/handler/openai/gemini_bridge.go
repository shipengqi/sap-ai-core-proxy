package openai

import (
	"encoding/json"
)

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiGenConfig struct {
	Temperature     *json.RawMessage `json:"temperature,omitempty"`
	TopP            *json.RawMessage `json:"topP,omitempty"`
	MaxOutputTokens *json.RawMessage `json:"maxOutputTokens,omitempty"`
	StopSequences   []string         `json:"stopSequences,omitempty"`
	CandidateCount  *int             `json:"candidateCount,omitempty"`
}

type geminiRequestBody struct {
	Contents          []geminiContent  `json:"contents"`
	SystemInstruction *geminiContent   `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenConfig `json:"generationConfig,omitempty"`
}

// openAIToGeminiBody converts an OpenAI chat request to Gemini generateContent format.
func openAIToGeminiBody(raw map[string]json.RawMessage) []byte {
	var messages []map[string]json.RawMessage
	if m, ok := raw["messages"]; ok {
		_ = json.Unmarshal(m, &messages)
	}

	var contents []geminiContent
	var sysInstruction *geminiContent

	for _, msg := range messages {
		var role string
		if r, ok := msg["role"]; ok {
			_ = json.Unmarshal(r, &role)
		}
		textContent := extractMessageText(msg)

		switch role {
		case "system":
			sysInstruction = &geminiContent{
				Role:  "user",
				Parts: []geminiPart{{Text: textContent}},
			}
		case "assistant":
			contents = append(contents, geminiContent{
				Role:  "model",
				Parts: []geminiPart{{Text: textContent}},
			})
		default:
			contents = append(contents, geminiContent{
				Role:  "user",
				Parts: []geminiPart{{Text: textContent}},
			})
		}
	}

	gc := &geminiGenConfig{}
	hasConfig := false
	if v, ok := raw["temperature"]; ok {
		gc.Temperature = &v
		hasConfig = true
	}
	if v, ok := raw["top_p"]; ok {
		gc.TopP = &v
		hasConfig = true
	}
	if v, ok := raw["max_completion_tokens"]; ok {
		gc.MaxOutputTokens = &v
		hasConfig = true
	} else if v, ok := raw["max_tokens"]; ok {
		gc.MaxOutputTokens = &v
		hasConfig = true
	}
	if v, ok := raw["stop"]; ok {
		var s string
		var ss []string
		if json.Unmarshal(v, &s) == nil {
			gc.StopSequences = []string{s}
			hasConfig = true
		} else if json.Unmarshal(v, &ss) == nil {
			gc.StopSequences = ss
			hasConfig = true
		}
	}
	if v, ok := raw["n"]; ok {
		var n int
		if json.Unmarshal(v, &n) == nil {
			gc.CandidateCount = &n
			hasConfig = true
		}
	}

	b := geminiRequestBody{
		Contents:          contents,
		SystemInstruction: sysInstruction,
	}
	if hasConfig {
		b.GenerationConfig = gc
	}

	out, _ := json.Marshal(b)
	return out
}

func extractMessageText(msg map[string]json.RawMessage) string {
	c, ok := msg["content"]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(c, &s) == nil {
		return s
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(c, &parts) == nil {
		var result string
		for _, p := range parts {
			if t, ok := p["text"]; ok {
				var ts string
				_ = json.Unmarshal(t, &ts)
				result += ts
			}
		}
		return result
	}
	return ""
}

// openAIToGeminiImageBody converts an OpenAI images/generations request to Gemini generateContent format.
func openAIToGeminiImageBody(prompt string, n int) []byte {
	if n < 1 {
		n = 1
	}
	body := map[string]interface{}{
		"contents": []map[string]interface{}{
			{"role": "user", "parts": []map[string]string{{"text": prompt}}},
		},
		"generationConfig": map[string]interface{}{
			"candidateCount":     n,
			"responseModalities": []string{"IMAGE"},
		},
	}
	out, _ := json.Marshal(body)
	return out
}

// geminiImageToOpenAIResponse converts a Gemini generateContent image response
// to OpenAI images/generations format (b64_json).
func geminiImageToOpenAIResponse(data []byte) []byte {
	var gr struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData *struct {
						Data string `json:"data"`
					} `json:"inlineData,omitempty"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	if err := json.Unmarshal(data, &gr); err != nil {
		return data
	}

	type imageItem struct {
		B64JSON string `json:"b64_json"`
	}
	var items []imageItem
	for _, cand := range gr.Candidates {
		for _, p := range cand.Content.Parts {
			if p.InlineData != nil && p.InlineData.Data != "" {
				items = append(items, imageItem{B64JSON: p.InlineData.Data})
			}
		}
	}
	if len(items) == 0 {
		return data
	}

	out, _ := json.Marshal(map[string]interface{}{"data": items})
	return out
}

// geminiToOpenAIResponse converts a Gemini generateContent response to OpenAI chat completion format.
func geminiToOpenAIResponse(data []byte, model string) []byte {
	var gr struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}

	if err := json.Unmarshal(data, &gr); err != nil || len(gr.Candidates) == 0 {
		return data
	}

	content := ""
	for _, p := range gr.Candidates[0].Content.Parts {
		content += p.Text
	}

	finishReason := "stop"
	switch gr.Candidates[0].FinishReason {
	case "MAX_TOKENS":
		finishReason = "length"
	case "SAFETY", "RECITATION", "OTHER":
		finishReason = "content_filter"
	}

	resp := map[string]interface{}{
		"id":     "chatcmpl-gemini",
		"object": "chat.completion",
		"model":  model,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]string{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]int{
			"prompt_tokens":     gr.UsageMetadata.PromptTokenCount,
			"completion_tokens": gr.UsageMetadata.CandidatesTokenCount,
			"total_tokens":      gr.UsageMetadata.TotalTokenCount,
		},
	}

	out, _ := json.Marshal(resp)
	return out
}

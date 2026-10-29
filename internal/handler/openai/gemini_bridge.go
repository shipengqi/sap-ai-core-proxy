package openai

import (
	"encoding/json"
	"strings"

	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
	"github.com/shipengqi/sap-ai-core-proxy/internal/transform"
)

// openAIToGeminiBody validates and converts the complete chat/tool history.
func openAIToGeminiBody(raw map[string]json.RawMessage) []byte {
	body, err := transform.OpenAIToGeminiBody(raw)
	if err != nil {
		return bridgeResponseError(err.Error(), nil)
	}
	return body
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

func geminiToOpenAIResponse(data []byte, model string) []byte {
	return transform.GeminiToOpenAIResponse(data, model)
}

// SAP prefixes the discovery name; Gemini's model path uses the provider name.
func geminiModelName(dep *sapclient.Deployment) string {
	name := dep.EffectiveModelName()
	if _, stripped, ok := strings.Cut(name, "--"); ok {
		return stripped
	}
	return name
}

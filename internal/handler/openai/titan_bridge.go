package openai

import "encoding/json"

// openAIToTitanImageBody converts an OpenAI images/generations request to
// Amazon Titan Image Generator format.
func openAIToTitanImageBody(prompt string, n int, size string) []byte {
	if n < 1 {
		n = 1
	}
	width, height := 512, 512
	switch size {
	case "256x256":
		width, height = 256, 256
	case "512x512":
		width, height = 512, 512
	case "1024x1024":
		width, height = 1024, 1024
	}

	body := map[string]interface{}{
		"taskType": "TEXT_IMAGE",
		"textToImageParams": map[string]string{
			"text": prompt,
		},
		"imageGenerationConfig": map[string]interface{}{
			"numberOfImages": n,
			"quality":        "standard",
			"cfgScale":       8.0,
			"height":         height,
			"width":          width,
		},
	}
	out, _ := json.Marshal(body)
	return out
}

// titanToOpenAIImageResponse converts a Titan image response to OpenAI
// images/generations format (b64_json).
func titanToOpenAIImageResponse(data []byte) []byte {
	var tr struct {
		Images    []string `json:"images"`
		Artifacts []struct {
			Base64 string `json:"base64"`
		} `json:"artifacts"`
	}

	if err := json.Unmarshal(data, &tr); err != nil {
		return data
	}

	type imageItem struct {
		B64JSON string `json:"b64_json"`
	}
	var items []imageItem
	for _, img := range tr.Images {
		if img != "" {
			items = append(items, imageItem{B64JSON: img})
		}
	}
	for _, a := range tr.Artifacts {
		if a.Base64 != "" {
			items = append(items, imageItem{B64JSON: a.Base64})
		}
	}
	if len(items) == 0 {
		return data
	}

	out, _ := json.Marshal(map[string]interface{}{"data": items})
	return out
}

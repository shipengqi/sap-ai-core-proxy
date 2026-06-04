package stream

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
)

// ParseGeminiStream reads the Gemini API SSE stream.
func ParseGeminiStream(r io.Reader, handler func(GeminiEvent) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		raw := line[6:]

		var data map[string]any
		if err := json.Unmarshal([]byte(raw), &data); err != nil {
			slog.Debug("parseGeminiStream: skipping non-JSON line")
			continue
		}

		// candidates[0].content.parts
		if candidates, ok := data["candidates"].([]any); ok && len(candidates) > 0 {
			if cand, ok := candidates[0].(map[string]any); ok {
				if content, ok := cand["content"].(map[string]any); ok {
					if parts, ok := content["parts"].([]any); ok {
						for _, p := range parts {
							part, ok := p.(map[string]any)
							if !ok {
								continue
							}
							text, ok := part["text"].(string)
							if !ok || text == "" {
								continue
							}
							thought, _ := part["thought"].(bool)
							if thought {
								if err := handler(GeminiEvent{Type: GeminiReasoningDelta, Text: text}); err != nil {
									return err
								}
							} else {
								if err := handler(GeminiEvent{Type: GeminiTextDelta, Text: text}); err != nil {
									return err
								}
							}
						}
					}
				}
			}
		}

		// usageMetadata
		if usage, ok := data["usageMetadata"].(map[string]any); ok {
			if err := handler(GeminiEvent{
				Type:         GeminiMetadata,
				PromptTokens: intFrom(usage, "promptTokenCount"),
				OutputTokens: intFrom(usage, "candidatesTokenCount"),
			}); err != nil {
				return err
			}
		}
	}

	return scanner.Err()
}

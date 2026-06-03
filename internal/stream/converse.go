package stream

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
)

// ParseConverseStream reads line-delimited JSON from the SAP Converse SSE stream
// and calls handler for each parsed event.
func ParseConverseStream(r io.Reader, handler func(ConverseEvent) error) error {
	toolBlocks := make(map[int]bool) // tracks indices that are tool blocks
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Strip optional "data: " prefix
		raw := line
		if strings.HasPrefix(line, "data: ") {
			raw = line[6:]
		}

		// Normalise Python-style JSON (single quotes → double quotes)
		raw = normalizePythonJSON(raw)

		var data map[string]any
		if err := json.Unmarshal([]byte(raw), &data); err != nil {
			slog.Debug("parseConverseStream: skipping non-JSON line")
			continue
		}

		// metadata.usage
		if meta, ok := data["metadata"].(map[string]any); ok {
			if usage, ok := meta["usage"].(map[string]any); ok {
				input := intFrom(usage, "inputTokens") + intFrom(usage, "cacheReadInputTokens") + intFrom(usage, "cacheWriteInputTokens")
				if err := handler(ConverseEvent{Type: ConverseMetadata, InputTokens: input, OutputTokens: intFrom(usage, "outputTokens")}); err != nil {
					return err
				}
			}
		}

		// messageStart.usage.inputTokens
		if ms, ok := data["messageStart"].(map[string]any); ok {
			if usage, ok := ms["usage"].(map[string]any); ok {
				if input := intFrom(usage, "inputTokens"); input > 0 {
					if err := handler(ConverseEvent{Type: ConverseMetadata, InputTokens: input}); err != nil {
						return err
					}
				}
			}
		}

		// contentBlockStart
		if cbs, ok := data["contentBlockStart"].(map[string]any); ok {
			idx := intFrom(cbs, "contentBlockIndex")
			start, _ := cbs["start"].(map[string]any)
			if toolUse, ok := start["toolUse"].(map[string]any); ok {
				id, _ := toolUse["toolUseId"].(string)
				name, _ := toolUse["name"].(string)
				if id == "" {
					id = "toolu_" + randomID(24)
				}
				toolBlocks[idx] = true
				if err := handler(ConverseEvent{Type: ConverseToolBlockStart, Index: idx, ID: id, Name: name}); err != nil {
					return err
				}
			} else {
				if err := handler(ConverseEvent{Type: ConverseTextBlockStart, Index: idx}); err != nil {
					return err
				}
			}
		}

		// contentBlockDelta
		if cbd, ok := data["contentBlockDelta"].(map[string]any); ok {
			idx := intFrom(cbd, "contentBlockIndex")
			delta, _ := cbd["delta"].(map[string]any)
			if delta != nil {
				if text, ok := delta["text"].(string); ok {
					if err := handler(ConverseEvent{Type: ConverseTextDelta, Index: idx, Text: text}); err != nil {
						return err
					}
				} else if rc, ok := delta["reasoningContent"].(map[string]any); ok {
					if text, ok := rc["text"].(string); ok && text != "" {
						if err := handler(ConverseEvent{Type: ConverseReasoningDelta, Index: idx, Text: text}); err != nil {
							return err
						}
					}
				} else if toolUse, ok := delta["toolUse"].(map[string]any); ok {
					var partial string
					if inp, ok := toolUse["input"].(string); ok {
						partial = inp
					} else if toolUse["input"] != nil {
						b, _ := json.Marshal(toolUse["input"])
						partial = string(b)
					}
					if err := handler(ConverseEvent{Type: ConverseToolInputDelta, Index: idx, PartialJSON: partial}); err != nil {
						return err
					}
				}
			}
		}

		// contentBlockStop
		if cbStop, ok := data["contentBlockStop"].(map[string]any); ok {
			idx := intFrom(cbStop, "contentBlockIndex")
			if toolBlocks[idx] {
				delete(toolBlocks, idx)
				if err := handler(ConverseEvent{Type: ConverseToolBlockStop, Index: idx}); err != nil {
					return err
				}
			} else {
				if err := handler(ConverseEvent{Type: ConverseTextBlockStop, Index: idx}); err != nil {
					return err
				}
			}
		}

		// messageStop
		if msgStop, ok := data["messageStop"].(map[string]any); ok {
			reason, _ := msgStop["stopReason"].(string)
			if reason == "" {
				reason = "end_turn"
			}
			if err := handler(ConverseEvent{Type: ConverseMessageStop, StopReason: reason}); err != nil {
				return err
			}
		}
	}

	return scanner.Err()
}

// normalizePythonJSON converts Python-style single-quoted JSON to standard double-quoted JSON.
// Handles escaped quotes and string boundary detection.
func normalizePythonJSON(s string) string {
	if !strings.ContainsRune(s, '\'') {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s))
	inStr := false
	quoteChar := byte(0)

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if !inStr {
			if ch == '\'' {
				inStr = true
				quoteChar = '\''
				sb.WriteByte('"')
			} else if ch == '"' {
				inStr = true
				quoteChar = '"'
				sb.WriteByte('"')
			} else {
				sb.WriteByte(ch)
			}
		} else {
			if ch == '\\' && i+1 < len(s) {
				next := s[i+1]
				if quoteChar == '\'' && next == '\'' {
					sb.WriteByte('\'')
					i++
				} else if quoteChar == '\'' && next == '"' {
					sb.WriteString("\\\"")
					i++
				} else {
					sb.WriteByte(ch)
					sb.WriteByte(next)
					i++
				}
			} else if ch == quoteChar {
				inStr = false
				quoteChar = 0
				sb.WriteByte('"')
			} else if ch == '"' && quoteChar == '\'' {
				sb.WriteString("\\\"")
			} else {
				sb.WriteByte(ch)
			}
		}
	}
	return sb.String()
}

func intFrom(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

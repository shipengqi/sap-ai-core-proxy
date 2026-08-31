// Package transform provides shared request/response transformation helpers
// for SAP AI Core Bedrock-compatible (Anthropic) inference endpoints.
package transform

import (
	"encoding/json"
	"log/slog"
	"strings"
)

// BedrockAllowedFields is the set of fields forwarded to SAP AI Core Bedrock endpoints.
var BedrockAllowedFields = map[string]bool{
	"anthropic_version": true,
	"messages":          true,
	"system":            true,
	"max_tokens":        true,
	"top_p":             true,
	"top_k":             true,
	"stop_sequences":    true,
	"tools":             true,
	"tool_choice":       true,
	"metadata":          true,
	"thinking":          true,
}

// PromoteSystemMessages moves any {"role":"system"} entries from messages[]
// to the top-level "system" field (Bedrock/SAP AI Core requirement).
func PromoteSystemMessages(m map[string]json.RawMessage) map[string]json.RawMessage {
	msgsRaw, ok := m["messages"]
	if !ok {
		return m
	}

	type message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}

	var msgs []message
	if err := json.Unmarshal(msgsRaw, &msgs); err != nil {
		return m
	}

	var systemParts []string
	var remaining []message
	for _, msg := range msgs {
		if msg.Role == "system" {
			var s string
			if err := json.Unmarshal(msg.Content, &s); err == nil {
				systemParts = append(systemParts, s)
			}
		} else {
			remaining = append(remaining, msg)
		}
	}

	if len(systemParts) == 0 {
		return m
	}

	combined := ""
	if existing, ok := m["system"]; ok {
		var s string
		if err := json.Unmarshal(existing, &s); err == nil && s != "" {
			combined = s + "\n"
		}
	}
	for i, p := range systemParts {
		if i > 0 {
			combined += "\n"
		}
		combined += p
	}

	sysJSON, _ := json.Marshal(combined)
	m["system"] = sysJSON
	newMsgs, _ := json.Marshal(remaining)
	m["messages"] = newMsgs
	return m
}

// StripCacheControl removes the cache_control field from system content blocks
// and from message content arrays. SAP AI Core Bedrock does not support it.
func StripCacheControl(m map[string]json.RawMessage) map[string]json.RawMessage {
	if sys, ok := m["system"]; ok {
		if stripped, changed := removeFieldFromBlocks(sys); changed {
			m["system"] = stripped
		}
	}

	if msgsRaw, ok := m["messages"]; ok {
		var msgs []json.RawMessage
		if err := json.Unmarshal(msgsRaw, &msgs); err == nil {
			changed := false
			for i, msg := range msgs {
				var obj map[string]json.RawMessage
				if err := json.Unmarshal(msg, &obj); err != nil {
					continue
				}
				if content, ok := obj["content"]; ok {
					if stripped, c := removeFieldFromBlocks(content); c {
						obj["content"] = stripped
						b, _ := json.Marshal(obj)
						msgs[i] = b
						changed = true
					}
				}
			}
			if changed {
				b, _ := json.Marshal(msgs)
				m["messages"] = b
			}
		}
	}
	return m
}

// FlattenSystem converts a top-level system array-of-content-blocks into a plain string.
// The Anthropic SDK sends system as an array when using prompt caching; Bedrock requires a
// string for plain text, but supports the array form when cache_control blocks are present.
// Blocks that carry cache_control are left as-is so SAP Bedrock can apply prompt caching.
func FlattenSystem(m map[string]json.RawMessage) map[string]json.RawMessage {
	sys, ok := m["system"]
	if !ok {
		return m
	}
	var s string
	if json.Unmarshal(sys, &s) == nil {
		return m // already a plain string — nothing to do
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(sys, &blocks); err != nil {
		return m
	}
	// If any block carries cache_control, keep the array so SAP Bedrock can honour it.
	for _, b := range blocks {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(b, &obj); err == nil {
			if _, hasCacheControl := obj["cache_control"]; hasCacheControl {
				return m
			}
		}
	}
	// No cache_control — flatten to string (Bedrock plain-text requirement).
	var typed []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(sys, &typed); err != nil {
		return m
	}
	var parts []string
	for _, b := range typed {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	if len(parts) == 0 {
		return m
	}
	joined, _ := json.Marshal(strings.Join(parts, "\n"))
	m["system"] = joined
	return m
}

// ConvertImagePartsToAnthropic rewrites OpenAI image_url parts (base64 data URIs)
// inside messages[] into Anthropic-native image blocks that Bedrock understands.
// Remote URLs are not supported by SAP AI Core Bedrock and are dropped with a warning.
func ConvertImagePartsToAnthropic(m map[string]json.RawMessage) map[string]json.RawMessage {
	msgsRaw, ok := m["messages"]
	if !ok {
		return m
	}

	var msgs []json.RawMessage
	if err := json.Unmarshal(msgsRaw, &msgs); err != nil {
		return m
	}

	changed := false
	for i, msg := range msgs {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(msg, &obj); err != nil {
			continue
		}
		content, ok := obj["content"]
		if !ok {
			continue
		}
		// Only multipart content arrays need conversion; plain strings are text-only.
		var parts []json.RawMessage
		if err := json.Unmarshal(content, &parts); err != nil {
			continue
		}

		newParts := make([]json.RawMessage, 0, len(parts))
		partChanged := false
		for _, part := range parts {
			var p map[string]json.RawMessage
			if err := json.Unmarshal(part, &p); err != nil {
				newParts = append(newParts, part)
				continue
			}
			var partType string
			if t, ok := p["type"]; ok {
				_ = json.Unmarshal(t, &partType)
			}
			if partType != "image_url" {
				newParts = append(newParts, part)
				continue
			}

			var imageURLObj map[string]string
			if iu, ok := p["image_url"]; ok {
				_ = json.Unmarshal(iu, &imageURLObj)
			}
			url := imageURLObj["url"]
			if !strings.HasPrefix(url, "data:") {
				slog.Warn("skipping non-base64 image_url: remote URLs are not supported by SAP AI Core Bedrock", "url_prefix", url[:min(len(url), 30)])
				partChanged = true
				continue
			}

			mimeType := "image/jpeg"
			b64data := url
			if idx := strings.Index(url, ";base64,"); idx >= 0 {
				mimeType = url[5:idx]
				b64data = url[idx+8:]
			}

			anthropicPart, _ := json.Marshal(map[string]interface{}{
				"type": "image",
				"source": map[string]string{
					"type":       "base64",
					"media_type": mimeType,
					"data":       b64data,
				},
			})
			newParts = append(newParts, anthropicPart)
			partChanged = true
		}

		if partChanged {
			newContent, _ := json.Marshal(newParts)
			obj["content"] = newContent
			newMsg, _ := json.Marshal(obj)
			msgs[i] = newMsg
			changed = true
		}
	}

	if changed {
		newMsgs, _ := json.Marshal(msgs)
		m["messages"] = newMsgs
	}
	return m
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func removeFieldFromBlocks(raw json.RawMessage) (json.RawMessage, bool) {
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return raw, false
	}
	changed := false
	for i, block := range blocks {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(block, &obj); err != nil {
			continue
		}
		if _, ok := obj["cache_control"]; ok {
			delete(obj, "cache_control")
			b, _ := json.Marshal(obj)
			blocks[i] = b
			changed = true
		}
	}
	if !changed {
		return raw, false
	}
	b, _ := json.Marshal(blocks)
	return b, true
}

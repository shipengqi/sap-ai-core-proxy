// Package transform provides shared request/response transformation helpers
// for SAP AI Core Bedrock-compatible (Anthropic) inference endpoints.
package transform

import (
	"encoding/json"
	"strings"
)

// BedrockAllowedFields is the set of fields forwarded to SAP AI Core Bedrock endpoints.
var BedrockAllowedFields = map[string]bool{
	"anthropic_version": true,
	"messages":          true,
	"system":            true,
	"max_tokens":        true,
	"temperature":       true,
	"top_p":             true,
	"top_k":             true,
	"stop_sequences":    true,
	"tools":             true,
	"tool_choice":       true,
	"metadata":          true,
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
// The Anthropic SDK sends system as an array when using prompt caching; Bedrock requires a string.
func FlattenSystem(m map[string]json.RawMessage) map[string]json.RawMessage {
	sys, ok := m["system"]
	if !ok {
		return m
	}
	var s string
	if json.Unmarshal(sys, &s) == nil {
		return m
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(sys, &blocks); err != nil {
		return m
	}
	var parts []string
	for _, b := range blocks {
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

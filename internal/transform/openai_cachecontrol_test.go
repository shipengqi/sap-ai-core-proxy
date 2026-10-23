package transform

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAnthropicToOpenAIBodyDropsCacheControl pins the two failures that
// cache_control caused on the OpenAI-compatible path.
//
// Claude Code marks the system block of every request, so a non-Claude model on
// the native Anthropic surface hit both of them on the normal path, not on an
// edge case:
//
//  1. FlattenSystem keeps the array form when a block carries cache_control, so
//     prependSystemMessage could not unmarshal it into a string and returned the
//     messages untouched — the system prompt vanished from the upstream request.
//  2. A text-only content array has no Anthropic-only block to translate, so the
//     message was forwarded verbatim and the marker leaked into the body.
//
// Both are silent: the request still succeeds, it just runs without its
// instructions and with a field the endpoint does not define.
func TestAnthropicToOpenAIBodyDropsCacheControl(t *testing.T) {
	const marker = "SYSTEM-PROMPT-MARKER"

	build := func(system, messages string) string {
		raw := map[string]json.RawMessage{
			"system":     json.RawMessage(system),
			"messages":   json.RawMessage(messages),
			"max_tokens": json.RawMessage(`10`),
		}
		return string(AnthropicToOpenAIBody(raw, false))
	}

	tests := []struct {
		name     string
		system   string
		messages string
	}{
		{
			name:     "marked system and marked text block",
			system:   `[{"type":"text","text":"` + marker + `","cache_control":{"type":"ephemeral"}}]`,
			messages: `[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}]`,
		},
		{
			name:     "unmarked system, marked text block",
			system:   `[{"type":"text","text":"` + marker + `"}]`,
			messages: `[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}]`,
		},
		{
			name:     "marked system, plain string content",
			system:   `[{"type":"text","text":"` + marker + `","cache_control":{"type":"ephemeral"}}]`,
			messages: `[{"role":"user","content":"hello"}]`,
		},
		{
			name:     "system as plain string",
			system:   `"` + marker + `"`,
			messages: `[{"role":"user","content":"hello"}]`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := build(tc.system, tc.messages)

			if strings.Contains(out, "cache_control") {
				t.Errorf("cache_control leaked into the OpenAI body: %s", out)
			}
			if !strings.Contains(out, marker) {
				t.Fatalf("system prompt missing from the OpenAI body: %s", out)
			}

			var body struct {
				Messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal([]byte(out), &body); err != nil {
				t.Fatalf("decode outgoing body: %v", err)
			}
			if len(body.Messages) == 0 || body.Messages[0].Role != "system" {
				t.Fatalf("first outgoing message is not the system message: %s", out)
			}
			if !strings.Contains(string(body.Messages[0].Content), marker) {
				t.Errorf("system message does not carry the prompt: %s", out)
			}
		})
	}
}

// TestAnthropicToOpenAIBodyKeepsCacheControlOnBedrockPath is the guard in the
// other direction: the fix above must not reach the Bedrock path, where
// cache_control is honoured and stripping it would silently disable prompt
// caching.
func TestAnthropicToOpenAIBodyKeepsCacheControlOnBedrockPath(t *testing.T) {
	marked := `[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}]`
	raw := map[string]json.RawMessage{
		"system":   json.RawMessage(marked),
		"messages": json.RawMessage(`[{"role":"user","content":"hi"}]`),
	}

	// The Bedrock body builder must leave the array untouched so the breakpoint
	// survives.
	filtered := map[string]json.RawMessage{"system": raw["system"]}
	filtered = FlattenSystem(filtered)
	if got := string(filtered["system"]); got != marked {
		t.Errorf("FlattenSystem rewrote a marked system array: %s", got)
	}

	// And the OpenAI path must drop it.
	out := string(AnthropicToOpenAIBody(map[string]json.RawMessage{
		"system":     json.RawMessage(marked),
		"messages":   json.RawMessage(`[{"role":"user","content":"hi"}]`),
		"max_tokens": json.RawMessage(`10`),
	}, false))
	if strings.Contains(out, "cache_control") {
		t.Errorf("OpenAI path kept cache_control: %s", out)
	}
}

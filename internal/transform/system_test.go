package transform

import (
	"encoding/json"
	"testing"
)

// TestPromoteSystemMessagesPreservesMarkedSystem is the regression test for a
// silent system-prompt loss.
//
// The top-level system field arrives as an array of content blocks whenever the
// client uses prompt caching. The old implementation tried to read that array as
// a string, failed, started from an empty accumulator, and then assigned the
// result back over the field — so a request that both marked its system prompt
// and put a {"role":"system"} entry in messages[] reached the model with only
// the promoted fragment and no system prompt at all. The cache breakpoint went
// with it.
func TestPromoteSystemMessagesPreservesMarkedSystem(t *testing.T) {
	in := `{"system":[{"type":"text","text":"ORIGINAL-SYSTEM","cache_control":{"type":"ephemeral"}}],` +
		`"messages":[{"role":"system","content":"promoted tail"},{"role":"user","content":"hi"}]}`

	out := promote(t, in)

	var blocks []map[string]any
	if err := json.Unmarshal(out["system"], &blocks); err != nil {
		t.Fatalf("system is no longer an array: %s (%v)", out["system"], err)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 system blocks, got %d: %s", len(blocks), out["system"])
	}
	if got := blocks[0]["text"]; got != "ORIGINAL-SYSTEM" {
		t.Errorf("original system text lost: %v", got)
	}
	if _, ok := blocks[0]["cache_control"]; !ok {
		t.Error("cache_control breakpoint was dropped from the original block")
	}
	if got := blocks[1]["text"]; got != "promoted tail" {
		t.Errorf("promoted text = %v, want \"promoted tail\"", got)
	}

	var msgs []map[string]any
	if err := json.Unmarshal(out["messages"], &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0]["role"] != "user" {
		t.Errorf("system message not removed from messages: %s", out["messages"])
	}
}

// TestPromoteSystemMessagesLeavesCleanRequestAlone pins the shape Claude Code
// actually sends: a marked system array and no role:"system" in messages. Here
// the function must be a no-op down to the byte, because any change to the
// system prefix would invalidate the cache on the next turn.
func TestPromoteSystemMessagesLeavesCleanRequestAlone(t *testing.T) {
	system := `[{"type":"text","text":"ORIGINAL-SYSTEM","cache_control":{"type":"ephemeral"}}]`
	in := `{"system":` + system + `,"messages":[{"role":"user","content":"hi"}]}`

	out := promote(t, in)

	if got := string(out["system"]); got != system {
		t.Errorf("system was rewritten:\n got %s\nwant %s", got, system)
	}
	if got := string(out["messages"]); got != `[{"role":"user","content":"hi"}]` {
		t.Errorf("messages were rewritten: %s", got)
	}
}

func TestPromoteSystemMessagesCases(t *testing.T) {
	tests := []struct {
		name         string
		in           string
		wantSystem   string // exact JSON, "" means the field must be absent
		wantMessages string
	}{
		{
			name:         "no system in messages is untouched",
			in:           `{"system":"S","messages":[{"role":"user","content":"hi"}]}`,
			wantSystem:   `"S"`,
			wantMessages: `[{"role":"user","content":"hi"}]`,
		},
		{
			name:         "string system is extended",
			in:           `{"system":"S","messages":[{"role":"system","content":"P"},{"role":"user","content":"hi"}]}`,
			wantSystem:   `"S\nP"`,
			wantMessages: `[{"role":"user","content":"hi"}]`,
		},
		{
			name:         "absent system takes the promoted text",
			in:           `{"messages":[{"role":"system","content":"P"},{"role":"user","content":"hi"}]}`,
			wantSystem:   `"P"`,
			wantMessages: `[{"role":"user","content":"hi"}]`,
		},
		{
			name:         "several system messages join in order",
			in:           `{"messages":[{"role":"system","content":"A"},{"role":"system","content":"B"},{"role":"user","content":"hi"}]}`,
			wantSystem:   `"A\nB"`,
			wantMessages: `[{"role":"user","content":"hi"}]`,
		},
		{
			name:         "block-array system content is promoted",
			in:           `{"messages":[{"role":"system","content":[{"type":"text","text":"A"},{"type":"text","text":"B"}]},{"role":"user","content":"hi"}]}`,
			wantSystem:   `"A\nB"`,
			wantMessages: `[{"role":"user","content":"hi"}]`,
		},
		{
			name:         "developer string is promoted",
			in:           `{"system":"S","messages":[{"role":"developer","content":"D"},{"role":"user","content":"hi"}]}`,
			wantSystem:   `"S\nD"`,
			wantMessages: `[{"role":"user","content":"hi"}]`,
		},
		{
			name:         "developer input_text is appended after cache breakpoint",
			in:           `{"system":[{"type":"text","text":"S","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"developer","content":[{"type":"input_text","text":"D"},{"type":"text","text":"E"}]},{"role":"user","content":"hi"}]}`,
			wantSystem:   `[{"type":"text","text":"S","cache_control":{"type":"ephemeral"}},{"type":"text","text":"D"},{"type":"text","text":"E"}]`,
			wantMessages: `[{"role":"user","content":"hi"}]`,
		},
		{
			name:         "system input_text is promoted",
			in:           `{"messages":[{"role":"system","content":[{"type":"input_text","text":"A"}]},{"role":"user","content":"hi"}]}`,
			wantSystem:   `"A"`,
			wantMessages: `[{"role":"user","content":"hi"}]`,
		},
		{
			name:         "unreadable developer is preserved alongside promoted system",
			in:           `{"system":"S","messages":[{"role":"developer","content":{"weird":true}},{"role":"system","content":"P"},{"role":"user","content":"hi"}]}`,
			wantSystem:   `"S\nP"`,
			wantMessages: `[{"role":"developer","content":{"weird":true}},{"role":"user","content":"hi"}]`,
		},
		{
			name:         "unmergeable top level system leaves messages untouched",
			in:           `{"system":{"weird":true},"messages":[{"role":"developer","content":"D"},{"role":"user","content":"hi"}]}`,
			wantSystem:   `{"weird":true}`,
			wantMessages: `[{"role":"developer","content":"D"},{"role":"user","content":"hi"}]`,
		},
		{
			name:         "unmarked system array is extended with promoted text",
			in:           `{"system":[{"type":"text","text":"S"}],"messages":[{"role":"system","content":"P"},{"role":"user","content":"hi"}]}`,
			wantSystem:   `[{"type":"text","text":"S"},{"type":"text","text":"P"}]`,
			wantMessages: `[{"role":"user","content":"hi"}]`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := promote(t, tc.in)

			if tc.wantSystem == "" {
				if _, ok := out["system"]; ok {
					t.Fatalf("system should be absent, got %s", out["system"])
				}
			} else if got := string(out["system"]); got != tc.wantSystem {
				t.Errorf("system = %s, want %s", got, tc.wantSystem)
			}

			if got := string(out["messages"]); got != tc.wantMessages {
				t.Errorf("messages = %s, want %s", got, tc.wantMessages)
			}
		})
	}
}

// TestPromoteSystemMessagesKeepsUnreadableContent covers the other way this
// function could lose text: a system message whose content is neither a string
// nor text blocks used to be dropped from messages[] without being promoted.
func TestPromoteSystemMessagesKeepsUnreadableContent(t *testing.T) {
	in := `{"system":"S","messages":[{"role":"system","content":{"weird":true}},{"role":"system","content":"P"},{"role":"user","content":"hi"}]}`

	out := promote(t, in)

	var msgs []map[string]any
	if err := json.Unmarshal(out["messages"], &msgs); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range msgs {
		if m["role"] == "system" {
			found = true
		}
	}
	if !found {
		t.Errorf("unreadable system message was dropped: %s", out["messages"])
	}
	if got := string(out["system"]); got != `"S\nP"` {
		t.Errorf("system = %s, want %s", got, `"S\nP"`)
	}
}

// promote runs PromoteSystemMessages over a JSON request and returns the result.
func promote(t *testing.T, in string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(in), &m); err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	return PromoteSystemMessages(m)
}

// TestSystemPromptSurvivesBedrockChain runs the exact transform sequence the
// Bedrock handler applies, in the same order. Testing PromoteSystemMessages in
// isolation would miss an interaction with FlattenSystem, which keeps the array
// form only when a block carries cache_control — the two functions disagree
// about what "the system prompt" is, and that disagreement is where the text
// used to disappear.
func TestSystemPromptSurvivesBedrockChain(t *testing.T) {
	chain := func(t *testing.T, in string) map[string]json.RawMessage {
		t.Helper()
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(in), &m); err != nil {
			t.Fatalf("bad fixture: %v", err)
		}
		m = PromoteSystemMessages(m)
		m = ConvertImagePartsToAnthropic(m)
		m = FlattenSystem(m)
		return m
	}

	tests := []struct {
		name       string
		in         string
		wantSystem string
	}{
		{
			name:       "plain string system",
			in:         `{"system":"KEEP-ME","messages":[{"role":"user","content":"hi"}]}`,
			wantSystem: `"KEEP-ME"`,
		},
		{
			name:       "marked array, the shape Claude Code sends",
			in:         `{"system":[{"type":"text","text":"KEEP-ME","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`,
			wantSystem: `[{"type":"text","text":"KEEP-ME","cache_control":{"type":"ephemeral"}}]`,
		},
		{
			name:       "marked array plus a promoted system message",
			in:         `{"system":[{"type":"text","text":"KEEP-ME","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"system","content":"tail"},{"role":"user","content":"hi"}]}`,
			wantSystem: `[{"type":"text","text":"KEEP-ME","cache_control":{"type":"ephemeral"}},{"type":"text","text":"tail"}]`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := chain(t, tc.in)
			if got := string(out["system"]); got != tc.wantSystem {
				t.Errorf("system = %s, want %s", got, tc.wantSystem)
			}
			if !json.Valid(out["messages"]) {
				t.Errorf("messages is not valid JSON: %s", out["messages"])
			}
		})
	}
}

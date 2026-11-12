package compress

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The OpenAI wire shape has an explicit trust marker (role=tool / type=
// function_call_output) that the Anthropic shape lacks. These tests pin both
// halves of that claim: tool output is compressed, and a user message carrying
// the exact same bytes is never touched.

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return b
}

func openAIMessages(t *testing.T, raw json.RawMessage) []map[string]json.RawMessage {
	t.Helper()
	var msgs []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &msgs); err != nil {
		t.Fatalf("decode messages: %v\n%s", err, raw)
	}
	return msgs
}

func TestCompressOpenAIRequest_ChatToolString(t *testing.T) {
	in := map[string]json.RawMessage{
		"model":       mustJSON(t, "gpt-5.6"),
		"temperature": json.RawMessage(`0.2`),
		"messages": mustJSON(t, []any{
			map[string]any{"role": "user", "content": "why is the build failing?"},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "content": fixturePytest},
		}),
	}

	out, rep := CompressOpenAIRequest(in)
	if rep.BlocksCompressed != 1 || rep.BlocksConsidered != 1 {
		t.Fatalf("report = %+v, want one block considered and compressed", rep)
	}
	if rep.OriginalBytes <= rep.CompressedBytes {
		t.Fatalf("bytes %d -> %d did not shrink", rep.OriginalBytes, rep.CompressedBytes)
	}
	if !bytes.Contains(out["messages"], []byte("lines omitted")) {
		t.Fatalf("compressed body has no omission marker:\n%s", out["messages"])
	}
	if !bytes.Contains(out["messages"], []byte("AssertionError: assert 404 == 200")) {
		t.Fatalf("an error line was lost:\n%s", out["messages"])
	}

	msgs := openAIMessages(t, out["messages"])
	if string(msgs[0]["content"]) != `"why is the build failing?"` {
		t.Fatalf("user message was rewritten: %s", msgs[0]["content"])
	}
	if string(msgs[1]["tool_call_id"]) != `"call_1"` {
		t.Fatalf("tool_call_id was lost: %s", msgs[1]["tool_call_id"])
	}
	var tool string
	if err := json.Unmarshal(msgs[1]["content"], &tool); err != nil {
		t.Fatalf("tool content is not a string: %v", err)
	}
	if len(tool) >= len(fixturePytest) {
		t.Fatalf("tool content did not shrink: %d bytes of %d", len(tool), len(fixturePytest))
	}

	// Every key outside the changed array stays byte-identical.
	if !bytes.Equal(out["model"], in["model"]) || !bytes.Equal(out["temperature"], in["temperature"]) {
		t.Fatalf("unrelated request fields were rewritten")
	}
	if _, changed := out["stream"]; changed {
		t.Fatalf("compressor injected a field")
	}
}

func TestCompressOpenAIRequest_UserMessageIsNeverCompressed(t *testing.T) {
	in := map[string]json.RawMessage{
		"messages": mustJSON(t, []any{
			map[string]any{"role": "user", "content": fixturePytest},
			map[string]any{"role": "assistant", "content": fixturePytest},
			map[string]any{"role": "system", "content": fixturePytest},
		}),
	}
	out, rep := CompressOpenAIRequest(in)
	if rep.BlocksCompressed != 0 {
		t.Fatalf("compressed %d non-tool blocks", rep.BlocksCompressed)
	}
	if rep.Skipped != skipNoToolOutput {
		t.Fatalf("Skipped = %q, want %q", rep.Skipped, skipNoToolOutput)
	}
	if !bytes.Equal(out["messages"], in["messages"]) {
		t.Fatalf("non-tool messages were rewritten")
	}
}

func TestCompressOpenAIRequest_ChatToolContentParts(t *testing.T) {
	in := map[string]json.RawMessage{
		"messages": mustJSON(t, []any{
			map[string]any{
				"role":         "tool",
				"tool_call_id": "call_2",
				"content": []any{
					map[string]any{"type": "text", "text": "short header"},
					map[string]any{"type": "text", "text": fixturePytest},
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.invalid/x.png"}},
				},
			},
		}),
	}
	out, rep := CompressOpenAIRequest(in)
	if rep.BlocksCompressed != 1 {
		t.Fatalf("report = %+v, want the big text part compressed", rep)
	}
	msgs := openAIMessages(t, out["messages"])
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(msgs[0]["content"], &parts); err != nil {
		t.Fatalf("decode parts: %v", err)
	}
	if len(parts) != 3 {
		t.Fatalf("parts = %d, want 3", len(parts))
	}
	if string(parts[0]["text"]) != `"short header"` {
		t.Fatalf("small text part changed: %s", parts[0]["text"])
	}
	if !bytes.Contains(parts[2]["image_url"], []byte("example.invalid")) {
		t.Fatalf("non-text part was lost: %s", parts[2]["image_url"])
	}
	var big string
	if err := json.Unmarshal(parts[1]["text"], &big); err != nil {
		t.Fatalf("big text part is not a string: %v", err)
	}
	if len(big) >= len(fixturePytest) {
		t.Fatalf("big text part did not shrink")
	}
}

func TestCompressOpenAIRequest_LegacyFunctionRole(t *testing.T) {
	in := map[string]json.RawMessage{
		"messages": mustJSON(t, []any{
			map[string]any{"role": "function", "name": "Bash", "content": fixturePytest},
		}),
	}
	out, rep := CompressOpenAIRequest(in)
	if rep.BlocksCompressed != 1 {
		t.Fatalf("legacy function role was not compressed: %+v", rep)
	}
	if !bytes.Contains(out["messages"], []byte("lines omitted")) {
		t.Fatalf("no compression marker: %s", out["messages"])
	}
}

func TestCompressOpenAIRequest_ResponsesFunctionCallOutput(t *testing.T) {
	in := map[string]json.RawMessage{
		"model":      mustJSON(t, "gpt-5.6"),
		"stream":     json.RawMessage(`true`),
		"store":      json.RawMessage(`false`),
		"input": mustJSON(t, []any{
			map[string]any{
				"type": "message", "role": "user",
				"content": []any{map[string]any{"type": "input_text", "text": "run the tests"}},
			},
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": fixturePytest},
		}),
	}
	out, rep := CompressOpenAIRequest(in)
	if rep.BlocksCompressed != 1 {
		t.Fatalf("report = %+v, want one function_call_output compressed", rep)
	}
	if !bytes.Contains(out["input"], []byte("lines omitted")) {
		t.Fatalf("no compression marker: %s", out["input"])
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(out["input"], &items); err != nil {
		t.Fatalf("decode input: %v", err)
	}
	if len(items) != 2 || string(items[1]["call_id"]) != `"call_1"` {
		t.Fatalf("input shape or call_id changed: %s", out["input"])
	}
	if !bytes.Contains(items[0]["content"], []byte("run the tests")) {
		t.Fatalf("the user input item was rewritten: %s", items[0]["content"])
	}
	var output string
	if err := json.Unmarshal(items[1]["output"], &output); err != nil {
		t.Fatalf("output is not a string: %v", err)
	}
	if len(output) >= len(fixturePytest) {
		t.Fatalf("output did not shrink")
	}
	if !bytes.Equal(out["stream"], in["stream"]) || !bytes.Equal(out["store"], in["store"]) {
		t.Fatalf("unrelated Responses fields were rewritten")
	}
}

func TestCompressOpenAIRequest_ResponsesOutputParts(t *testing.T) {
	in := map[string]json.RawMessage{
		"input": mustJSON(t, []any{
			map[string]any{
				"type":   "function_call_output",
				"call_id": "call_9",
				"output": []any{
					map[string]any{"type": "input_text", "text": fixturePytest},
					map[string]any{"type": "input_image", "image_url": "https://example.invalid/y.png"},
				},
			},
		}),
	}
	out, rep := CompressOpenAIRequest(in)
	if rep.BlocksCompressed != 1 {
		t.Fatalf("report = %+v, want one text part compressed", rep)
	}
	if !bytes.Contains(out["input"], []byte("lines omitted")) {
		t.Fatalf("no compression marker: %s", out["input"])
	}
	var items []struct {
		Output []map[string]json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(out["input"], &items); err != nil {
		t.Fatalf("decode input: %v", err)
	}
	if len(items[0].Output) != 2 || !bytes.Contains(items[0].Output[1]["image_url"], []byte("example.invalid")) {
		t.Fatalf("non-text output part was lost: %s", out["input"])
	}
}

func TestCompressOpenAIRequest_NonLogToolOutputUntouched(t *testing.T) {
	// A short answer and a source file are both tool output, but neither is a
	// log. The first must fail the size gate, the second the detection gate.
	source := strings.Repeat("const ERROR = 1 // an error constant in source code\n", 40)
	in := map[string]json.RawMessage{
		"messages": mustJSON(t, []any{
			map[string]any{"role": "tool", "tool_call_id": "c1", "content": "12:00"},
			map[string]any{"role": "tool", "tool_call_id": "c2", "content": source},
		}),
	}
	out, rep := CompressOpenAIRequest(in)
	if rep.BlocksCompressed != 0 || rep.BlocksConsidered != 2 {
		t.Fatalf("report = %+v, want two considered and none compressed", rep)
	}
	if rep.Skipped != skipTooSmall && rep.Skipped != skipNotLog {
		t.Fatalf("Skipped = %q, want a detection reason", rep.Skipped)
	}
	if !bytes.Equal(out["messages"], in["messages"]) {
		t.Fatalf("non-log tool output was rewritten")
	}
}

func TestCompressOpenAIRequest_PrefixStableAcrossTurns(t *testing.T) {
	history := []any{
		map[string]any{"role": "user", "content": "run the tests"},
		map[string]any{"role": "tool", "tool_call_id": "call_1", "content": fixturePytest},
	}
	turn1 := map[string]json.RawMessage{"messages": mustJSON(t, history)}
	turn2 := map[string]json.RawMessage{
		"messages": mustJSON(t, append(append([]any{}, history...),
			map[string]any{"role": "assistant", "content": "looking at the failure"},
			map[string]any{"role": "tool", "tool_call_id": "call_2", "content": strings.ReplaceAll(fixturePytest, "test_api.py", "test_db.py")},
		)),
	}
	out1, rep1 := CompressOpenAIRequest(turn1)
	out2, rep2 := CompressOpenAIRequest(turn2)
	if rep1.BlocksCompressed != 1 || rep2.BlocksCompressed != 2 {
		t.Fatalf("reports = %+v / %+v, want 1 and 2 compressed blocks", rep1, rep2)
	}
	var a, b []json.RawMessage
	if err := json.Unmarshal(out1["messages"], &a); err != nil {
		t.Fatalf("decode turn 1: %v", err)
	}
	if err := json.Unmarshal(out2["messages"], &b); err != nil {
		t.Fatalf("decode turn 2: %v", err)
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			t.Fatalf("message %d of the shared prefix differs between turns:\n%s\n%s", i, a[i], b[i])
		}
	}
}

func TestCompressOpenAIRequest_DeterministicWithColdCache(t *testing.T) {
	in := map[string]json.RawMessage{
		"messages": mustJSON(t, []any{
			map[string]any{"role": "tool", "tool_call_id": "c1", "content": fixturePytest},
		}),
	}
	resetTextCache()
	cold, coldRep := CompressOpenAIRequest(in)
	warm, warmRep := CompressOpenAIRequest(in)
	resetTextCache()
	cleared, clearedRep := CompressOpenAIRequest(in)
	if !bytes.Equal(cold["messages"], warm["messages"]) || !bytes.Equal(cold["messages"], cleared["messages"]) {
		t.Fatalf("cache state changed the output")
	}
	if coldRep.BlocksCompressed != warmRep.BlocksCompressed || coldRep.BlocksCompressed != clearedRep.BlocksCompressed {
		t.Fatalf("cache state changed the report: %+v / %+v / %+v", coldRep, warmRep, clearedRep)
	}
}

func TestCompressOpenAIRequest_StrategyDisabled(t *testing.T) {
	Configure(false, []string{StrategyLog})
	defer Configure(false, nil)
	in := map[string]json.RawMessage{
		"messages": mustJSON(t, []any{
			map[string]any{"role": "tool", "tool_call_id": "c1", "content": fixturePytest},
		}),
	}
	out, rep := CompressOpenAIRequest(in)
	if rep.BlocksCompressed != 0 || rep.Skipped != skipStrategyOff {
		t.Fatalf("report = %+v, want strategy_disabled with no compression", rep)
	}
	if !bytes.Equal(out["messages"], in["messages"]) {
		t.Fatalf("request changed while the strategy was off")
	}
}

func TestCompressOpenAIRequest_MalformedInputIsSafe(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]json.RawMessage
	}{
		{"nil map", nil},
		{"empty map", map[string]json.RawMessage{}},
		{"messages is a string", map[string]json.RawMessage{"messages": json.RawMessage(`"oops"`)}},
		{"messages is null", map[string]json.RawMessage{"messages": json.RawMessage(`null`)}},
		{"array of scalars", map[string]json.RawMessage{"messages": json.RawMessage(`[1,"x",null]`)}},
		{"tool content is a number", map[string]json.RawMessage{"messages": json.RawMessage(`[{"role":"tool","content":42}]`)}},
		{"tool content is null", map[string]json.RawMessage{"messages": json.RawMessage(`[{"role":"tool","content":null}]`)}},
		{"input is an object", map[string]json.RawMessage{"input": json.RawMessage(`{"type":"function_call_output"}`)}},
		{"bad json", map[string]json.RawMessage{"messages": json.RawMessage(`[{`)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			var before json.RawMessage
			if in != nil {
				before = in["messages"]
				if before == nil {
					before = in["input"]
				}
			}
			out, rep := CompressOpenAIRequest(in)
			if rep.BlocksCompressed != 0 {
				t.Fatalf("compressed a malformed request: %+v", rep)
			}
			if in == nil {
				if out != nil {
					t.Fatalf("nil map became non-nil")
				}
				return
			}
			after := out["messages"]
			if after == nil {
				after = out["input"]
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("malformed request was rewritten:\n%s\n%s", before, after)
			}
		})
	}
}

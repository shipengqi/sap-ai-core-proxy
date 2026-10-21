package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/shipengqi/sap-ai-core-proxy/internal/compress"
)

// This file is the end-to-end guard for live-zone compression: it drives real
// requests through the real router and compares the bytes the upstream received
// with the feature off against the bytes it received with the feature on.
//
// The claim under test is narrow and absolute: the only bytes that may differ
// are inside the tool_result of the last user message. Everything else — system,
// tools, the message history — is the prompt-cache prefix, and rewriting it is
// billed at 1.25x per byte while the saving is a 0.1x read.

// compressPytestLog builds a deterministic pytest -v run: 120 tests, two
// failures, one summary. Built rather than pasted so the noise section is big
// enough to be worth compressing.
func compressPytestLog() string {
	var b strings.Builder
	b.WriteString("============================= test session starts ==============================\n")
	b.WriteString("platform linux -- Python 3.11.4, pytest-7.4.0, pluggy-1.2.0\n")
	b.WriteString("rootdir: /app\n")
	b.WriteString("collected 120 items\n\n")
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "tests/test_api.py::test_case_%03d PASSED                                  [%3d%%]\n", i, i*100/120)
	}
	b.WriteString("\n================================== FAILURES ===================================\n")
	b.WriteString("_______________________________ test_update_user ______________________________\n\n")
	b.WriteString("    def test_update_user(client):\n")
	b.WriteString("        resp = client.patch(\"/users/1\", json={\"name\": \"new\"})\n")
	b.WriteString(">       assert resp.status_code == 200\n")
	b.WriteString("E       AssertionError: assert 404 == 200\n")
	b.WriteString("E        +  where 404 = Response(client.patch('/users/1')).status_code\n\n")
	b.WriteString("tests/test_api.py:42: AssertionError\n")
	b.WriteString("=========================== short test summary info ============================\n")
	b.WriteString("FAILED tests/test_api.py::test_update_user - AssertionError: assert 404 == 200\n")
	b.WriteString("FAILED tests/test_api.py::test_delete_user - AssertionError: assert 500 == 204\n")
	b.WriteString("========================= 2 failed, 118 passed in 4.53s ========================\n")
	return b.String()
}

// compressClaudeCodeRequest is a request in the shape Claude Code sends: system
// blocks, tool definitions, several turns, and one large tool result at the end.
func compressClaudeCodeRequest(t *testing.T, log string) string {
	t.Helper()
	req := map[string]any{
		"model":      "claude-3-5-sonnet-latest",
		"max_tokens": 4096,
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude Code, an assistant for software engineering."},
			map[string]any{"type": "text", "text": "Always run the tests before you answer."},
		},
		"tools": []any{
			map[string]any{
				"name":        "Bash",
				"description": "Execute a shell command",
				"input_schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{"type": "string"},
					},
					"required": []any{"command"},
				},
			},
			map[string]any{
				"name":        "Read",
				"description": "Read a file",
				"input_schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file_path": map[string]any{"type": "string"},
					},
					"required": []any{"file_path"},
				},
			},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": "the build is failing, please fix it"},
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "text", "text": "Let me look at the repo first."},
					map[string]any{"type": "tool_use", "id": "toolu_01", "name": "Read",
						"input": map[string]any{"file_path": "/app/tests/test_api.py"}},
				},
			},
			// An earlier turn whose tool result is also big. It sits in the
			// cache prefix and must not be touched.
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": "toolu_01",
						"content": strings.ReplaceAll(compressPytestLog(), "test_api.py", "test_db.py")},
				},
			},
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "text", "text": "Now let me run the tests."},
					map[string]any{"type": "tool_use", "id": "toolu_02", "name": "Bash",
						"input": map[string]any{"command": "pytest -v"}},
				},
			},
			// The live zone.
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "here is the pytest output"},
					map[string]any{"type": "tool_result", "tool_use_id": "toolu_02", "content": log},
				},
			},
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return string(body)
}

// TestCompress_OffMatchesBaseline pins the default: with the switch off, the
// upstream receives exactly what it received before this feature existed.
func TestCompress_OffMatchesBaseline(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	req := compressClaudeCodeRequest(t, compressPytestLog())

	compress.Configure(false, nil)
	for i := 0; i < 2; i++ {
		if status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", req); status != 200 {
			t.Fatalf("request %d: status %d: %s", i, status, body)
		}
	}

	bodies := up.bodiesSnapshot()
	if len(bodies) != 2 {
		t.Fatalf("upstream received %d requests, want 2", len(bodies))
	}
	if !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("the same request produced different upstream bytes:\n%s\n%s", bodies[0], bodies[1])
	}
	if bytes.Contains(bodies[0], []byte("lines omitted")) {
		t.Fatalf("the baseline body contains a compression marker:\n%s", bodies[0])
	}
}

// TestCompress_OnlyTheLastToolResultChanges is the central guarantee: turn the
// feature on and compare the upstream bytes field by field.
func TestCompress_OnlyTheLastToolResultChanges(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	req := compressClaudeCodeRequest(t, compressPytestLog())

	compress.Configure(false, nil)
	if status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", req); status != 200 {
		t.Fatalf("baseline request: status %d: %s", status, body)
	}
	compress.Configure(true, nil)
	defer compress.Configure(false, nil)
	if status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", req); status != 200 {
		t.Fatalf("compressed request: status %d: %s", status, body)
	}

	bodies := up.bodiesSnapshot()
	if len(bodies) != 2 {
		t.Fatalf("upstream received %d requests, want 2", len(bodies))
	}
	before, after := bodies[0], bodies[1]
	if bytes.Equal(before, after) {
		t.Fatal("compression changed nothing; the fixture is not doing its job")
	}

	var beforeDoc, afterDoc map[string]json.RawMessage
	if err := json.Unmarshal(before, &beforeDoc); err != nil {
		t.Fatalf("decode baseline: %v", err)
	}
	if err := json.Unmarshal(after, &afterDoc); err != nil {
		t.Fatalf("decode compressed: %v", err)
	}
	if len(beforeDoc) != len(afterDoc) {
		t.Fatalf("top-level field count %d, want %d", len(afterDoc), len(beforeDoc))
	}
	for k, v := range beforeDoc {
		if k == "messages" {
			continue
		}
		if !bytes.Equal(v, afterDoc[k]) {
			t.Errorf("top-level field %q changed:\nbefore: %s\nafter:  %s", k, v, afterDoc[k])
		}
	}

	beforeMsgs := decodeMessages(t, beforeDoc["messages"])
	afterMsgs := decodeMessages(t, afterDoc["messages"])
	if len(beforeMsgs) != len(afterMsgs) {
		t.Fatalf("messages length %d, want %d", len(afterMsgs), len(beforeMsgs))
	}
	// The whole history: identical bytes.
	for i := 0; i < len(beforeMsgs)-1; i++ {
		if !bytes.Equal(beforeMsgs[i], afterMsgs[i]) {
			t.Errorf("messages[%d] changed:\nbefore: %s\nafter:  %s", i, beforeMsgs[i], afterMsgs[i])
		}
	}

	// The last message: every field but content is identical.
	beforeLast := decodeObject(t, beforeMsgs[len(beforeMsgs)-1])
	afterLast := decodeObject(t, afterMsgs[len(afterMsgs)-1])
	for k, v := range beforeLast {
		if k == "content" {
			continue
		}
		if !bytes.Equal(v, afterLast[k]) {
			t.Errorf("last message field %q changed: %s -> %s", k, v, afterLast[k])
		}
	}

	beforeBlocks := decodeArray(t, beforeLast["content"])
	afterBlocks := decodeArray(t, afterLast["content"])
	if len(beforeBlocks) != len(afterBlocks) {
		t.Fatalf("content blocks %d, want %d", len(afterBlocks), len(beforeBlocks))
	}
	// The text block is not a tool_result, so it must be untouched.
	if !bytes.Equal(beforeBlocks[0], afterBlocks[0]) {
		t.Errorf("the text block changed:\nbefore: %s\nafter:  %s", beforeBlocks[0], afterBlocks[0])
	}
	beforeTool := decodeObject(t, beforeBlocks[1])
	afterTool := decodeObject(t, afterBlocks[1])
	for k, v := range beforeTool {
		if k == "content" {
			continue
		}
		if !bytes.Equal(v, afterTool[k]) {
			t.Errorf("tool_result field %q changed: %s -> %s", k, v, afterTool[k])
		}
	}

	var beforeText, afterText string
	if err := json.Unmarshal(beforeTool["content"], &beforeText); err != nil {
		t.Fatalf("decode baseline text: %v", err)
	}
	if err := json.Unmarshal(afterTool["content"], &afterText); err != nil {
		t.Fatalf("decode compressed text: %v", err)
	}
	if len(afterText) >= len(beforeText) {
		t.Fatalf("tool_result %d -> %d bytes, want strictly smaller", len(beforeText), len(afterText))
	}
	for _, want := range []string{
		"FAILED tests/test_api.py::test_update_user",
		"FAILED tests/test_api.py::test_delete_user",
		"2 failed, 118 passed in 4.53s",
		"E       AssertionError: assert 404 == 200",
	} {
		if !strings.Contains(afterText, want) {
			t.Errorf("compressed tool_result lost %q:\n%s", want, afterText)
		}
	}
	t.Logf("tool_result: %d -> %d bytes (%.1fx)", len(beforeText), len(afterText),
		float64(len(beforeText))/float64(len(afterText)))
}

// TestCompress_DeterministicWhenEnabled: two identical requests with the feature
// on must send identical bytes, or the cache prefix changes every turn.
func TestCompress_DeterministicWhenEnabled(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	compress.Configure(true, nil)
	defer compress.Configure(false, nil)

	req := compressClaudeCodeRequest(t, compressPytestLog())
	for i := 0; i < 3; i++ {
		if status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", req); status != 200 {
			t.Fatalf("request %d: status %d: %s", i, status, body)
		}
	}
	bodies := up.bodiesSnapshot()
	if len(bodies) != 3 {
		t.Fatalf("upstream received %d requests, want 3", len(bodies))
	}
	for i := 1; i < len(bodies); i++ {
		if !bytes.Equal(bodies[0], bodies[i]) {
			t.Fatalf("request %d differs from request 0:\n%s\n%s", i, bodies[0], bodies[i])
		}
	}
}

// TestCompress_NoChangeWhenNothingToCompress: a tool result that is not a log
// must pass through untouched even with the switch on.
func TestCompress_NoChangeWhenNothingToCompress(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	prose := strings.Repeat("the parser reads a line and returns its upper case form. ", 40)
	req := compressClaudeCodeRequest(t, prose)

	compress.Configure(false, nil)
	if status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", req); status != 200 {
		t.Fatalf("baseline request: status %d: %s", status, body)
	}
	compress.Configure(true, nil)
	defer compress.Configure(false, nil)
	if status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", req); status != 200 {
		t.Fatalf("compressed request: status %d: %s", status, body)
	}

	bodies := up.bodiesSnapshot()
	if len(bodies) != 2 {
		t.Fatalf("upstream received %d requests, want 2", len(bodies))
	}
	if !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("a non-log tool result was modified:\nbefore: %s\nafter:  %s", bodies[0], bodies[1])
	}
}

// TestCompress_MetricsFamilies: the new families appear on the shared /metrics
// endpoint, are parseable by the existing strict parser, and report a real
// reduction in bytes.
func TestCompress_MetricsFamilies(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	compress.Configure(true, nil)
	defer compress.Configure(false, nil)

	req := compressClaudeCodeRequest(t, compressPytestLog())
	if status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", req); status != 200 {
		t.Fatalf("request: status %d: %s", status, body)
	}

	text := qaGetMetrics(t, proxy.URL)
	samples := qaParseMetrics(t, text) // fails the test if any line is malformed

	for _, name := range []string{
		"sap_proxy_compress_blocks_total",
		"sap_proxy_compress_bytes_before_total",
		"sap_proxy_compress_bytes_after_total",
		"sap_proxy_compress_skipped_total",
	} {
		if !strings.Contains(text, "# TYPE "+name+" counter") {
			t.Errorf("missing # TYPE declaration for %s\n%s", name, text)
		}
	}
	// The byte counters must say bytes, not tokens: there is no tokenizer here.
	for _, name := range []string{
		"sap_proxy_compress_bytes_before_total",
		"sap_proxy_compress_bytes_after_total",
	} {
		idx := strings.Index(text, "# HELP "+name+" ")
		if idx < 0 {
			t.Errorf("missing # HELP for %s", name)
			continue
		}
		help := text[idx : idx+strings.Index(text[idx:], "\n")]
		if !strings.Contains(help, "not tokens") {
			t.Errorf("# HELP %s must state that the unit is bytes, not tokens:\n%s", name, help)
		}
	}

	compressed := qaFindSamples(samples, "sap_proxy_compress_blocks_total",
		map[string]string{"surface": "anthropic", "result": "compressed"})
	if len(compressed) != 1 || compressed[0].value < 1 {
		t.Fatalf("blocks_total{result=compressed} = %+v, want 1 sample >= 1\n%s", compressed, text)
	}

	before := qaFindSamples(samples, "sap_proxy_compress_bytes_before_total",
		map[string]string{"surface": "anthropic"})
	after := qaFindSamples(samples, "sap_proxy_compress_bytes_after_total",
		map[string]string{"surface": "anthropic"})
	if len(before) != 1 || len(after) != 1 {
		t.Fatalf("byte series = %d and %d, want 1 and 1\n%s", len(before), len(after), text)
	}
	if before[0].value <= after[0].value {
		t.Errorf("bytes %v -> %v, want strictly smaller", before[0].value, after[0].value)
	}
	t.Logf("metrics: blocks compressed=%v, bytes %v -> %v", compressed[0].value, before[0].value, after[0].value)
}

// TestCompress_StatsEndpoint checks /stats/compress reports the same numbers in
// a form a human can read without a Prometheus.
func TestCompress_StatsEndpoint(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	compress.Configure(true, nil)
	defer compress.Configure(false, nil)

	req := compressClaudeCodeRequest(t, compressPytestLog())
	if status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", req); status != 200 {
		t.Fatalf("request: status %d: %s", status, body)
	}

	resp, err := http.Get(proxy.URL + "/stats/compress")
	if err != nil {
		t.Fatalf("GET /stats/compress: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /stats/compress: status %d: %s", resp.StatusCode, raw)
	}

	var doc struct {
		Blocks []struct {
			Surface string `json:"surface"`
			Result  string `json:"result"`
			Count   int64  `json:"count"`
		} `json:"blocks"`
		Bytes []struct {
			Surface     string `json:"surface"`
			BytesBefore int64  `json:"bytes_before"`
			BytesAfter  int64  `json:"bytes_after"`
		} `json:"bytes"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode /stats/compress: %v\n%s", err, raw)
	}
	found := false
	for _, b := range doc.Blocks {
		if b.Surface == "anthropic" && b.Result == "compressed" && b.Count >= 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("no compressed block for surface anthropic:\n%s", raw)
	}
	for _, b := range doc.Bytes {
		if b.Surface == "anthropic" && b.BytesAfter >= b.BytesBefore {
			t.Errorf("bytes %d -> %d, want strictly smaller", b.BytesBefore, b.BytesAfter)
		}
	}
}

func decodeMessages(t *testing.T, raw json.RawMessage) []json.RawMessage {
	t.Helper()
	var msgs []json.RawMessage
	if err := json.Unmarshal(raw, &msgs); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	return msgs
}

func decodeObject(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("decode object %s: %v", raw, err)
	}
	return obj
}

func decodeArray(t *testing.T, raw json.RawMessage) []json.RawMessage {
	t.Helper()
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		t.Fatalf("decode array %s: %v", raw, err)
	}
	return arr
}

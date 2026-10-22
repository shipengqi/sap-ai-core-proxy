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

// This file is the end-to-end guard for request compression: it drives real
// requests through the real router and compares the bytes the upstream received
// with the feature off against the bytes it received with the feature on.
//
// The claim under test has two halves. First, every tool_result log in the
// request is compressed — the live zone and the history alike — because
// compressing only the live zone leaves the client's original text in the
// prefix and breaks the provider's cache on the next turn. Second, because the
// transform is a pure function of the input text, the compressed history of turn
// N+1 is byte-identical to the compressed live zone of turn N, so the prefix the
// provider cached is the prefix it is asked to read.

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

// compressClaudeCodeMessages is the history every fixture starts from: a plain
// user turn, a tool_use, a big tool_result in the history, a second tool_use and
// the live tool_result. A fresh slice is returned on every call, so callers may
// append a further turn to it.
func compressClaudeCodeMessages(log string) []any {
	return []any{
		map[string]any{"role": "user", "content": "the build is failing, please fix it"},
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "text", "text": "Let me look at the repo first."},
				map[string]any{"type": "tool_use", "id": "toolu_01", "name": "Read",
					"input": map[string]any{"file_path": "/app/tests/test_api.py"}},
			},
		},
		// An earlier turn whose tool result is also big. It is compressed like
		// every other tool_result, which is what keeps the prefix stable.
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
	}
}

// compressClaudeCodeRequestWith builds the full request around a message
// history. Two calls that share a message prefix produce byte-identical
// prefixes, which is what lets the tests compare turns.
func compressClaudeCodeRequestWith(t *testing.T, messages []any) string {
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
		"messages": messages,
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return string(body)
}

// compressClaudeCodeRequest is a request in the shape Claude Code sends: system
// blocks, tool definitions, several turns, and one large tool result at the end.
func compressClaudeCodeRequest(t *testing.T, log string) string {
	t.Helper()
	return compressClaudeCodeRequestWith(t, compressClaudeCodeMessages(log))
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

// TestCompress_EveryToolResultChanges is the central guarantee: turn the
// feature on and compare the upstream bytes field by field. Both tool_results —
// the one in the history and the one in the live zone — must shrink, while
// system, tools and every non-tool_result message keep their exact bytes.
func TestCompress_EveryToolResultChanges(t *testing.T) {
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

	// Messages 0, 1 and 3 carry no tool_result: identical bytes.
	for _, i := range []int{0, 1, 3} {
		if !bytes.Equal(beforeMsgs[i], afterMsgs[i]) {
			t.Errorf("messages[%d] changed although it carries no tool_result:\nbefore: %s\nafter:  %s",
				i, beforeMsgs[i], afterMsgs[i])
		}
	}

	// Messages 2 (history) and 4 (live zone) each carry a log tool_result.
	for _, i := range []int{2, 4} {
		beforeBlocks := decodeArray(t, decodeObject(t, beforeMsgs[i])["content"])
		afterBlocks := decodeArray(t, decodeObject(t, afterMsgs[i])["content"])
		if len(beforeBlocks) != len(afterBlocks) {
			t.Fatalf("messages[%d]: content blocks %d, want %d", i, len(afterBlocks), len(beforeBlocks))
		}
		beforeTool := decodeObject(t, beforeBlocks[len(beforeBlocks)-1])
		afterTool := decodeObject(t, afterBlocks[len(afterBlocks)-1])
		for k, v := range beforeTool {
			if k == "content" {
				continue
			}
			if !bytes.Equal(v, afterTool[k]) {
				t.Errorf("messages[%d]: tool_result field %q changed: %s -> %s", i, k, v, afterTool[k])
			}
		}
		var beforeText, afterText string
		if err := json.Unmarshal(beforeTool["content"], &beforeText); err != nil {
			t.Fatalf("messages[%d]: decode baseline text: %v", i, err)
		}
		if err := json.Unmarshal(afterTool["content"], &afterText); err != nil {
			t.Fatalf("messages[%d]: decode compressed text: %v", i, err)
		}
		if len(afterText) >= len(beforeText) {
			t.Fatalf("messages[%d]: tool_result %d -> %d bytes, want strictly smaller", i, len(beforeText), len(afterText))
		}
		if !strings.Contains(afterText, "lines omitted") {
			t.Errorf("messages[%d]: no omission marker, so nothing was dropped", i)
		}
		t.Logf("messages[%d] tool_result: %d -> %d bytes (%.1fx)", i, len(beforeText), len(afterText),
			float64(len(beforeText))/float64(len(afterText)))
	}

	// The live-zone text block is not a tool_result and must be untouched.
	beforeBlocks := decodeArray(t, decodeObject(t, beforeMsgs[4])["content"])
	afterBlocks := decodeArray(t, decodeObject(t, afterMsgs[4])["content"])
	if !bytes.Equal(beforeBlocks[0], afterBlocks[0]) {
		t.Errorf("the live-zone text block changed:\nbefore: %s\nafter:  %s", beforeBlocks[0], afterBlocks[0])
	}

	// The errors and the summary survive the rewrite.
	liveTool := decodeObject(t, afterBlocks[len(afterBlocks)-1])
	var liveText string
	if err := json.Unmarshal(liveTool["content"], &liveText); err != nil {
		t.Fatalf("decode live tool_result: %v", err)
	}
	for _, want := range []string{
		"FAILED tests/test_api.py::test_update_user",
		"FAILED tests/test_api.py::test_delete_user",
		"2 failed, 118 passed in 4.53s",
		"E       AssertionError: assert 404 == 200",
	} {
		if !strings.Contains(liveText, want) {
			t.Errorf("compressed tool_result lost %q:\n%s", want, liveText)
		}
	}
}

// TestCompress_PrefixStableAcrossTurns is the integration form of the cache
// guarantee: turn N+1 sends the turn-N history with the compressed tool_result
// in it, and the bytes the upstream receives for that history must be identical
// to the bytes it received for turn N. One byte of drift and the provider
// re-writes the whole prefix.
func TestCompress_PrefixStableAcrossTurns(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	compress.Configure(true, nil)
	defer compress.Configure(false, nil)

	turnOne := compressClaudeCodeRequest(t, compressPytestLog())

	// Turn two: the same conversation, one more assistant/user pair. The client
	// sends its own original bytes for the earlier tool_result; the proxy must
	// rewrite them into the same bytes it sent last turn.
	turnTwoMsgs := compressClaudeCodeMessages(compressPytestLog())
	turnTwoMsgs = append(turnTwoMsgs,
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": "Now the linter."},
			map[string]any{"type": "tool_use", "id": "toolu_03", "name": "Bash",
				"input": map[string]any{"command": "ruff check ."}},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "toolu_03",
				"content": strings.ReplaceAll(compressPytestLog(), "test_api.py", "test_web.py")},
		}},
	)
	turnTwo := compressClaudeCodeRequestWith(t, turnTwoMsgs)

	for i, req := range []string{turnOne, turnTwo} {
		if status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", req); status != 200 {
			t.Fatalf("turn %d: status %d: %s", i+1, status, body)
		}
	}

	bodies := up.bodiesSnapshot()
	if len(bodies) != 2 {
		t.Fatalf("upstream received %d requests, want 2", len(bodies))
	}
	var first, second map[string]json.RawMessage
	if err := json.Unmarshal(bodies[0], &first); err != nil {
		t.Fatalf("decode turn 1: %v", err)
	}
	if err := json.Unmarshal(bodies[1], &second); err != nil {
		t.Fatalf("decode turn 2: %v", err)
	}

	for _, key := range []string{"system", "tools"} {
		if !bytes.Equal(first[key], second[key]) {
			t.Errorf("%s differs between turns", key)
		}
	}

	firstMsgs := decodeMessages(t, first["messages"])
	secondMsgs := decodeMessages(t, second["messages"])
	if len(firstMsgs) != 5 || len(secondMsgs) != 7 {
		t.Fatalf("message counts = %d and %d, want 5 and 7", len(firstMsgs), len(secondMsgs))
	}
	for i := range firstMsgs {
		if !bytes.Equal(firstMsgs[i], secondMsgs[i]) {
			t.Fatalf("turn 2 rewrote the prefix at messages[%d]\nturn 1: %s\nturn 2: %s",
				i, firstMsgs[i], secondMsgs[i])
		}
	}

	// And the prefix really is compressed, so the test is not passing on
	// untouched bytes.
	historyTool := decodeObject(t, decodeArray(t, decodeObject(t, secondMsgs[2])["content"])[0])
	var historyText string
	if err := json.Unmarshal(historyTool["content"], &historyText); err != nil {
		t.Fatalf("decode history tool_result: %v", err)
	}
	if !strings.Contains(historyText, "lines omitted") {
		t.Fatalf("the history tool_result was not compressed, so prefix stability proves nothing:\n%s", historyText)
	}
	// The new turn's tool_result is compressed too.
	newTool := decodeObject(t, decodeArray(t, decodeObject(t, secondMsgs[6])["content"])[0])
	var newText string
	if err := json.Unmarshal(newTool["content"], &newText); err != nil {
		t.Fatalf("decode new tool_result: %v", err)
	}
	if !strings.Contains(newText, "lines omitted") {
		t.Errorf("the newest tool_result was not compressed:\n%s", newText)
	}
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
// must pass through untouched even with the switch on. Only the history block,
// which really is a log, may shrink.
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
	var before, after map[string]json.RawMessage
	if err := json.Unmarshal(bodies[0], &before); err != nil {
		t.Fatalf("decode baseline: %v", err)
	}
	if err := json.Unmarshal(bodies[1], &after); err != nil {
		t.Fatalf("decode compressed: %v", err)
	}

	// The live-zone tool_result is prose, not a log: identical bytes.
	beforeMsgs := decodeMessages(t, before["messages"])
	afterMsgs := decodeMessages(t, after["messages"])
	if len(beforeMsgs) != len(afterMsgs) {
		t.Fatalf("messages length %d, want %d", len(afterMsgs), len(beforeMsgs))
	}
	liveIndex := len(beforeMsgs) - 1
	beforeBlocks := decodeArray(t, decodeObject(t, beforeMsgs[liveIndex])["content"])
	afterBlocks := decodeArray(t, decodeObject(t, afterMsgs[liveIndex])["content"])
	if !bytes.Equal(beforeBlocks[len(beforeBlocks)-1], afterBlocks[len(afterBlocks)-1]) {
		t.Fatalf("a non-log tool result was modified:\nbefore: %s\nafter:  %s",
			beforeBlocks[len(beforeBlocks)-1], afterBlocks[len(afterBlocks)-1])
	}

	// Everything except the history tool_result is identical too: the prose
	// block must not drag any neighbour into a rewrite.
	for i := range beforeMsgs {
		if i == 2 {
			continue
		}
		if !bytes.Equal(beforeMsgs[i], afterMsgs[i]) {
			t.Errorf("messages[%d] changed although it carries no log:\nbefore: %s\nafter:  %s",
				i, beforeMsgs[i], afterMsgs[i])
		}
	}
	for _, key := range []string{"system", "tools"} {
		if !bytes.Equal(before[key], after[key]) {
			t.Errorf("%s changed", key)
		}
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

package test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/shipengqi/sap-ai-core-proxy/internal/compress"
)

// End-to-end guard for the OpenAI-shaped compressor. It drives real requests
// through the real router and inspects what the mock upstream received, with
// the kill switch off and on. Two surfaces matter: /chat/completions, where
// tool output is a role=tool message, and /responses, where Codex sends
// function_call_output items.

func compressOpenAIChatRequest(t *testing.T, messages []any) string {
	t.Helper()
	return compressMustJSON(t, map[string]any{
		"model":    "gpt-4o",
		"messages": messages,
	})
}

func compressCodexRequest(t *testing.T, input []any, stream bool) string {
	t.Helper()
	return compressMustJSON(t, map[string]any{
		"model":  "gpt-4o",
		"stream": stream,
		"store":  false,
		"input":  input,
	})
}

func compressMustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return string(b)
}

func compressToolMessages(log string) []any {
	return []any{
		map[string]any{"role": "system", "content": "You are a coding agent."},
		map[string]any{"role": "user", "content": "run the tests and fix the failures"},
		map[string]any{
			"role": "assistant",
			"tool_calls": []any{
				map[string]any{"id": "call_1", "type": "function",
					"function": map[string]any{"name": "Bash", "arguments": `{"command":"pytest -v"}`}},
			},
		},
		map[string]any{"role": "tool", "tool_call_id": "call_1", "content": log},
	}
}

func compressCodexInput(log string) []any {
	return []any{
		map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": "run the tests"}}},
		map[string]any{"type": "function_call_output", "call_id": "call_1", "output": log},
	}
}

func compressUpstreamMessages(t *testing.T, body []byte) []json.RawMessage {
	t.Helper()
	var doc struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decode upstream body: %v\n%s", err, body)
	}
	return doc.Messages
}

func compressPostOK(t *testing.T, url, body string) {
	t.Helper()
	if status, resp := qaPost(t, url, body); status != 200 {
		t.Fatalf("POST %s: status %d: %s", url, status, resp)
	}
}

// TestCompressOpenAI_ChatOffMatchesBaseline pins the default: with the switch
// off, the upstream receives exactly the bytes it received before this feature
// existed, over and over.
func TestCompressOpenAI_ChatOffMatchesBaseline(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	compress.Configure(false, nil)
	req := compressOpenAIChatRequest(t, compressToolMessages(compressPytestLog()))
	for i := 0; i < 2; i++ {
		compressPostOK(t, proxy.URL+"/openai/v1/chat/completions", req)
	}
	bodies := up.bodiesSnapshot()
	if len(bodies) != 2 {
		t.Fatalf("upstream received %d requests, want 2", len(bodies))
	}
	if !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("the same request produced different upstream bytes")
	}
	if bytes.Contains(bodies[0], []byte("lines omitted")) {
		t.Fatalf("baseline body contains a compression marker:\n%s", bodies[0])
	}
	if !bytes.Contains(bodies[0], []byte("test_case_059 PASSED")) {
		t.Fatalf("baseline body is missing the original tool output:\n%s", bodies[0])
	}
}

// TestCompressOpenAI_ChatCompressesToolOutput is the main-path assertion. The
// noisy progress lines go, the error and the summary stay, and the tool call
// metadata survives.
func TestCompressOpenAI_ChatCompressesToolOutput(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	compress.Configure(true, nil)
	defer compress.Configure(false, nil)
	log := compressPytestLog()
	req := compressOpenAIChatRequest(t, compressToolMessages(log))
	compressPostOK(t, proxy.URL+"/openai/v1/chat/completions", req)

	bodies := up.bodiesSnapshot()
	if len(bodies) != 1 {
		t.Fatalf("upstream received %d requests, want 1", len(bodies))
	}
	body := bodies[0]
	if !bytes.Contains(body, []byte("lines omitted")) {
		t.Fatalf("upstream body has no compression marker:\n%s", body)
	}
	if bytes.Contains(body, []byte("test_case_059 PASSED")) {
		t.Fatalf("noisy progress lines survived:\n%s", body)
	}
	if !bytes.Contains(body, []byte("AssertionError: assert 404 == 200")) {
		t.Fatalf("an error line was lost:\n%s", body)
	}
	if !bytes.Contains(body, []byte("2 failed, 118 passed")) {
		t.Fatalf("the summary was lost:\n%s", body)
	}
	if !bytes.Contains(body, []byte(`"tool_call_id":"call_1"`)) {
		t.Fatalf("tool_call_id was lost:\n%s", body)
	}
	if len(body) >= len(req) {
		t.Fatalf("upstream body did not shrink: %d bytes of %d", len(body), len(req))
	}
}

// TestCompressOpenAI_ChatUserMessageUntouched is the false-positive guard: the
// same log pasted into a user message is the human's words and must survive
// byte-for-byte, even though the compressor is enabled.
func TestCompressOpenAI_ChatUserMessageUntouched(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	compress.Configure(true, nil)
	defer compress.Configure(false, nil)
	log := compressPytestLog()
	req := compressOpenAIChatRequest(t, []any{
		map[string]any{"role": "user", "content": log},
	})
	compressPostOK(t, proxy.URL+"/openai/v1/chat/completions", req)

	bodies := up.bodiesSnapshot()
	if len(bodies) != 1 {
		t.Fatalf("upstream received %d requests, want 1", len(bodies))
	}
	body := bodies[0]
	if bytes.Contains(body, []byte("lines omitted")) {
		t.Fatalf("a user message was compressed:\n%s", body)
	}
	if !bytes.Contains(body, []byte("test_case_059 PASSED")) {
		t.Fatalf("the user's log was rewritten:\n%s", body)
	}
}

// TestCompressOpenAI_StreamingChatCompresses covers the streaming branch, which
// rebuilds the body through a different path (chatStream / buildSAPChatBody).
func TestCompressOpenAI_StreamingChatCompresses(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	compress.Configure(true, nil)
	defer compress.Configure(false, nil)
	req := compressMustJSON(t, map[string]any{
		"model":    "gpt-4o",
		"stream":   true,
		"messages": compressToolMessages(compressPytestLog()),
	})
	if status, resp := qaPost(t, proxy.URL+"/openai/v1/chat/completions", req); status != 200 {
		t.Fatalf("streaming request: status %d: %s", status, resp)
	}
	bodies := up.bodiesSnapshot()
	if len(bodies) == 0 {
		t.Fatalf("upstream received no request")
	}
	if !bytes.Contains(bodies[0], []byte("lines omitted")) {
		t.Fatalf("streaming body was not compressed:\n%s", bodies[0])
	}
}

// TestCompressOpenAI_CodexResponsesOffMatchesBaseline and the "on" test below
// cover /openai/v1/responses, the endpoint Codex is configured to call.
func TestCompressOpenAI_CodexResponsesOffMatchesBaseline(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	compress.Configure(false, nil)
	req := compressCodexRequest(t, compressCodexInput(compressPytestLog()), false)
	compressPostOK(t, proxy.URL+"/openai/v1/responses", req)

	bodies := up.bodiesSnapshot()
	if len(bodies) != 1 {
		t.Fatalf("upstream received %d requests, want 1", len(bodies))
	}
	if bytes.Contains(bodies[0], []byte("lines omitted")) {
		t.Fatalf("baseline Responses body contains a compression marker:\n%s", bodies[0])
	}
	if !bytes.Contains(bodies[0], []byte("test_case_059 PASSED")) {
		t.Fatalf("baseline Responses body lost the original output:\n%s", bodies[0])
	}
}

func TestCompressOpenAI_CodexResponsesCompresses(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	compress.Configure(true, nil)
	defer compress.Configure(false, nil)
	req := compressCodexRequest(t, compressCodexInput(compressPytestLog()), false)
	compressPostOK(t, proxy.URL+"/openai/v1/responses", req)

	bodies := up.bodiesSnapshot()
	if len(bodies) != 1 {
		t.Fatalf("upstream received %d requests, want 1", len(bodies))
	}
	body := bodies[0]
	if !bytes.Contains(body, []byte("lines omitted")) {
		t.Fatalf("function_call_output was not compressed:\n%s", body)
	}
	if bytes.Contains(body, []byte("test_case_059 PASSED")) {
		t.Fatalf("noisy progress lines survived:\n%s", body)
	}
	if !bytes.Contains(body, []byte("AssertionError: assert 404 == 200")) {
		t.Fatalf("an error line was lost:\n%s", body)
	}
	if !bytes.Contains(body, []byte(`"call_id":"call_1"`)) {
		t.Fatalf("call_id was lost:\n%s", body)
	}
	if !bytes.Contains(body, []byte("run the tests")) {
		t.Fatalf("the user input item was rewritten:\n%s", body)
	}
	// Other Responses fields must still be present after the body is rebuilt.
	if !bytes.Contains(body, []byte(`"store":false`)) {
		t.Fatalf("store was lost when the body was rebuilt:\n%s", body)
	}
}

// TestCompressOpenAI_CodexStreamingResponses covers Codex's default stream:true
// call. The mock upstream answers with JSON rather than SSE, so only the bytes
// it received are asserted here.
func TestCompressOpenAI_CodexStreamingResponses(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	compress.Configure(true, nil)
	defer compress.Configure(false, nil)
	req := compressCodexRequest(t, compressCodexInput(compressPytestLog()), true)
	_, _ = qaPost(t, proxy.URL+"/openai/v1/responses", req)

	bodies := up.bodiesSnapshot()
	if len(bodies) == 0 {
		t.Fatalf("upstream received no request")
	}
	if !bytes.Contains(bodies[0], []byte("lines omitted")) {
		t.Fatalf("streaming Responses body was not compressed:\n%s", bodies[0])
	}
}

// TestCompressOpenAI_CodexPreviousResponseStillCompresses covers the stored-
// response affinity path. The upstream owns the history in that case; only the
// incremental input is rewritten, so the cached prefix stays untouched.
func TestCompressOpenAI_CodexPreviousResponseStillCompresses(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	compress.Configure(true, nil)
	defer compress.Configure(false, nil)
	// Seed a stored response id the registry can resolve on the next turn.
	compressPostOK(t, proxy.URL+"/openai/v1/responses",
		compressCodexRequest(t, compressCodexInput("ok"), false))

	next := compressMustJSON(t, map[string]any{
		"model":                "gpt-4o",
		"previous_response_id": "resp-qa",
		"input":                compressCodexInput(compressPytestLog()),
	})
	compressPostOK(t, proxy.URL+"/openai/v1/responses", next)

	bodies := up.bodiesSnapshot()
	if len(bodies) != 2 {
		t.Fatalf("upstream received %d requests, want 2", len(bodies))
	}
	if !bytes.Contains(bodies[1], []byte("lines omitted")) {
		t.Fatalf("incremental input was not compressed with previous_response_id:\n%s", bodies[1])
	}
	if bytes.Contains(bodies[1], []byte("test_case_059 PASSED")) {
		t.Fatalf("noisy progress lines survived:\n%s", bodies[1])
	}
}

// TestCompressOpenAI_PrefixStableAcrossTurns is the cache guarantee: the
// compressed history of turn N+1 must be byte-identical to the compressed live
// zone of turn N, or the provider's implicit prefix cache misses every turn.
func TestCompressOpenAI_PrefixStableAcrossTurns(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	compress.Configure(true, nil)
	defer compress.Configure(false, nil)
	history := compressToolMessages(compressPytestLog())
	turn2 := append(append([]any{}, history...),
		map[string]any{"role": "assistant", "content": "looking at the failure"},
		map[string]any{"role": "tool", "tool_call_id": "call_2",
			"content": strings.ReplaceAll(compressPytestLog(), "test_api.py", "test_db.py")},
	)
	compressPostOK(t, proxy.URL+"/openai/v1/chat/completions", compressOpenAIChatRequest(t, history))
	compressPostOK(t, proxy.URL+"/openai/v1/chat/completions", compressOpenAIChatRequest(t, turn2))

	bodies := up.bodiesSnapshot()
	if len(bodies) != 2 {
		t.Fatalf("upstream received %d requests, want 2", len(bodies))
	}
	for i, body := range bodies {
		if !bytes.Contains(body, []byte("lines omitted")) {
			t.Fatalf("turn %d was not compressed:\n%s", i, body)
		}
	}
	first := compressUpstreamMessages(t, bodies[0])
	second := compressUpstreamMessages(t, bodies[1])
	if len(first) == 0 || len(second) < len(first) {
		t.Fatalf("message counts %d / %d", len(first), len(second))
	}
	for i := range first {
		if !bytes.Equal(first[i], second[i]) {
			t.Fatalf("message %d of the shared prefix differs between turns:\n%s\n%s", i, first[i], second[i])
		}
	}
}

// TestCompressOpenAI_MetricsSurfaceOpenAI proves the new path reports into the
// same collector as the Anthropic path, under its own surface label.
func TestCompressOpenAI_MetricsSurfaceOpenAI(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	compress.Configure(false, nil)
	before := compressOpenAIStatsCount(t, proxy.URL)
	compress.Configure(true, nil)
	defer compress.Configure(false, nil)
	compressPostOK(t, proxy.URL+"/openai/v1/chat/completions",
		compressOpenAIChatRequest(t, compressToolMessages(compressPytestLog())))
	after := compressOpenAIStatsCount(t, proxy.URL)
	if after <= before {
		t.Fatalf("surface=openai compressed count did not grow: %d -> %d", before, after)
	}
}

type compressStatsDoc struct {
	Blocks []struct {
		Surface string `json:"surface"`
		Result  string `json:"result"`
		Count   int64  `json:"count"`
	} `json:"blocks"`
}

func compressOpenAIStatsCount(t *testing.T, baseURL string) int64 {
	t.Helper()
	resp, err := http.Get(baseURL + "/stats/compress")
	if err != nil {
		t.Fatalf("GET /stats/compress: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /stats/compress: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /stats/compress: status %d: %s", resp.StatusCode, raw)
	}
	var doc compressStatsDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode /stats/compress: %v\n%s", err, raw)
	}
	var total int64
	for _, b := range doc.Blocks {
		if b.Surface == "openai" && b.Result == "compressed" {
			total += b.Count
		}
	}
	return total
}

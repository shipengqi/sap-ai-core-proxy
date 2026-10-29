package test

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// This file is the regression suite for the F1–F4 fixes. Each test is written
// against the *contract* the fix promised, not against the implementation:
// the client-visible bytes and status must be unchanged, while the counters must
// classify the request correctly.

// qaAnthropicSSEErrorText is a healthy stream whose model text happens to contain
// the literal string {"error":"boom"}. On the wire the quotes are escaped as \",
// which is exactly why a substring test for `"error"` must not fire.
const qaAnthropicSSEErrorText = "event: message_start\n" +
	`data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}` + "\n" +
	"\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"{\"error\":\"boom\"}"}}` + "\n" +
	"\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","usage":{"output_tokens":12}}` + "\n" +
	"\n" + "event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"

// qaAnthropicSSEWithBraceLine is a healthy stream plus one malformed non-SSE line
// that starts with "{" and is not an error.
const qaAnthropicSSEWithBraceLine = "event: message_start\n" +
	`data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}` + "\n" +
	"\n" +
	`{"note":"not an error"}` + "\n" +
	"\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","usage":{"output_tokens":12}}` + "\n" +
	"\n" + "event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"

// TestQA_F1_UpstreamStreamFailureRecordedAsFailure covers the real-failure path:
// the upstream rejects the streaming request outright (429) before any frame.
// The client must still see 200 (pre-existing wire behaviour), but the counters
// must record the 429 and must not treat it as missing usage.
func TestQA_F1_UpstreamStreamFailureRecordedAsFailure(t *testing.T) {
	up := newQAUpstream(t)
	up.streamStatus = http.StatusTooManyRequests
	up.streamBody = []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyStreaming)
	if status != http.StatusTooManyRequests {
		t.Fatalf("client status = %d, want upstream 429: %s", status, body)
	}
	// Fidelity: the client receives the upstream body plus the newline the pipe
	// adds to every line.
	if want := string(up.streamBody); string(body) != want {
		t.Errorf("client body = %q, want %q", body, want)
	}

	doc := qaGetStats(t, proxy.URL)
	if doc.Totals.Requests != 1 {
		t.Errorf("requests = %d, want 1", doc.Totals.Requests)
	}
	if doc.Totals.Failures != 1 {
		t.Errorf("failures = %d, want 1", doc.Totals.Failures)
	}
	if doc.Totals.UsageMissing != 0 {
		t.Errorf("usage_missing = %d, want 0: a failed request is not missing usage", doc.Totals.UsageMissing)
	}
	if doc.Totals.Uninstrumented != 0 {
		t.Errorf("uninstrumented = %d, want 0", doc.Totals.Uninstrumented)
	}
	if doc.Totals.Tokens != (qaTokens{}) {
		t.Errorf("tokens = %+v, want zero", doc.Totals.Tokens)
	}

	metrics := qaGetMetrics(t, proxy.URL)
	wantLine := `sap_proxy_requests_total{surface="anthropic",model="anthropic--claude-3.5-sonnet",status="429",stream="true"} 1`
	if !strings.Contains(metrics, wantLine) {
		t.Errorf("/metrics is missing %q\n%s", wantLine, metrics)
	}
	if strings.Contains(metrics, `status="200",stream="true"`) {
		t.Errorf("the failed stream is still recorded as a 200\n%s", metrics)
	}
}

// TestQA_F1_FakeSuccessBareJSONError covers the harder failure: the upstream
// answers 200 and puts an error object in the body with no SSE framing. The
// stream helpers have to inspect the body to notice.
func TestQA_F1_FakeSuccessBareJSONError(t *testing.T) {
	up := newQAUpstream(t)
	up.streamBody = []byte(`{"type":"error","error":{"type":"overloaded_error","message":"overloaded"}}`)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyStreaming)
	if status != http.StatusOK {
		t.Fatalf("client status = %d, want the unchanged 200: %s", status, body)
	}
	if want := "event: error\ndata: " + string(up.streamBody) + "\n\n"; string(body) != want {
		t.Errorf("client body = %q, want %q", body, want)
	}

	doc := qaGetStats(t, proxy.URL)
	if doc.Totals.Failures != 1 {
		t.Errorf("failures = %d, want 1", doc.Totals.Failures)
	}
	if doc.Totals.UsageMissing != 0 || doc.Totals.Uninstrumented != 0 {
		t.Errorf("usage_missing=%d uninstrumented=%d, want 0/0", doc.Totals.UsageMissing, doc.Totals.Uninstrumented)
	}

	metrics := qaGetMetrics(t, proxy.URL)
	wantLine := `sap_proxy_requests_total{surface="anthropic",model="anthropic--claude-3.5-sonnet",status="502",stream="true"} 1`
	if !strings.Contains(metrics, wantLine) {
		t.Errorf("/metrics is missing %q\n%s", wantLine, metrics)
	}
	if strings.Contains(metrics, `status="200",stream="true"`) {
		t.Errorf("the fake-success stream is still recorded as a 200\n%s", metrics)
	}
}

// TestQA_F2_ResponsesUsageAccounted covers the Responses API surface, including
// its cached-token spelling under input_tokens_details.
func TestQA_F2_ResponsesUsageAccounted(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	status, body := qaPost(t, proxy.URL+"/openai/v1/responses", `{"model":"gpt-4o","input":"hi"}`)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}
	// Fidelity: the client body is the upstream body, byte for byte.
	if !bytes.Equal(body, []byte(qaResponsesBody)) {
		t.Errorf("client body differs from the upstream body\nwant: %s\ngot:  %s", qaResponsesBody, body)
	}

	doc := qaGetStats(t, proxy.URL)
	want := qaTokens{Input: 30, Output: 7, CacheRead: 5}
	if doc.Totals.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", doc.Totals.Tokens, want)
	}
	if doc.Totals.UsageMissing != 0 || doc.Totals.Uninstrumented != 0 {
		t.Errorf("usage_missing=%d uninstrumented=%d, want 0/0", doc.Totals.UsageMissing, doc.Totals.Uninstrumented)
	}
	if model := qaModelOf(t, doc); model.Model != "gpt-4o" {
		t.Errorf("model label = %q, want gpt-4o", model.Model)
	}

	metrics := qaGetMetrics(t, proxy.URL)
	for _, wantLine := range []string{
		`sap_proxy_input_tokens_total{surface="openai",model="gpt-4o"} 30`,
		`sap_proxy_output_tokens_total{surface="openai",model="gpt-4o"} 7`,
		`sap_proxy_cache_read_tokens_total{surface="openai",model="gpt-4o"} 5`,
	} {
		if !strings.Contains(metrics, wantLine) {
			t.Errorf("/metrics is missing %q\n%s", wantLine, metrics)
		}
	}
}

// TestQA_F3_EmbeddingsAndImages covers the two remaining POST surfaces:
// embeddings report real prompt tokens, image generation has no token concept at
// all and must not be reported as a measurement gap.
func TestQA_F3_EmbeddingsAndImages(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	if status, body := qaPost(t, proxy.URL+"/openai/v1/embeddings", `{"model":"gpt-4o","input":"hi"}`); status != 200 {
		t.Fatalf("embeddings: expected 200, got %d: %s", status, body)
	}
	afterEmbeddings := qaGetStats(t, proxy.URL)
	if afterEmbeddings.Totals.Tokens.Input != 7 {
		t.Errorf("embeddings input = %d, want 7", afterEmbeddings.Totals.Tokens.Input)
	}
	if afterEmbeddings.Totals.UsageMissing != 0 || afterEmbeddings.Totals.Uninstrumented != 0 {
		t.Errorf("embeddings: usage_missing=%d uninstrumented=%d, want 0/0",
			afterEmbeddings.Totals.UsageMissing, afterEmbeddings.Totals.Uninstrumented)
	}

	if status, body := qaPost(t, proxy.URL+"/openai/v1/images/generations", `{"model":"gpt-4o","prompt":"a cat","n":1}`); status != 200 {
		t.Fatalf("images: expected 200, got %d: %s", status, body)
	}
	afterImages := qaGetStats(t, proxy.URL)

	if afterImages.Totals.Requests != afterEmbeddings.Totals.Requests+1 {
		t.Errorf("images did not increment requests: %d -> %d",
			afterEmbeddings.Totals.Requests, afterImages.Totals.Requests)
	}
	if afterImages.Totals.UsageMissing != afterEmbeddings.Totals.UsageMissing {
		t.Errorf("images changed usage_missing: %d -> %d",
			afterEmbeddings.Totals.UsageMissing, afterImages.Totals.UsageMissing)
	}
	if afterImages.Totals.Uninstrumented != afterEmbeddings.Totals.Uninstrumented {
		t.Errorf("images changed uninstrumented: %d -> %d",
			afterEmbeddings.Totals.Uninstrumented, afterImages.Totals.Uninstrumented)
	}
	if afterImages.Totals.Failures != 0 {
		t.Errorf("failures = %d, want 0", afterImages.Totals.Failures)
	}
}

// TestQA_BucketsReconcile is the invariant the new three-bucket split has to
// satisfy: every request lands in exactly one terminal bucket, so the numbers
// have to add up. It drives a deliberately mixed batch through one proxy.
func TestQA_BucketsReconcile(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	// 1. Anthropic non-streaming, usage reported.
	if status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyNonStreaming); status != 200 {
		t.Fatalf("anthropic: %d %s", status, body)
	}
	// 2. Anthropic non-streaming, upstream reports nothing.
	up.invokeBody = []byte(`{"id":"msg_silent","type":"message","content":[{"type":"text","text":"ok"}]}`)
	if status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyNonStreaming); status != 200 {
		t.Fatalf("anthropic silent: %d %s", status, body)
	}
	// 3. Streaming request the upstream rejects.
	up.streamStatus = http.StatusBadRequest
	up.streamBody = []byte(`{"type":"error","error":{"message":"bad"}}`)
	if status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyStreaming); status != 400 {
		t.Fatalf("anthropic stream failure: %d %s", status, body)
	}
	// 4. Embeddings, usage reported.
	if status, body := qaPost(t, proxy.URL+"/openai/v1/embeddings", `{"model":"gpt-4o","input":"hi"}`); status != 200 {
		t.Fatalf("embeddings: %d %s", status, body)
	}
	// 5. Images, no token concept.
	if status, body := qaPost(t, proxy.URL+"/openai/v1/images/generations", `{"model":"gpt-4o","prompt":"x"}`); status != 200 {
		t.Fatalf("images: %d %s", status, body)
	}
	// 6. Responses API, usage reported.
	if status, body := qaPost(t, proxy.URL+"/openai/v1/responses", `{"model":"gpt-4o","input":"hi"}`); status != 200 {
		t.Fatalf("responses: %d %s", status, body)
	}

	const (
		sentRequests       = 6
		wantFailures       = 1
		wantUsageMissing   = 1
		wantUninstrumented = 0
		// Known by construction: exactly one image request.
		wantNoUsageExpected = 1
	)

	doc := qaGetStats(t, proxy.URL)
	if doc.Totals.Requests != sentRequests {
		t.Fatalf("requests = %d, want %d", doc.Totals.Requests, sentRequests)
	}

	// Identity 1: requests == failures + successes.
	successes := doc.Totals.Requests - doc.Totals.Failures
	if doc.Totals.Failures != wantFailures {
		t.Errorf("failures = %d, want %d", doc.Totals.Failures, wantFailures)
	}

	// Identity 2: every success lands in exactly one of the four buckets.
	withUsage := successes - doc.Totals.UsageMissing - doc.Totals.Uninstrumented - wantNoUsageExpected
	if doc.Totals.UsageMissing != wantUsageMissing {
		t.Errorf("usage_missing = %d, want %d", doc.Totals.UsageMissing, wantUsageMissing)
	}
	if doc.Totals.Uninstrumented != wantUninstrumented {
		t.Errorf("uninstrumented = %d, want %d", doc.Totals.Uninstrumented, wantUninstrumented)
	}
	// 3 successful requests reported usage: anthropic (1), embeddings (4), responses (6).
	if withUsage != 3 {
		t.Errorf("successful requests with usage = %d, want 3 (successes %d - missing %d - uninstrumented %d - noUsageExpected %d)",
			withUsage, successes, doc.Totals.UsageMissing, doc.Totals.Uninstrumented, wantNoUsageExpected)
	}

	// The token totals must equal exactly the three with-usage responses.
	want := qaTokens{
		Input:      10 + 7 + 30, // anthropic + embeddings + responses
		Output:     8 + 7,       // anthropic + responses
		CacheRead:  4 + 5,       // anthropic + responses input_tokens_details
		CacheWrite: 2,           // anthropic
	}
	if doc.Totals.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", doc.Totals.Tokens, want)
	}

	// The per-model breakdown must add up to the same buckets.
	var missing, uninstrumented, failures int64
	for _, entry := range doc.ByModel {
		missing += entry.UsageMissing
		uninstrumented += entry.Uninstrumented
		failures += entry.Failures
	}
	if missing != doc.Totals.UsageMissing || uninstrumented != doc.Totals.Uninstrumented || failures != doc.Totals.Failures {
		t.Errorf("by_model (%d/%d/%d) disagrees with totals (%d/%d/%d)",
			missing, uninstrumented, failures,
			doc.Totals.UsageMissing, doc.Totals.Uninstrumented, doc.Totals.Failures)
	}
}

// TestQA_StreamDataLineMentioningErrorIsNotFailure is the false-positive guard
// for the new "inspect lines that start with {" path: a model that prints an
// error object in its answer must not turn a healthy stream into a failure.
func TestQA_StreamDataLineMentioningErrorIsNotFailure(t *testing.T) {
	up := newQAUpstream(t)
	up.streamBody = []byte(qaAnthropicSSEErrorText)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyStreaming)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}
	// Byte fidelity first: the accounting must not have touched the stream.
	if !bytes.Equal(body, []byte(qaAnthropicSSEErrorText)) {
		t.Fatalf("stream bytes changed\nwant: %q\ngot:  %q", qaAnthropicSSEErrorText, body)
	}

	doc := qaGetStats(t, proxy.URL)
	if doc.Totals.Failures != 0 {
		t.Errorf("failures = %d, want 0: an error string inside a text delta is not an error event", doc.Totals.Failures)
	}
	if doc.Totals.UsageMissing != 0 {
		t.Errorf("usage_missing = %d, want 0", doc.Totals.UsageMissing)
	}
	if want := (qaTokens{Input: 10, Output: 12}); doc.Totals.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", doc.Totals.Tokens, want)
	}
	metrics := qaGetMetrics(t, proxy.URL)
	if !strings.Contains(metrics, `status="200",stream="true"`) {
		t.Errorf("the healthy stream is not recorded as 200\n%s", metrics)
	}
}

// TestQA_MalformedStreamBraceLineIsTransparent covers the other direction: a
// non-SSE line that starts with "{" but is not an error must be forwarded
// unchanged and must not disturb the token count.
func TestQA_MalformedStreamBraceLineIsTransparent(t *testing.T) {
	up := newQAUpstream(t)
	up.streamBody = []byte(qaAnthropicSSEWithBraceLine)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyStreaming)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}
	if !bytes.Equal(body, []byte(qaAnthropicSSEWithBraceLine)) {
		t.Fatalf("stream bytes changed\nwant: %q\ngot:  %q", qaAnthropicSSEWithBraceLine, body)
	}

	doc := qaGetStats(t, proxy.URL)
	if doc.Totals.Failures != 0 {
		t.Errorf("failures = %d, want 0", doc.Totals.Failures)
	}
	if want := (qaTokens{Input: 10, Output: 12}); doc.Totals.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", doc.Totals.Tokens, want)
	}
	if doc.Totals.UsageMissing != 0 {
		t.Errorf("usage_missing = %d, want 0", doc.Totals.UsageMissing)
	}
}

// TestQA_BraceLineWithNullErrorIsNotFailure pins the null guard: several
// providers include "error": null on success bodies.
func TestQA_BraceLineWithNullErrorIsNotFailure(t *testing.T) {
	up := newQAUpstream(t)
	up.streamBody = []byte(`{"error":null,"note":"ok"}` + "\n\n" + "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	if status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyStreaming); status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}

	doc := qaGetStats(t, proxy.URL)
	if doc.Totals.Failures != 0 {
		t.Errorf(`failures = %d, want 0: "error": null is not a failure`, doc.Totals.Failures)
	}
}

// TestQA_StatusLabelIsEffectiveStatus pins the semantic change from the fix: the
// status label carries the effective status (what the upstream did), while the
// client keeps seeing 200. The non-streaming 400 path is unchanged.
func TestQA_StatusLabelIsEffectiveStatus(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	// Non-streaming 400: client and label agree.
	up.invokeStatus = http.StatusBadRequest
	up.invokeBody = []byte(`{"type":"error","error":{"message":"bad"}}`)
	if status, _ := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyNonStreaming); status != http.StatusBadRequest {
		t.Fatalf("non-streaming 400: client status = %d, want 400", status)
	}

	// Streaming 400: client 200, label 400.
	up.streamStatus = http.StatusBadRequest
	up.streamBody = []byte(`{"type":"error","error":{"message":"bad"}}`)
	if status, _ := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyStreaming); status != http.StatusBadRequest {
		t.Fatalf("streaming 400: client status = %d, want 400", status)
	}

	metrics := qaGetMetrics(t, proxy.URL)
	for _, wantLine := range []string{
		`sap_proxy_requests_total{surface="anthropic",model="anthropic--claude-3.5-sonnet",status="400",stream="false"} 1`,
		`sap_proxy_requests_total{surface="anthropic",model="anthropic--claude-3.5-sonnet",status="400",stream="true"} 1`,
	} {
		if !strings.Contains(metrics, wantLine) {
			t.Errorf("/metrics is missing %q\n%s", wantLine, metrics)
		}
	}

	doc := qaGetStats(t, proxy.URL)
	if doc.Totals.Failures != 2 || doc.Totals.Requests != 2 {
		t.Errorf("failures=%d requests=%d, want 2/2", doc.Totals.Failures, doc.Totals.Requests)
	}
	if doc.Totals.UsageMissing != 0 || doc.Totals.Uninstrumented != 0 {
		t.Errorf("usage_missing=%d uninstrumented=%d, want 0/0", doc.Totals.UsageMissing, doc.Totals.Uninstrumented)
	}
}

// TestQA_F1_StreamFailureAcrossSurfaces checks the other streaming call sites.
// Eight handlers record the upstream status before locking the wire status; a
// single forgotten call would silently keep counting that surface's failures as
// successes, and only an end-to-end test can see it.
func TestQA_F1_StreamFailureAcrossSurfaces(t *testing.T) {
	tests := []struct {
		name             string
		path             string
		request          string
		setup            func(*qaUpstream)
		wantSurface      string
		wantModel        string
		wantStatus       int
		wantClientStatus int
		wantBody         string
	}{
		{
			name:    "anthropic",
			path:    "/anthropic/v1/messages",
			request: qaClientBodyStreaming,
			setup: func(u *qaUpstream) {
				u.streamStatus = http.StatusTooManyRequests
				u.streamBody = []byte(`{"type":"error","error":{"message":"slow down"}}`)
			},
			wantSurface: "anthropic",
			wantModel:   "anthropic--claude-3.5-sonnet",
			wantStatus:  http.StatusTooManyRequests,
			wantBody:    `{"type":"error","error":{"message":"slow down"}}`,
		},
		{
			name:    "openai-chat",
			path:    "/openai/v1/chat/completions",
			request: `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			setup: func(u *qaUpstream) {
				u.chatStreamStatus = http.StatusTooManyRequests
				u.chatStream = []byte(`{"error":{"message":"slow down","type":"rate_limit"}}`)
			},
			wantSurface:      "openai",
			wantModel:        "gpt-4o",
			wantStatus:       http.StatusTooManyRequests,
			wantClientStatus: http.StatusTooManyRequests,
			wantBody:         `{"error":{"message":"slow down","type":"rate_limit"}}`,
		},
		{
			name:    "gemini",
			path:    "/gemini/v1beta/models/gemini-1.5-pro:streamGenerateContent",
			request: `{"contents":[]}`,
			setup: func(u *qaUpstream) {
				u.geminiStreamStatus = http.StatusInternalServerError
				u.geminiStreamBody = []byte(`{"error":{"code":500,"message":"boom"}}`)
			},
			wantSurface: "gemini",
			wantModel:   "gemini-1.5-pro",
			wantStatus:  http.StatusInternalServerError,
			// PipeGemini copies raw bytes, so no re-framing.
			wantBody: `{"error":{"code":500,"message":"boom"}}`,
		},
		{
			// GLM goes through PipeAnthropicToOpenAI, the last distinct pipe.
			name:    "glm",
			path:    "/openai/v1/chat/completions",
			request: `{"model":"zai--glm-4.6","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			setup: func(u *qaUpstream) {
				u.messagesStatus = http.StatusTooManyRequests
				u.messagesBody = []byte(`{"type":"error","error":{"message":"slow down"}}`)
			},
			wantSurface: "openai",
			wantModel:   "zai--glm-4.6",
			wantStatus:  http.StatusTooManyRequests,
			// Pre-existing: PipeAnthropicToOpenAI only forwards data: lines, so a
			// non-SSE error body never reaches the client. What it does emit is the
			// synthesised finish chunk and [DONE].
			wantBody: `{"type":"error","error":{"message":"slow down"}}`,
		},
		{
			name:    "perplexity",
			path:    "/openai/v1/chat/completions",
			request: `{"model":"sonar-pro","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			setup: func(u *qaUpstream) {
				u.chatStreamStatus = http.StatusServiceUnavailable
				u.chatStream = []byte(`{"error":{"message":"overloaded","type":"server_error"}}`)
			},
			wantSurface: "openai",
			wantModel:   "sonar-pro",
			wantStatus:  http.StatusServiceUnavailable,
			wantBody:    `{"error":{"message":"overloaded","type":"server_error"}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			up := newQAUpstream(t)
			tc.setup(up)
			defer up.Close()
			proxy := qaProxy(t, up.URL)
			defer proxy.Close()

			status, body := qaPost(t, proxy.URL+tc.path, tc.request)
			wantClientStatus := tc.wantClientStatus
			if wantClientStatus == 0 {
				wantClientStatus = tc.wantStatus
			}
			if status != wantClientStatus {
				t.Fatalf("client status = %d, want %d: %s", status, wantClientStatus, body)
			}
			if string(body) != tc.wantBody {
				t.Errorf("client body = %q, want %q", body, tc.wantBody)
			}

			doc := qaGetStats(t, proxy.URL)
			if doc.Totals.Failures != 1 {
				t.Errorf("failures = %d, want 1", doc.Totals.Failures)
			}
			if doc.Totals.UsageMissing != 0 || doc.Totals.Uninstrumented != 0 {
				t.Errorf("usage_missing=%d uninstrumented=%d, want 0/0",
					doc.Totals.UsageMissing, doc.Totals.Uninstrumented)
			}

			metrics := qaGetMetrics(t, proxy.URL)
			wantLine := fmt.Sprintf(
				`sap_proxy_requests_total{surface="%s",model="%s",status="%d",stream="true"} 1`,
				tc.wantSurface, tc.wantModel, tc.wantStatus)
			if !strings.Contains(metrics, wantLine) {
				t.Errorf("/metrics is missing %q\n%s", wantLine, metrics)
			}
			if strings.Contains(metrics, `stream="true"} 1`) &&
				strings.Contains(metrics, fmt.Sprintf(`surface="%s",model="%s",status="200",stream="true"`, tc.wantSurface, tc.wantModel)) {
				t.Errorf("%s: the failed stream is still recorded as a 200\n%s", tc.name, metrics)
			}
		})
	}
}

// TestQA_UninstrumentedMetricIsExposed checks the new metric is actually
// scrapeable: a forgotten Observe call must be visible, not merely counted.
func TestQA_UninstrumentedMetricIsExposed(t *testing.T) {
	// The HTTP surface has no uninstrumented route left (that is the point of the
	// fix), so this asserts the exposition itself is present and parseable with a
	// zero value rather than pretending to drive a gap.
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	if status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyNonStreaming); status != 200 {
		t.Fatalf("seed request: %d %s", status, body)
	}

	text := qaGetMetrics(t, proxy.URL)
	samples := qaParseMetrics(t, text)
	want := `sap_proxy_uninstrumented_total{surface="anthropic",model="anthropic--claude-3.5-sonnet"} 0`
	if !strings.Contains(text, want) {
		t.Errorf("/metrics is missing %q\n%s", want, text)
	}
	if got := qaFindSamples(samples, "sap_proxy_uninstrumented_total", map[string]string{"surface": "anthropic"}); len(got) != 1 || got[0].value != 0 {
		t.Errorf("uninstrumented series = %+v, want exactly one with value 0", got)
	}
}

// TestQA_GeminiToOpenAIFakeSuccessIsDetected covers the cross-format Gemini pipe:
// it inspects every non-empty line, so a bare JSON error body inside a 200 stream
// is recognised as a failure.
func TestQA_GeminiToOpenAIFakeSuccessIsDetected(t *testing.T) {
	up := newQAUpstream(t)
	up.geminiStreamBody = []byte(`{"error":{"code":503,"message":"overloaded"}}`)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	const req = `{"model":"gemini-1.5-pro","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	if status, body := qaPost(t, proxy.URL+"/openai/v1/chat/completions", req); status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}

	doc := qaGetStats(t, proxy.URL)
	if doc.Totals.Failures != 1 {
		t.Errorf("failures = %d, want 1", doc.Totals.Failures)
	}
	if doc.Totals.Uninstrumented != 0 || doc.Totals.UsageMissing != 0 {
		t.Errorf("uninstrumented=%d usage_missing=%d, want 0/0", doc.Totals.Uninstrumented, doc.Totals.UsageMissing)
	}
	metrics := qaGetMetrics(t, proxy.URL)
	wantLine := `sap_proxy_requests_total{surface="openai",model="gemini-1.5-pro",status="502",stream="true"} 1`
	if !strings.Contains(metrics, wantLine) {
		t.Errorf("/metrics is missing %q\n%s", wantLine, metrics)
	}
}

// TestQA_ResidualGap_AnthropicToOpenAIFakeSuccess covered the last streaming pipe
// the F1 fix missed. PipeAnthropicToOpenAI (used by the GLM family and by the
// OpenAI-surface → Anthropic-upstream path) forwards only "data:" lines, so a 200
// response whose body is a bare JSON error was never inspected.
//
// R1 fixed it: non-data lines that start with "{" are now observed before being
// dropped. The drop itself is pre-existing behaviour and is pinned below — the
// client still receives only the synthesised finish chunk and [DONE].
func TestQA_ResidualGap_AnthropicToOpenAIFakeSuccess(t *testing.T) {
	up := newQAUpstream(t)
	up.messagesStatus = http.StatusOK
	up.messagesBody = []byte(`{"type":"error","error":{"type":"overloaded_error","message":"overloaded"}}`)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	const req = `{"model":"zai--glm-4.6","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	status, body := qaPost(t, proxy.URL+"/openai/v1/chat/completions", req)
	if status != http.StatusOK {
		t.Fatalf("client status = %d, want 200: %s", status, body)
	}
	// Pre-existing behaviour, unchanged: the non-SSE error body is not forwarded,
	// the client gets the synthesised finish chunk and [DONE].
	if !strings.Contains(string(body), `"error"`) || strings.Contains(string(body), `"finish_reason":"stop"`) || !strings.Contains(string(body), "[DONE]") {
		t.Errorf("upstream error was not surfaced: %s", body)
	}

	doc := qaGetStats(t, proxy.URL)
	if doc.Totals.Failures != 1 {
		t.Errorf("failures = %d, want 1: the error body is now inspected", doc.Totals.Failures)
	}
	if doc.Totals.Uninstrumented != 0 {
		t.Errorf("uninstrumented = %d, want 0: the pipe inspected the body", doc.Totals.Uninstrumented)
	}
	if doc.Totals.UsageMissing != 0 {
		t.Errorf("usage_missing = %d, want 0", doc.Totals.UsageMissing)
	}
	if doc.Totals.Tokens != (qaTokens{}) {
		t.Errorf("tokens = %+v, want zero", doc.Totals.Tokens)
	}

	metrics := qaGetMetrics(t, proxy.URL)
	if !strings.Contains(metrics, `model="zai--glm-4.6",status="502",stream="true"`) {
		t.Errorf("expected the fake success to be recorded as a 502\n%s", metrics)
	}
	if strings.Contains(metrics, `model="zai--glm-4.6",status="200",stream="true"`) {
		t.Errorf("the fake success is still recorded as a 200\n%s", metrics)
	}
}

// TestQA_ResidualGap_PrettyPrintedStreamError documents the other half of the
// bare-JSON detection heuristic: it inspects lines that *start* with "{", so a
// pretty-printed multi-line error body is seen as a lone "{" and never parsed.
//
// Current behaviour: failures=0, usage_missing=1 (the request counts as a
// success that reported nothing). Expected after a fix: failures=1, status=502.
// Real upstreams emit compact JSON, so the exposure is small — but a proxy in
// between that pretty-prints would hide the failure.
func TestQA_ResidualGap_PrettyPrintedStreamError(t *testing.T) {
	up := newQAUpstream(t)
	up.streamBody = []byte("{\n  \"type\": \"error\",\n  \"error\": {\"type\": \"overloaded_error\"}\n}")
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	if status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyStreaming); status != http.StatusOK {
		t.Fatalf("client status = %d, want 200: %s", status, body)
	}

	doc := qaGetStats(t, proxy.URL)
	if doc.Totals.Failures != 1 {
		t.Fatalf("failures = %d, want 1", doc.Totals.Failures)
	}
	if doc.Totals.UsageMissing != 0 {
		t.Errorf("usage_missing = %d, want 0", doc.Totals.UsageMissing)
	}
	t.Log("a multi-line error is reported as a failed stream")
}

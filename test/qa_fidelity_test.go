package test

import (
	"bytes"
	"testing"
)

// These tests are the highest-priority attack surface: the proxy is the entry
// point for Claude Code, so the accounting feature must not change a single
// forwarded byte in either direction.
//
// Note on request-body equality: the Anthropic handler is *not* a pass-through.
// It rebuilds the Bedrock body from an allow-list (dropping "model", adding
// "anthropic_version" and "max_tokens") and re-marshals it, which is pre-existing
// behaviour and unchanged by this feature. Asserting client-bytes == upstream
// bytes would therefore fail for reasons that have nothing to do with usage
// accounting. Instead each test asserts:
//
//  1. the exact expected upstream bytes (a hand-written literal, independent of
//     the implementation), so any added/removed/reordered byte is caught;
//  2. determinism: the same client request sent twice produces identical bytes;
//  3. the response bytes the client receives are byte-identical to the pinned
//     upstream payload, which *is* a strict pass-through guarantee.

const (
	// qaClientBodyNonStreaming is a canonical Anthropic Messages request. Its
	// inner objects are already compact and its content is a plain string, so the
	// handler's transforms are all identity operations on it.
	qaClientBodyNonStreaming = `{"anthropic_version":"bedrock-2023-05-31","max_tokens":64,` +
		`"messages":[{"content":"hi","role":"user"}],"metadata":{"user_id":"u1"},` +
		`"model":"claude-3-5-sonnet-latest"}`

	// qaWantUpstreamNonStreaming is what the allow-list + re-marshal must produce:
	// "model" dropped (not a Bedrock field), everything else sorted by key.
	qaWantUpstreamNonStreaming = `{"anthropic_version":"bedrock-2023-05-31","max_tokens":64,` +
		`"messages":[{"content":"hi","role":"user"}],"metadata":{"user_id":"u1"}}`

	// qaClientBodyStreaming differs only by stream:true. "stream" is not a Bedrock
	// field either, so the upstream bytes must be identical to the non-streaming
	// case: the flag selects the endpoint, it is not forwarded.
	qaClientBodyStreaming = `{"anthropic_version":"bedrock-2023-05-31","max_tokens":64,` +
		`"messages":[{"content":"hi","role":"user"}],"metadata":{"user_id":"u1"},` +
		`"model":"claude-3-5-sonnet-latest","stream":true}`
)

// TestQA_RequestBytes_NonStreamingAnthropic pins the exact bytes the upstream
// receives for a non-streaming native Anthropic request, and proves the same
// request twice produces the same bytes.
func TestQA_RequestBytes_NonStreamingAnthropic(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	for i := 0; i < 2; i++ {
		status, _ := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyNonStreaming)
		if status != 200 {
			t.Fatalf("attempt %d: expected 200, got %d", i, status)
		}
	}

	bodies := up.bodiesSnapshot()
	if len(bodies) != 2 {
		t.Fatalf("upstream received %d requests, want 2", len(bodies))
	}
	for i, got := range bodies {
		if !bytes.Equal(got, []byte(qaWantUpstreamNonStreaming)) {
			t.Fatalf("attempt %d: upstream body mismatch\nwant: %s\ngot:  %s", i, qaWantUpstreamNonStreaming, got)
		}
	}
	if !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("the same client request produced different upstream bytes:\nfirst:  %s\nsecond: %s", bodies[0], bodies[1])
	}
}

// TestQA_RequestBytes_StreamingAnthropic pins the streaming request bytes.
func TestQA_RequestBytes_StreamingAnthropic(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	for i := 0; i < 2; i++ {
		status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyStreaming)
		if status != 200 {
			t.Fatalf("attempt %d: expected 200, got %d: %s", i, status, body)
		}
	}

	bodies := up.bodiesSnapshot()
	if len(bodies) != 2 {
		t.Fatalf("upstream received %d requests, want 2", len(bodies))
	}
	for i, got := range bodies {
		if !bytes.Equal(got, []byte(qaWantUpstreamNonStreaming)) {
			t.Fatalf("attempt %d: upstream body mismatch\nwant: %s\ngot:  %s", i, qaWantUpstreamNonStreaming, got)
		}
	}
	if !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("the same streaming request produced different upstream bytes:\nfirst:  %s\nsecond: %s", bodies[0], bodies[1])
	}
}

// TestQA_ResponseBytes_NonStreamingAnthropic asserts the client receives the
// upstream body verbatim, byte for byte.
func TestQA_ResponseBytes_NonStreamingAnthropic(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyNonStreaming)
	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}
	if !bytes.Equal(body, []byte(qaAnthropicBody)) {
		t.Fatalf("client body differs from the upstream body\nwant: %s\ngot:  %s", qaAnthropicBody, body)
	}
}

// TestQA_StreamBytes_PipeAnthropic asserts the SSE stream reaches the client
// byte for byte when the upstream already sends event: lines.
func TestQA_StreamBytes_PipeAnthropic(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyStreaming)
	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}
	if !bytes.Equal(body, []byte(qaAnthropicSSE)) {
		t.Fatalf("stream bytes differ from the upstream bytes\nwant: %q\ngot:  %q", qaAnthropicSSE, body)
	}
}

// TestQA_StreamBytes_PipeAnthropicInjectsEventLines pins the *pre-existing*
// behaviour of PipeAnthropic: when the upstream omits event: lines it injects
// them. The accounting feature must not alter that, so the assertion is against
// the exact documented output rather than against the raw upstream bytes.
func TestQA_StreamBytes_PipeAnthropicInjectsEventLines(t *testing.T) {
	up := newQAUpstream(t)
	up.streamBody = []byte(qaAnthropicSSENoEvent)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyStreaming)
	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}
	if !bytes.Equal(body, []byte(qaAnthropicSSENoEventExpected)) {
		t.Fatalf("injected stream bytes differ from the documented behaviour\nwant: %q\ngot:  %q", qaAnthropicSSENoEventExpected, body)
	}
}

// TestQA_StreamBytes_PipeOpenAI asserts the OpenAI SSE stream reaches the client
// byte for byte.
func TestQA_StreamBytes_PipeOpenAI(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	const req = `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	status, body := qaPost(t, proxy.URL+"/openai/v1/chat/completions", req)
	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}
	if !bytes.Equal(body, []byte(qaOpenAISSE)) {
		t.Fatalf("stream bytes differ from the upstream bytes\nwant: %q\ngot:  %q", qaOpenAISSE, body)
	}
}

package test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// This file started as a set of characterization tests for the gaps the QA pass
// found in P0. All four gaps are now fixed, so the assertions below describe the
// *fixed* behaviour; the original observation is kept in each GAP note so the
// history stays readable.
//
// What is still pinned as unchanged: the client-visible bytes and status code.
// The proxy deliberately keeps answering 200 to a stream it has already started,
// because changing that would alter Claude Code's error path.

// GAP 1 (fixed): a streaming request whose upstream fails used to be recorded as
// a successful 200 with usage_missing=1, because the handlers call c.Status(200)
// before piping and nothing recorded what the upstream actually said.
//
// Now the response path records the upstream status (and a failure reported
// inside an already-200 stream), so the counters see a 502 while the client still
// receives the same 200 and the same bytes.
func TestQA_Characterization_StreamingUpstreamErrorLooksLikeSuccess(t *testing.T) {
	up := newQAUpstream(t)
	up.streamBody = []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"boom"}}`)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyStreaming)
	if status != http.StatusOK {
		t.Fatalf("client status = %d, want 200: the wire status must stay unchanged: %s", status, body)
	}
	// Fidelity: the client still receives exactly what the upstream sent. The
	// trailing newline is pre-existing pipe behaviour (PipeAnthropic re-frames
	// every line with "\n"), not something this feature introduced.
	if want := string(up.streamBody) + "\n"; string(body) != want {
		t.Errorf("client body = %q, want %q", body, want)
	}

	doc := qaGetStats(t, proxy.URL)
	if doc.Totals.Requests != 1 {
		t.Errorf("requests = %d, want 1", doc.Totals.Requests)
	}
	if doc.Totals.Failures != 1 {
		t.Errorf("failures = %d, want 1: an error inside the stream is a failure", doc.Totals.Failures)
	}
	if doc.Totals.UsageMissing != 0 {
		t.Errorf("usage_missing = %d, want 0: a failed request is not missing usage", doc.Totals.UsageMissing)
	}
	if doc.Totals.Uninstrumented != 0 {
		t.Errorf("uninstrumented = %d, want 0", doc.Totals.Uninstrumented)
	}

	metrics := qaGetMetrics(t, proxy.URL)
	if !strings.Contains(metrics, `status="502",stream="true"`) {
		t.Errorf("expected the failed stream to be recorded as 502\n%s", metrics)
	}
	if strings.Contains(metrics, `status="200",stream="true"`) {
		t.Errorf("the failed stream is still recorded as a 200\n%s", metrics)
	}
}

// GAP 2 (fixed): POST /openai/v1/responses is a real inference call returning
// usage{input_tokens,output_tokens}, but nothing read it and the model label was
// never set, so its tokens were lost under model="unknown".
//
// Now the handler sets the model label and observes the response, so the tokens
// are attributed to gpt-4o and the request is not counted as missing usage.
func TestQA_Characterization_ResponsesSurfaceUsageLost(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	status, body := qaPost(t, proxy.URL+"/openai/v1/responses", `{"model":"gpt-4o","input":"hi"}`)
	if status != http.StatusOK {
		t.Fatalf("POST /openai/v1/responses: expected 200, got %d: %s", status, body)
	}
	// The upstream body carries usage; the client gets it verbatim.
	if !bytes.Contains(body, []byte(`"usage"`)) {
		t.Fatalf("upstream body without usage, fixture is wrong: %s", body)
	}

	doc := qaGetStats(t, proxy.URL)
	// The fixture reports input_tokens_details.cached_tokens:5, which is the
	// Responses API spelling of the cached prompt count.
	want := qaTokens{Input: 30, Output: 7, CacheRead: 5}
	if doc.Totals.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", doc.Totals.Tokens, want)
	}
	if doc.Totals.UsageMissing != 0 {
		t.Errorf("usage_missing = %d, want 0", doc.Totals.UsageMissing)
	}
	if doc.Totals.Uninstrumented != 0 {
		t.Errorf("uninstrumented = %d, want 0", doc.Totals.Uninstrumented)
	}
	model := qaModelOf(t, doc)
	if model.Model != "gpt-4o" {
		t.Errorf("model label = %q, want %q", model.Model, "gpt-4o")
	}
}

// GAP 3 (fixed): the embeddings handlers never observed the response, so a call
// that reported prompt_tokens:7 was counted as usage_missing with zero tokens.
//
// Now both embeddings endpoints observe the body, so the prompt tokens are
// accounted for.
func TestQA_Characterization_EmbeddingsUsageLost(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	status, body := qaPost(t, proxy.URL+"/openai/v1/embeddings", `{"model":"gpt-4o","input":"hi"}`)
	if status != http.StatusOK {
		t.Fatalf("POST /openai/v1/embeddings: expected 200, got %d: %s", status, body)
	}

	doc := qaGetStats(t, proxy.URL)
	if doc.Totals.Tokens.Input != 7 {
		t.Errorf("input = %d, want 7", doc.Totals.Tokens.Input)
	}
	if doc.Totals.UsageMissing != 0 {
		t.Errorf("usage_missing = %d, want 0", doc.Totals.UsageMissing)
	}
	if doc.Totals.Uninstrumented != 0 {
		t.Errorf("uninstrumented = %d, want 0", doc.Totals.Uninstrumented)
	}
}

// GAP 4 (fixed, with a documented limitation): the Gemini stream capture cap.
// A 600KB stream is forwarded intact (the fidelity guarantee holds) but the
// capture is dropped, so the token counts are unknown.
//
// The limitation stands — a buffered capture cannot cover arbitrarily large
// streams — but the loss is now attributed: the request still counts as
// usage_missing (the number really is unknown) *and* increments
// usage_capture_dropped, so P1 can tell "we dropped it" from "the upstream never
// sent it".
func TestQA_Characterization_GeminiStreamCaptureCapDropsUsage(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	// Build a chunked-JSON body well past the 512KB capture limit that still ends
	// with a usageMetadata block.
	pad := strings.Repeat("x", 4096)
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < 150; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"candidates":[{"content":{"parts":[{"text":"` + pad + `"}]}}]}`)
	}
	b.WriteString(`,{"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":6}}]`)
	up.geminiStreamBody = []byte(b.String())

	status, body := qaPost(t, proxy.URL+"/gemini/v1beta/models/gemini-1.5-pro:streamGenerateContent", `{"contents":[]}`)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}
	// Fidelity must hold regardless of the cap.
	if len(body) != len(up.geminiStreamBody) {
		t.Errorf("forwarded %d bytes, want all %d", len(body), len(up.geminiStreamBody))
	}

	doc := qaGetStats(t, proxy.URL)
	if doc.Totals.Tokens != (qaTokens{}) {
		t.Errorf("tokens = %+v, want zero: the capture was dropped", doc.Totals.Tokens)
	}
	if doc.Totals.UsageMissing != 1 {
		t.Errorf("usage_missing = %d, want 1: the numbers really are unknown", doc.Totals.UsageMissing)
	}
	if doc.Totals.UsageCaptureDropped != 1 {
		t.Errorf("usage_capture_dropped = %d, want 1: the loss must be attributed to the cap", doc.Totals.UsageCaptureDropped)
	}
	if doc.Totals.Uninstrumented != 0 {
		t.Errorf("uninstrumented = %d, want 0: the stream was instrumented", doc.Totals.Uninstrumented)
	}

	metrics := qaGetMetrics(t, proxy.URL)
	wantLine := `sap_proxy_usage_capture_dropped_total{surface="gemini",model="gemini-1.5-pro"} 1`
	if !strings.Contains(metrics, wantLine) {
		t.Errorf("/metrics is missing %q\n%s", wantLine, metrics)
	}
	t.Logf("LIMITATION: a %d-byte Gemini stream is forwarded intact; its usage is dropped by the capture cap and attributed", len(body))
}

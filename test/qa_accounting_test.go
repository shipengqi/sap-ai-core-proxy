package test

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// qaAnthropicSSEDeltaRepeatsInput is a stream whose message_delta repeats the
// input-side counters. Some upstreams do this with a running total; applying it
// again would double the prompt.
const qaAnthropicSSEDeltaRepeatsInput = "event: message_start\n" +
	`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":4,"cache_creation_input_tokens":2}}}` + "\n" +
	"\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","usage":{"input_tokens":10,"cache_read_input_tokens":4,"cache_creation_input_tokens":2,"output_tokens":12}}` + "\n" +
	"\n"

// TestQA_AnthropicCumulativeOutputNotSummed covers the real Anthropic shape: two
// message_delta events carry cumulative output_tokens (5 then 12), so the total
// must be 12, not 17, and the prompt must not be counted twice.
func TestQA_AnthropicCumulativeOutputNotSummed(t *testing.T) {
	up := newQAUpstream(t) // qaAnthropicSSE already has message_delta 5 then 12
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyStreaming)
	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}

	doc := qaGetStats(t, proxy.URL)
	want := qaTokens{Input: 10, Output: 12, CacheRead: 4, CacheWrite: 2}
	if doc.Totals.Tokens != want {
		t.Errorf("tokens = %+v, want %+v (output must be the last cumulative value, not the sum)", doc.Totals.Tokens, want)
	}
	if doc.Totals.Requests != 1 {
		t.Errorf("requests = %d, want 1", doc.Totals.Requests)
	}
	if doc.Totals.UsageMissing != 0 {
		t.Errorf("usage_missing = %d, want 0", doc.Totals.UsageMissing)
	}

	metrics := qaGetMetrics(t, proxy.URL)
	wantLine := `sap_proxy_output_tokens_total{surface="anthropic",model="anthropic--claude-3.5-sonnet"} 12`
	if !strings.Contains(metrics, wantLine) {
		t.Errorf("/metrics is missing %q\n%s", wantLine, metrics)
	}
	// The stream split must be recorded as streaming.
	wantStream := `sap_proxy_requests_total{surface="anthropic",model="anthropic--claude-3.5-sonnet",status="200",stream="true"} 1`
	if !strings.Contains(metrics, wantStream) {
		t.Errorf("/metrics is missing %q\n%s", wantStream, metrics)
	}
}

// TestQA_AnthropicDeltaDoesNotRepeatInput covers message_delta carrying input
// counters: they must neither overwrite nor add to the message_start values.
func TestQA_AnthropicDeltaDoesNotRepeatInput(t *testing.T) {
	up := newQAUpstream(t)
	up.streamBody = []byte(qaAnthropicSSEDeltaRepeatsInput)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyStreaming)
	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}

	doc := qaGetStats(t, proxy.URL)
	want := qaTokens{Input: 10, Output: 12, CacheRead: 4, CacheWrite: 2}
	if doc.Totals.Tokens != want {
		t.Errorf("tokens = %+v, want %+v (a repeated message_delta must not double the prompt)", doc.Totals.Tokens, want)
	}
}

// TestQA_Upstream400 covers the failure path: a 4xx counts as a request and a
// failure, but never as missing usage, and contributes no tokens.
func TestQA_Upstream400(t *testing.T) {
	up := newQAUpstream(t)
	up.invokeStatus = http.StatusBadRequest
	up.invokeBody = []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens is required"}}`)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyNonStreaming)
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 to be forwarded, got %d: %s", status, body)
	}
	if string(body) != string(up.invokeBody) {
		t.Errorf("error body was altered\nwant: %s\ngot:  %s", up.invokeBody, body)
	}

	doc := qaGetStats(t, proxy.URL)
	if doc.Totals.Requests != 1 {
		t.Errorf("requests = %d, want 1", doc.Totals.Requests)
	}
	if doc.Totals.Failures != 1 {
		t.Errorf("failures = %d, want 1", doc.Totals.Failures)
	}
	if doc.Totals.UsageMissing != 0 {
		t.Errorf("usage_missing = %d, want 0 (a 4xx has no usage by definition)", doc.Totals.UsageMissing)
	}
	if doc.Totals.Tokens != (qaTokens{}) {
		t.Errorf("tokens = %+v, want all zero", doc.Totals.Tokens)
	}

	metrics := qaGetMetrics(t, proxy.URL)
	wantLine := `sap_proxy_requests_total{surface="anthropic",model="anthropic--claude-3.5-sonnet",status="400",stream="false"} 1`
	if !strings.Contains(metrics, wantLine) {
		t.Errorf("/metrics is missing %q\n%s", wantLine, metrics)
	}
	if strings.Contains(metrics, `status="200"`) {
		t.Errorf("/metrics reports a 200 for a failed request\n%s", metrics)
	}
}

// TestQA_NonInferenceRequestsDoNotPollute hammers every non-inference endpoint
// and asserts the counters are untouched.
func TestQA_NonInferenceRequestsDoNotPollute(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	before := qaGetStats(t, proxy.URL)

	type call struct {
		method string
		path   string
	}
	for _, c := range []call{
		{http.MethodGet, "/openai/v1/models"},
		{http.MethodHead, "/anthropic"},
		{http.MethodGet, "/health"},
		{http.MethodGet, "/metrics"},
		{http.MethodGet, "/stats"},
	} {
		req, err := http.NewRequest(c.method, proxy.URL+c.path, nil)
		if err != nil {
			t.Fatalf("build %s %s: %v", c.method, c.path, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s %s: expected 200, got %d", c.method, c.path, resp.StatusCode)
		}
	}

	after := qaGetStats(t, proxy.URL)

	if after.Totals.Requests != before.Totals.Requests {
		t.Errorf("requests changed from %d to %d on non-inference endpoints", before.Totals.Requests, after.Totals.Requests)
	}
	if after.Totals.Tokens != before.Totals.Tokens {
		t.Errorf("tokens changed from %+v to %+v on non-inference endpoints", before.Totals.Tokens, after.Totals.Tokens)
	}
	if after.Totals.UsageMissing != before.Totals.UsageMissing {
		t.Errorf("usage_missing changed from %d to %d on non-inference endpoints", before.Totals.UsageMissing, after.Totals.UsageMissing)
	}
	if len(after.ByModel) != len(before.ByModel) {
		t.Errorf("by_model grew from %d to %d entries on non-inference endpoints", len(before.ByModel), len(after.ByModel))
	}

	metrics := qaGetMetrics(t, proxy.URL)
	if strings.Contains(metrics, `model="unknown"`) {
		t.Errorf("a non-inference request created an unknown-model series\n%s", metrics)
	}
}

// TestQA_ConcurrentRequestsExactCount fires 50 requests at the same model and
// asserts the counters are exact: no lost updates, no double counting.
func TestQA_ConcurrentRequestsExactCount(t *testing.T) {
	const n = 50

	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: n}}
	defer client.CloseIdleConnections()

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := client.Post(proxy.URL+"/anthropic/v1/messages", "application/json",
				strings.NewReader(qaClientBodyNonStreaming))
			if err != nil {
				errs <- fmt.Errorf("request %d: %w", i, err)
				return
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				errs <- fmt.Errorf("request %d: status %d", i, resp.StatusCode)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent request failed: %v", err)
	}

	doc := qaGetStats(t, proxy.URL)
	if doc.Totals.Requests != n {
		t.Errorf("requests = %d, want %d", doc.Totals.Requests, n)
	}
	// The upstream reports input 10 / output 8 / cache_read 4 / cache_write 2.
	want := qaTokens{
		Input:      10 * n,
		Output:     8 * n,
		CacheRead:  4 * n,
		CacheWrite: 2 * n,
	}
	if doc.Totals.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", doc.Totals.Tokens, want)
	}

	metrics := qaGetMetrics(t, proxy.URL)
	wantLine := fmt.Sprintf(`sap_proxy_requests_total{surface="anthropic",model="anthropic--claude-3.5-sonnet",status="200",stream="false"} %d`, n)
	if !strings.Contains(metrics, wantLine) {
		t.Errorf("/metrics is missing %q\n%s", wantLine, metrics)
	}
	if got := len(up.bodiesSnapshot()); got != n {
		t.Errorf("upstream received %d requests, want %d", got, n)
	}
}

// TestQA_UsageMissingCountedOnce is the positive case for the metric that will
// drive the P1 decision: a successful response whose upstream reported no usage
// must increment usage_missing exactly once.
func TestQA_UsageMissingCountedOnce(t *testing.T) {
	up := newQAUpstream(t)
	up.invokeBody = []byte(`{"id":"msg_no_usage","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}]}`)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyNonStreaming)
	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}

	doc := qaGetStats(t, proxy.URL)
	if doc.Totals.Requests != 1 {
		t.Errorf("requests = %d, want 1", doc.Totals.Requests)
	}
	if doc.Totals.Failures != 0 {
		t.Errorf("failures = %d, want 0", doc.Totals.Failures)
	}
	if doc.Totals.UsageMissing != 1 {
		t.Errorf("usage_missing = %d, want 1", doc.Totals.UsageMissing)
	}
	if doc.Totals.Tokens != (qaTokens{}) {
		t.Errorf("tokens = %+v, want all zero", doc.Totals.Tokens)
	}

	metrics := qaGetMetrics(t, proxy.URL)
	wantLine := `sap_proxy_usage_missing_total{surface="anthropic",model="anthropic--claude-3.5-sonnet"} 1`
	if !strings.Contains(metrics, wantLine) {
		t.Errorf("/metrics is missing %q\n%s", wantLine, metrics)
	}
}

// TestQA_StatsMatchesMetrics checks the two endpoints cannot disagree: every

// by_model token total must equal the matching Prometheus series.
func TestQA_StatsMatchesMetrics(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	if status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyNonStreaming); status != 200 {
		t.Fatalf("non-streaming request failed: %d %s", status, body)
	}
	if status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyStreaming); status != 200 {
		t.Fatalf("streaming request failed: %d %s", status, body)
	}

	doc := qaGetStats(t, proxy.URL)
	model := qaModelOf(t, doc)
	if model.Model != "anthropic--claude-3.5-sonnet" {
		t.Fatalf("model label = %q", model.Model)
	}
	// 10 + 10 input, 8 + 12 output, 4 + 4 cache read, 2 + 2 cache write.
	want := qaTokens{Input: 20, Output: 20, CacheRead: 8, CacheWrite: 4}
	if model.Tokens != want {
		t.Errorf("by_model tokens = %+v, want %+v", model.Tokens, want)
	}
	if !reflect.DeepEqual(model.Tokens, doc.Totals.Tokens) {
		t.Errorf("by_model tokens %+v disagree with totals %+v", model.Tokens, doc.Totals.Tokens)
	}

	metrics := qaGetMetrics(t, proxy.URL)
	for _, wantLine := range []string{
		`sap_proxy_input_tokens_total{surface="anthropic",model="anthropic--claude-3.5-sonnet"} 20`,
		`sap_proxy_output_tokens_total{surface="anthropic",model="anthropic--claude-3.5-sonnet"} 20`,
		`sap_proxy_cache_read_tokens_total{surface="anthropic",model="anthropic--claude-3.5-sonnet"} 8`,
		`sap_proxy_cache_write_tokens_total{surface="anthropic",model="anthropic--claude-3.5-sonnet"} 4`,
		`sap_proxy_request_duration_seconds_count{surface="anthropic"} 2`,
	} {
		if !strings.Contains(metrics, wantLine) {
			t.Errorf("/metrics is missing %q\n%s", wantLine, metrics)
		}
	}
}

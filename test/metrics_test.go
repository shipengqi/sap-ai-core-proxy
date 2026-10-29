package test

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"

	collectorv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"

	"github.com/shipengqi/sap-ai-core-proxy/test/testutil"
)

// metricsAggregate mirrors the JSON shape of GET /stats.
type metricsAggregate struct {
	Requests     int64 `json:"requests"`
	Failures     int64 `json:"failures"`
	UsageMissing int64 `json:"usage_missing"`
	Tokens       struct {
		Input      int64 `json:"input_tokens"`
		Output     int64 `json:"output_tokens"`
		CacheRead  int64 `json:"cache_read_input_tokens"`
		CacheWrite int64 `json:"cache_creation_input_tokens"`
	} `json:"tokens"`
	CacheHitRatio float64 `json:"cache_hit_ratio"`
	// Uninstrumented counts successful requests that no observation path read.
	Uninstrumented int64 `json:"uninstrumented"`
	// UsageCaptureDropped counts requests whose usage the proxy discarded.
	UsageCaptureDropped int64 `json:"usage_capture_dropped"`
}

type metricsBreakdown struct {
	Surface string `json:"surface"`
	Model   string `json:"model"`
	metricsAggregate
}

type metricsStats struct {
	Since         string             `json:"since"`
	UptimeSeconds float64            `json:"uptime_seconds"`
	Totals        metricsAggregate   `json:"totals"`
	BySurface     []metricsBreakdown `json:"by_surface"`
	ByModel       []metricsBreakdown `json:"by_model"`
}

// setupMetrics starts the proxy against the mock server. The real-credentials
// mode is skipped: this test asserts exact token counts, which only the mock can
// guarantee (and it must not burn quota).
func setupMetrics(t *testing.T) (proxyURL string, cleanup func()) {
	t.Helper()
	if _, _, _, _, hasReal := testutil.RealCreds(); hasReal {
		t.Skip("skipping mock-only usage accounting test in real mode")
	}
	mock := testutil.NewMockSAPServer(t)
	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start mock proxy: %v", err)
	}
	return proxy.URL, func() {
		proxy.Close()
		mock.Close()
	}
}

func getStats(t *testing.T, proxyURL string) metricsStats {
	t.Helper()
	resp, err := http.Get(proxyURL + "/stats")
	if err != nil {
		t.Fatalf("GET /stats: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /stats: expected 200, got %d: %s", resp.StatusCode, body)
	}

	var stats metricsStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		t.Fatalf("decode /stats: %v", err)
	}
	return stats
}

func getMetrics(t *testing.T, proxyURL string) string {
	return getMetricsAt(t, proxyURL, "/metrics")
}

func getMetricsAt(t *testing.T, proxyURL, path string) string {
	t.Helper()
	resp, err := http.Get(proxyURL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: expected 200, got %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

// TestMetrics_OTLPReceiver accepts a standard OTLP HTTP/protobuf metrics export
// and acknowledges it without mixing arbitrary agent telemetry into /metrics.
func TestMetrics_OTLPReceiver(t *testing.T) {
	proxyURL, cleanup := setupMetrics(t)
	defer cleanup()

	request := &collectorv1.ExportMetricsServiceRequest{ResourceMetrics: []*metricsv1.ResourceMetrics{{
		Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{{
			Key: "service.name", Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: "claude-code"}},
		}}},
		ScopeMetrics: []*metricsv1.ScopeMetrics{{Metrics: []*metricsv1.Metric{{
			Name: "claude.tokens", Description: "Tokens exported by Claude Code",
			Data: &metricsv1.Metric_Sum{Sum: &metricsv1.Sum{
				AggregationTemporality: metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				IsMonotonic:            true,
				DataPoints:             []*metricsv1.NumberDataPoint{{Value: &metricsv1.NumberDataPoint_AsInt{AsInt: 42}}},
			}},
		}}}},
	}}}
	body, err := proto.Marshal(request)
	if err != nil {
		t.Fatalf("marshal OTLP request: %v", err)
	}
	resp, err := http.Post(proxyURL+"/otel/v1/metrics", "application/x-protobuf", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST OTLP metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST OTLP metrics: expected 200, got %d", resp.StatusCode)
	}
	if got := getMetrics(t, proxyURL); strings.Contains(got, "claude_tokens") || strings.Contains(got, "claude-code") {
		t.Errorf("/metrics must not expose discarded OTLP telemetry\n%s", got)
	}
}

func TestMetrics_OTLPReceiverRejectsGET(t *testing.T) {
	proxyURL, cleanup := setupMetrics(t)
	defer cleanup()

	resp, err := http.Get(proxyURL + "/otel/v1/metrics")
	if err != nil {
		t.Fatalf("GET OTLP metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /otel/v1/metrics status = %d, want 404", resp.StatusCode)
	}
}

// TestMetrics_AnthropicUsageAccounting drives the native Anthropic surface both
// streaming and non-streaming and checks that the numbers the proxy reports match
// what the mock upstream reported.
func TestMetrics_AnthropicUsageAccounting(t *testing.T) {
	proxyURL, cleanup := setupMetrics(t)
	defer cleanup()

	// Non-streaming: the mock answers usage {input 10, output 8, cache_read 4,
	// cache_write 2}.
	nonStreaming := `{"model":"claude-3-5-sonnet-latest","max_tokens":100,"messages":[{"role":"user","content":"Say hi"}]}`
	resp, err := http.Post(proxyURL+"/anthropic/v1/messages", "application/json", strings.NewReader(nonStreaming))
	if err != nil {
		t.Fatalf("POST /anthropic/v1/messages: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("non-streaming request: expected 200, got %d", resp.StatusCode)
	}

	// Streaming: message_start reports {input 10, cache_read 4, cache_write 2},
	// message_delta reports output 3.
	streaming := `{"model":"claude-3-5-sonnet-latest","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"Say hi"}]}`
	resp, err = http.Post(proxyURL+"/anthropic/v1/messages", "application/json", strings.NewReader(streaming))
	if err != nil {
		t.Fatalf("POST /anthropic/v1/messages (stream): %v", err)
	}
	streamBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read stream body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("streaming request: expected 200, got %d: %s", resp.StatusCode, streamBody)
	}
	// The stream itself must be untouched by the accounting.
	if !strings.Contains(string(streamBody), "event: message_start") ||
		!strings.Contains(string(streamBody), "message_stop") {
		t.Fatalf("stream body was altered: %q", streamBody)
	}

	stats := getStats(t, proxyURL)

	if stats.Totals.Requests != 2 {
		t.Errorf("total requests = %d, want 2", stats.Totals.Requests)
	}
	if stats.Totals.Failures != 0 {
		t.Errorf("total failures = %d, want 0", stats.Totals.Failures)
	}
	if stats.Totals.UsageMissing != 0 {
		t.Errorf("total usage_missing = %d, want 0", stats.Totals.UsageMissing)
	}

	wantTokens := metricsAggregate{}.Tokens
	wantTokens.Input = 20     // 10 + 10
	wantTokens.Output = 11    // 8 + 3
	wantTokens.CacheRead = 8  // 4 + 4
	wantTokens.CacheWrite = 4 // 2 + 2
	if stats.Totals.Tokens != wantTokens {
		t.Errorf("total tokens = %+v, want %+v", stats.Totals.Tokens, wantTokens)
	}

	// 8 cache reads over 20 + 8 + 4 prompt tokens.
	if want := 8.0 / 32.0; math.Abs(stats.Totals.CacheHitRatio-want) > 1e-9 {
		t.Errorf("cache_hit_ratio = %v, want %v", stats.Totals.CacheHitRatio, want)
	}

	if len(stats.BySurface) != 1 || stats.BySurface[0].Surface != "anthropic" {
		t.Fatalf("unexpected by_surface: %+v", stats.BySurface)
	}
	if len(stats.ByModel) != 1 {
		t.Fatalf("unexpected by_model: %+v", stats.ByModel)
	}
	if got := stats.ByModel[0].Model; got != "anthropic--claude-3.5-sonnet" {
		t.Errorf("model label = %q, want the normalised SAP deployment name", got)
	}

	metrics := getMetrics(t, proxyURL)
	for _, want := range []string{
		`sap_proxy_requests_total{surface="anthropic",model="anthropic--claude-3.5-sonnet",status="200",stream="false"} 1`,
		`sap_proxy_requests_total{surface="anthropic",model="anthropic--claude-3.5-sonnet",status="200",stream="true"} 1`,
		`sap_proxy_input_tokens_total{surface="anthropic",model="anthropic--claude-3.5-sonnet"} 20`,
		`sap_proxy_output_tokens_total{surface="anthropic",model="anthropic--claude-3.5-sonnet"} 11`,
		`sap_proxy_cache_read_tokens_total{surface="anthropic",model="anthropic--claude-3.5-sonnet"} 8`,
		`sap_proxy_cache_write_tokens_total{surface="anthropic",model="anthropic--claude-3.5-sonnet"} 4`,
		`sap_proxy_usage_missing_total{surface="anthropic",model="anthropic--claude-3.5-sonnet"} 0`,
		`sap_proxy_request_duration_seconds_count{surface="anthropic"} 2`,
	} {
		if !strings.Contains(metrics, want) {
			t.Errorf("/metrics is missing %q\n---\n%s", want, metrics)
		}
	}
}

// TestMetrics_GeminiStreamUsage covers the Gemini native streaming path, whose
// usage has to be recovered from a buffered chunked-JSON body rather than from
// per-line SSE frames.
func TestMetrics_GeminiStreamUsage(t *testing.T) {
	proxyURL, cleanup := setupMetrics(t)
	defer cleanup()

	reqBody := `{"contents":[{"role":"user","parts":[{"text":"Say hi"}]}]}`
	resp, err := http.Post(
		proxyURL+"/gemini/v1beta/models/gemini-1.5-pro:streamGenerateContent",
		"application/json",
		strings.NewReader(reqBody),
	)
	if err != nil {
		t.Fatalf("POST streamGenerateContent: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "usageMetadata") {
		t.Fatalf("the stream body must be forwarded unchanged, got: %q", body)
	}

	stats := getStats(t, proxyURL)
	if stats.Totals.Requests != 1 {
		t.Errorf("total requests = %d, want 1", stats.Totals.Requests)
	}
	// The mock reports promptTokenCount 12 and candidatesTokenCount 6.
	if stats.Totals.Tokens.Input != 12 || stats.Totals.Tokens.Output != 6 {
		t.Errorf("gemini stream tokens = %+v, want input 12 / output 6", stats.Totals.Tokens)
	}
	if len(stats.BySurface) != 1 || stats.BySurface[0].Surface != "gemini" {
		t.Fatalf("unexpected by_surface: %+v", stats.BySurface)
	}
}

// TestMetrics_StreamingUpstreamErrorIsRecordedAsFailure is the F1 regression
// test: the handlers call c.Status(200) before piping, so the writer status can
// never show a failed stream. The recorded status must come from the upstream.
//
// The client-visible status stays 200 on purpose — changing it would change
// pre-existing behaviour that Claude Code depends on.
func TestMetrics_StreamingUpstreamErrorIsRecordedAsFailure(t *testing.T) {
	if _, _, _, _, hasReal := testutil.RealCreds(); hasReal {
		t.Skip("skipping mock-only usage accounting test in real mode")
	}
	mock := testutil.NewMockSAPServer(t)
	mock.StreamingErrorStatus = http.StatusTooManyRequests
	defer mock.Close()
	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer proxy.Close()

	reqBody := `{"model":"claude-3-5-sonnet-latest","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	resp, err := http.Post(proxy.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /anthropic/v1/messages: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("client status = %d, want upstream 429: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "mock upstream streaming failure") {
		t.Errorf("the upstream error body must still be forwarded, got: %q", body)
	}

	stats := getStats(t, proxy.URL)
	if stats.Totals.Requests != 1 {
		t.Errorf("requests = %d, want 1", stats.Totals.Requests)
	}
	if stats.Totals.Failures != 1 {
		t.Errorf("failures = %d, want 1: the upstream answered %d", stats.Totals.Failures, http.StatusTooManyRequests)
	}
	if stats.Totals.UsageMissing != 0 {
		t.Errorf("usage_missing = %d, want 0: a failed request is not missing usage", stats.Totals.UsageMissing)
	}
	if stats.Totals.Uninstrumented != 0 {
		t.Errorf("uninstrumented = %d, want 0", stats.Totals.Uninstrumented)
	}

	metrics := getMetrics(t, proxy.URL)
	want := `sap_proxy_requests_total{surface="anthropic",model="anthropic--claude-3.5-sonnet",status="429",stream="true"} 1`
	if !strings.Contains(metrics, want) {
		t.Errorf("/metrics is missing %q\n%s", want, metrics)
	}
	if strings.Contains(metrics, `stream="true"} 1`) && strings.Contains(metrics, `status="200",stream="true"`) {
		t.Errorf("/metrics records the failed stream as a 200\n%s", metrics)
	}
}

// TestMetrics_NoUsageExpectedEndpoints covers F3: embeddings bill prompt tokens
// and must be observed, while image generation has no token concept and must be
// excluded from both usage_missing and uninstrumented.
func TestMetrics_NoUsageExpectedEndpoints(t *testing.T) {
	proxyURL, cleanup := setupMetrics(t)
	defer cleanup()

	resp, err := http.Post(proxyURL+"/openai/v1/embeddings", "application/json",
		strings.NewReader(`{"model":"gpt-4o","input":"hi"}`))
	if err != nil {
		t.Fatalf("POST /openai/v1/embeddings: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("embeddings: expected 200, got %d", resp.StatusCode)
	}

	resp, err = http.Post(proxyURL+"/openai/v1/images/generations", "application/json",
		strings.NewReader(`{"model":"gpt-4o","prompt":"a cat","n":1}`))
	if err != nil {
		t.Fatalf("POST /openai/v1/images/generations: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("images: expected 200, got %d", resp.StatusCode)
	}

	stats := getStats(t, proxyURL)
	if stats.Totals.Requests != 2 {
		t.Errorf("requests = %d, want 2", stats.Totals.Requests)
	}
	// The mock embeddings endpoint reports prompt_tokens 7.
	if stats.Totals.Tokens.Input != 7 {
		t.Errorf("input tokens = %d, want 7 from the embeddings call", stats.Totals.Tokens.Input)
	}
	if stats.Totals.UsageMissing != 0 {
		t.Errorf("usage_missing = %d, want 0", stats.Totals.UsageMissing)
	}
	if stats.Totals.Uninstrumented != 0 {
		t.Errorf("uninstrumented = %d, want 0: both endpoints declare their accounting", stats.Totals.Uninstrumented)
	}

	metrics := getMetrics(t, proxyURL)
	if !strings.Contains(metrics, `sap_proxy_input_tokens_total{surface="openai",model="gpt-4o"} 7`) {
		t.Errorf("/metrics is missing the embeddings prompt tokens\n%s", metrics)
	}
}

// TestMetrics_ResponsesAPIShape covers F2: /openai/v1/responses is a real
// inference call that reports input_tokens/output_tokens plus the cached part
// under input_tokens_details.cached_tokens.
func TestMetrics_ResponsesAPIShape(t *testing.T) {
	proxyURL, cleanup := setupMetrics(t)
	defer cleanup()

	resp, err := http.Post(proxyURL+"/openai/v1/responses", "application/json",
		strings.NewReader(`{"model":"gpt-4o","input":"hi"}`))
	if err != nil {
		t.Fatalf("POST /openai/v1/responses: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	stats := getStats(t, proxyURL)
	if stats.Totals.Requests != 1 {
		t.Errorf("requests = %d, want 1", stats.Totals.Requests)
	}
	if stats.Totals.Tokens.Input != 30 || stats.Totals.Tokens.Output != 7 || stats.Totals.Tokens.CacheRead != 5 {
		t.Errorf("tokens = %+v, want input 30 / output 7 / cache_read 5", stats.Totals.Tokens)
	}
	if stats.Totals.UsageMissing != 0 {
		t.Errorf("usage_missing = %d, want 0", stats.Totals.UsageMissing)
	}
	if len(stats.ByModel) != 1 || stats.ByModel[0].Model != "gpt-4o" {
		t.Fatalf("by_model = %+v, want a single gpt-4o entry", stats.ByModel)
	}
}

// TestMetrics_StreamPipeWithoutUsageCountsAsObserved answers the semantic question
// R1 raised: a stream that the pipe *inspected* but that carried no usage must land
// in usage_missing, not in uninstrumented. uninstrumented means "nobody looked";
// after R1 every stream pipe looks at every line, including the bare-JSON ones.
func TestMetrics_StreamPipeWithoutUsageCountsAsObserved(t *testing.T) {
	up := newQAUpstream(t)
	// A healthy Anthropic-shaped stream whose usage block is present but empty.
	up.messagesBody = []byte("event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{}}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n")
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	const req = `{"model":"zai--glm-4.6","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	if status, body := qaPost(t, proxy.URL+"/openai/v1/chat/completions", req); status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}

	doc := qaGetStats(t, proxy.URL)
	if doc.Totals.Uninstrumented != 0 {
		t.Errorf("uninstrumented = %d, want 0: the pipe inspected every line", doc.Totals.Uninstrumented)
	}
	if doc.Totals.UsageMissing != 1 {
		t.Errorf("usage_missing = %d, want 1: inspected, but the upstream reported no numbers", doc.Totals.UsageMissing)
	}
	if doc.Totals.Failures != 0 {
		t.Errorf("failures = %d, want 0", doc.Totals.Failures)
	}
}

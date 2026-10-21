package test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipengqi/sap-ai-core-proxy/internal/router"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
)

// This file holds the fixtures the QA suite needs and that the shared testutil
// mock cannot provide:
//
//   - qaUpstream records the *raw* bytes it received, so request-body fidelity
//     can be asserted byte for byte instead of structurally.
//   - qaUpstream pins the exact response bytes, so response fidelity can be
//     asserted against a literal instead of against a substring.
//
// It is deliberately self-contained: the shared mock in test/testutil is used by
// the rest of the suite, and changing it to capture raw bytes would couple these
// assertions to another file's fixtures.

// Fixed upstream payloads. They are intentionally byte-pinned literals: the
// tests compare the client's bytes against these exact strings.
const (
	// qaAnthropicBody is a non-streaming Anthropic message.
	qaAnthropicBody = `{"id":"msg_qa","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"anthropic--claude-3.5-sonnet","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":8,"cache_read_input_tokens":4,"cache_creation_input_tokens":2}}`

	// qaAnthropicSSE is a streaming Anthropic response. Every data: line is
	// preceded by its event: line, so PipeAnthropic has nothing to inject and the
	// client must receive these bytes verbatim.
	//
	// The two message_delta events carry cumulative output_tokens (5 then 12),
	// which is what Anthropic actually does.
	qaAnthropicSSE = "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_qa","type":"message","role":"assistant","content":[],"usage":{"input_tokens":10,"cache_read_input_tokens":4,"cache_creation_input_tokens":2}}}` + "\n" +
		"\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n" +
		"\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}` + "\n" +
		"\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":12}}` + "\n" +
		"\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n" +
		"\n"

	// qaAnthropicSSENoEvent is the Bedrock-style stream that omits event: lines.
	// PipeAnthropic injects them; qaAnthropicSSENoEventExpected is the exact
	// output that documented behaviour must produce.
	qaAnthropicSSENoEvent = `data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}` + "\n" +
		"\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":3}}` + "\n" +
		"\n"

	qaAnthropicSSENoEventExpected = "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}` + "\n" +
		"\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":3}}` + "\n" +
		"\n"

	// qaOpenAIBody is a non-streaming OpenAI chat completion.
	qaOpenAIBody = `{"id":"chatcmpl-qa","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`

	// qaOpenAISSE is a streaming OpenAI response with the final usage chunk that
	// stream_options.include_usage produces.
	qaOpenAISSE = `data: {"id":"chatcmpl-qa","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}],"usage":null}` + "\n" +
		"\n" +
		`data: {"id":"chatcmpl-qa","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}` + "\n" +
		"\n" +
		"data: [DONE]\n" +
		"\n"

	// qaResponsesBody is an OpenAI Responses API body, which reports usage under
	// input_tokens/output_tokens.
	qaResponsesBody = `{"id":"resp-qa","object":"response","status":"completed","usage":{"input_tokens":30,"output_tokens":7,"total_tokens":37,"input_tokens_details":{"cached_tokens":5}}}`
)

// qaUpstream is a byte-recording stand-in for SAP AI Core.
type qaUpstream struct {
	*httptest.Server

	mu     sync.Mutex
	bodies [][]byte
	paths  []string

	invokeStatus       int
	invokeBody         []byte
	streamStatus       int
	streamBody         []byte
	chatBody           []byte
	chatStream         []byte
	chatStreamStatus   int
	responsesBody      []byte
	messagesStatus     int
	messagesBody       []byte
	geminiBody         []byte
	geminiStreamBody   []byte
	geminiStreamStatus int

	// streamChunks, when set, replaces streamBody on the Anthropic streaming
	// endpoint: every chunk is written and flushed separately, and the handler
	// waits on streamGate between the first and the remaining chunks. It lets a
	// test prove the proxy forwards the first frame before the upstream finished.
	streamChunks [][]byte
	streamGate   chan struct{}
}

func newQAUpstream(t *testing.T) *qaUpstream {
	t.Helper()
	u := &qaUpstream{
		invokeStatus:       http.StatusOK,
		invokeBody:         []byte(qaAnthropicBody),
		streamStatus:       http.StatusOK,
		streamBody:         []byte(qaAnthropicSSE),
		chatBody:           []byte(qaOpenAIBody),
		chatStream:         []byte(qaOpenAISSE),
		chatStreamStatus:   http.StatusOK,
		responsesBody:      []byte(qaResponsesBody),
		messagesStatus:     http.StatusOK,
		messagesBody:       []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n"),
		geminiBody:         []byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":6}}`),
		geminiStreamBody:   []byte(`[{"candidates":[{"content":{"parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":6}}]`),
		geminiStreamStatus: http.StatusOK,
	}
	u.Server = httptest.NewServer(http.HandlerFunc(u.serve))
	return u
}

func (u *qaUpstream) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	path := r.URL.Path

	switch {
	case strings.HasSuffix(path, "/oauth/token"):
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "qa-token",
			"expires_in":   3600,
			"token_type":   "Bearer",
		})

	case strings.HasSuffix(path, "/v2/lm/deployments"):
		base := u.URL + "/v2/inference/deployments/dep-qa-001"
		deps := []map[string]any{}
		for _, model := range []string{
			"anthropic--claude-3.5-sonnet",
			"gpt-4o",
			"gemini-1.5-pro",
			// Cross-format families, which each have their own streaming pipe.
			"zai--glm-4.6",
			"sonar-pro",
			"qwen3-32b",
		} {
			deps = append(deps, map[string]any{
				"id":            "dep-qa-001",
				"status":        "RUNNING",
				"modelName":     model,
				"scenarioId":    "foundation-models",
				"deploymentUrl": base,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"resources": deps})

	case strings.HasSuffix(path, "/invoke-with-response-stream"):
		u.record(r, body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(u.streamStatus)
		if len(u.streamChunks) > 0 {
			u.writeChunks(w)
			return
		}
		_, _ = w.Write(u.streamBody)

	case strings.HasSuffix(path, "/invoke"):
		u.record(r, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(u.invokeStatus)
		_, _ = w.Write(u.invokeBody)

	case strings.HasSuffix(path, "/chat/completions"):
		u.record(r, body)
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(u.chatStreamStatus)
			_, _ = w.Write(u.chatStream)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(u.chatBody)

	case strings.HasSuffix(path, "/responses"):
		u.record(r, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(u.responsesBody)

	case strings.HasSuffix(path, "/images/generations"):
		u.record(r, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1700000000,"data":[{"url":"https://example.invalid/qa.png"}]}`))

	// GLM streams against the Anthropic Messages endpoint of its deployment.
	case strings.HasSuffix(path, "/messages"):
		u.record(r, body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(u.messagesStatus)
		_, _ = w.Write(u.messagesBody)

	case strings.HasSuffix(path, "/embeddings"):
		u.record(r, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[],"usage":{"prompt_tokens":7,"total_tokens":7}}`))

	case strings.Contains(path, ":streamGenerateContent"):
		u.record(r, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(u.geminiStreamStatus)
		_, _ = w.Write(u.geminiStreamBody)

	case strings.Contains(path, ":generateContent"):
		u.record(r, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(u.geminiBody)

	default:
		http.NotFound(w, r)
	}
}

func (u *qaUpstream) record(r *http.Request, body []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.paths = append(u.paths, r.Method+" "+r.URL.Path)
	u.bodies = append(u.bodies, body)
}

// writeChunks writes streamChunks one flush at a time, waiting on streamGate
// after the first chunk.
func (u *qaUpstream) writeChunks(w http.ResponseWriter) {
	flusher, _ := w.(http.Flusher)
	_, _ = w.Write(u.streamChunks[0])
	if flusher != nil {
		flusher.Flush()
	}
	if u.streamGate != nil {
		select {
		case <-u.streamGate:
		case <-time.After(3 * time.Second):
		}
	}
	for _, chunk := range u.streamChunks[1:] {
		_, _ = w.Write(chunk)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// bodiesSnapshot returns a copy of every recorded body.
func (u *qaUpstream) bodiesSnapshot() [][]byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([][]byte, len(u.bodies))
	copy(out, u.bodies)
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// qaProxy starts the real router against upstream and returns its base URL.
func qaProxy(t *testing.T, upstreamURL string) *httptest.Server {
	t.Helper()
	proxy, err := newQAProxy(upstreamURL, true)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	return proxy
}

// newQAProxy builds the production router directly so the metrics toggle can be
// exercised; testutil.NewTestProxy always enables the usage endpoints.
func newQAProxy(upstreamURL string, metrics bool) (*httptest.Server, error) {
	auth := sapclient.NewAuthManager(upstreamURL, "qa-client", "qa-secret")
	client := sapclient.NewClient(upstreamURL, "qa-group", auth)
	deployments := sapclient.NewDeploymentManager(client)
	return httptest.NewServer(router.New(client, deployments, false, metrics)), nil
}

// qaStatsDoc mirrors the JSON shape of GET /stats.
type qaStatsDoc struct {
	Since         string        `json:"since"`
	UptimeSeconds float64       `json:"uptime_seconds"`
	Totals        qaAggregate   `json:"totals"`
	BySurface     []qaBreakdown `json:"by_surface"`
	ByModel       []qaBreakdown `json:"by_model"`
}

type qaAggregate struct {
	Requests      int64    `json:"requests"`
	Failures      int64    `json:"failures"`
	UsageMissing  int64    `json:"usage_missing"`
	Tokens        qaTokens `json:"tokens"`
	CacheHitRatio float64  `json:"cache_hit_ratio"`
	// Uninstrumented counts successful requests that no observation path read.
	Uninstrumented int64 `json:"uninstrumented"`
	// UsageCaptureDropped counts requests whose usage the proxy discarded.
	UsageCaptureDropped int64 `json:"usage_capture_dropped"`
}

type qaTokens struct {
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	CacheRead  int64 `json:"cache_read_input_tokens"`
	CacheWrite int64 `json:"cache_creation_input_tokens"`
}

type qaBreakdown struct {
	Surface string `json:"surface"`
	Model   string `json:"model"`
	qaAggregate
}

// qaGetStats fetches and decodes /stats.
func qaGetStats(t *testing.T, baseURL string) qaStatsDoc {
	t.Helper()
	resp, err := http.Get(baseURL + "/stats")
	if err != nil {
		t.Fatalf("GET /stats: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /stats: expected 200, got %d: %s", resp.StatusCode, raw)
	}
	var doc qaStatsDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode /stats: %v\n%s", err, raw)
	}
	return doc
}

// qaGetMetrics fetches /metrics as text.
func qaGetMetrics(t *testing.T, baseURL string) string {
	t.Helper()
	resp, err := http.Get(baseURL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics: expected 200, got %d", resp.StatusCode)
	}
	return string(raw)
}

// qaPost sends a POST and returns the status and the raw response bytes.
func qaPost(t *testing.T, url, body string) (int, []byte) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response from %s: %v", url, err)
	}
	return resp.StatusCode, raw
}

// qaModelOf returns the model label of the single by_model entry, failing when
// the entry count is not what the test expects.
func qaModelOf(t *testing.T, doc qaStatsDoc) qaBreakdown {
	t.Helper()
	if len(doc.ByModel) != 1 {
		t.Fatalf("by_model has %d entries, want 1: %+v", len(doc.ByModel), doc.ByModel)
	}
	return doc.ByModel[0]
}

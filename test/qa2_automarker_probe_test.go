package test

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/shipengqi/sap-ai-core-proxy/internal/handler/openai"
	"github.com/shipengqi/sap-ai-core-proxy/test/testutil"
)

// qa2ToolsBody is the smallest native Anthropic request that has a markable
// final tool declaration.
const qa2NativeToolsBody = `{"model":"claude-3-5-sonnet-latest","max_tokens":100,` +
	`"messages":[{"role":"user","content":"hi"}],` +
	`"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`

// qa2OpenAIToolsBody is the same request on the OpenAI surface.
const qa2OpenAIToolsBody = `{"model":"claude-3-5-sonnet-latest","max_tokens":100,` +
	`"messages":[{"role":"user","content":"hi"}],` +
	`"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`

// qa2Results is every result label the family is allowed to publish.
var qa2Results = []string{"added", "skipped_client_marker", "skipped_no_tools"}

// qa2Deltas returns how much each result label moved for one (surface, model).
func qa2Deltas(before, after, surface, model string) map[string]int64 {
	deltas := make(map[string]int64, len(qa2Results))
	for _, result := range qa2Results {
		deltas[result] = autoMarkerCount(after, surface, model, result) -
			autoMarkerCount(before, surface, model, result)
	}
	return deltas
}

// TestQA2AutoMarkerCountsOncePerRequestAcrossThinkingRetry is the double-count
// probe. The mock rejects any body that still carries thinking, so the proxy
// must take its retry path; the counter must still move by exactly one, because
// the outcome describes the request, not the number of body builds.
func TestQA2AutoMarkerCountsOncePerRequestAcrossThinkingRetry(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		body    string
		surface string
	}{
		{
			name:    "native non-streaming retry",
			path:    "/anthropic/v1/messages",
			body:    strings.Replace(qa2NativeToolsBody, `"max_tokens":100`, `"max_tokens":100,"thinking":{"type":"enabled","budget_tokens":5000}`, 1),
			surface: "anthropic",
		},
		{
			name:    "native streaming retry",
			path:    "/anthropic/v1/messages",
			body:    strings.Replace(qa2NativeToolsBody, `"max_tokens":100`, `"max_tokens":100,"stream":true,"thinking":{"type":"enabled","budget_tokens":5000}`, 1),
			surface: "anthropic",
		},
		{
			name:    "openai non-streaming retry",
			path:    "/openai/v1/chat/completions",
			body:    strings.Replace(qa2OpenAIToolsBody, `"max_tokens":100`, `"max_tokens":8192,"reasoning_effort":"medium"`, 1),
			surface: "openai",
		},
		{
			name:    "openai streaming retry",
			path:    "/openai/v1/chat/completions",
			body:    strings.Replace(qa2OpenAIToolsBody, `"max_tokens":100`, `"max_tokens":8192,"stream":true,"reasoning_effort":"medium"`, 1),
			surface: "openai",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			openai.ClearThinkingCache()
			mock := testutil.NewMockSAPServer(t)
			defer mock.Close()
			mock.RejectThinking = true

			proxy, err := testutil.NewTestProxyWithAnthropicAutoCache(mock.URL, true)
			if err != nil {
				t.Fatalf("NewTestProxyWithAnthropicAutoCache: %v", err)
			}
			defer proxy.Close()

			before := getMetrics(t, proxy.URL)

			response, err := http.Post(proxy.URL+tc.path, "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("POST %s: %v", tc.path, err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want %d (retry must succeed)", response.StatusCode, http.StatusOK)
			}

			after := getMetrics(t, proxy.URL)
			deltas := qa2Deltas(before, after, tc.surface, autoMarkerModel)
			if deltas["added"] != 1 {
				t.Errorf("added moved by %d, want 1 (retry must not double count); all deltas %v\n---\n%s",
					deltas["added"], deltas, after)
			}
			for _, result := range []string{"skipped_client_marker", "skipped_no_tools"} {
				if deltas[result] != 0 {
					t.Errorf("%s moved by %d, want 0; all deltas %v", result, deltas[result], deltas)
				}
			}

			// The marker must actually be on the retried body, otherwise "added"
			// would be reporting a build that never reached upstream.
			body := mock.LastInvokeBody
			if strings.Contains(tc.body, `"stream":true`) {
				body = mock.LastInvokeStreamBody
			}
			if body == nil {
				t.Fatal("no upstream body was captured")
			}
			var tools []map[string]json.RawMessage
			if err := json.Unmarshal(body["tools"], &tools); err != nil {
				t.Fatalf("decode upstream tools: %v", err)
			}
			if len(tools) == 0 {
				t.Fatal("upstream body has no tools")
			}
			if _, ok := tools[len(tools)-1]["cache_control"]; !ok {
				t.Errorf("retried upstream body has no cache_control on the final tool: %s", body["tools"])
			}
		})
	}
}

// TestQA2AutoMarkerSilentWhenDisabledAcrossRetry pins the disabled contract on
// the retry path too: no result label may move at all.
func TestQA2AutoMarkerSilentWhenDisabledAcrossRetry(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		body    string
		surface string
	}{
		{
			name:    "native",
			path:    "/anthropic/v1/messages",
			body:    strings.Replace(qa2NativeToolsBody, `"max_tokens":100`, `"max_tokens":100,"thinking":{"type":"enabled","budget_tokens":5000}`, 1),
			surface: "anthropic",
		},
		{
			name:    "openai",
			path:    "/openai/v1/chat/completions",
			body:    strings.Replace(qa2OpenAIToolsBody, `"max_tokens":100`, `"max_tokens":8192,"reasoning_effort":"medium"`, 1),
			surface: "openai",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			openai.ClearThinkingCache()
			mock := testutil.NewMockSAPServer(t)
			defer mock.Close()
			mock.RejectThinking = true

			proxy, err := testutil.NewTestProxyWithAnthropicAutoCache(mock.URL, false)
			if err != nil {
				t.Fatalf("NewTestProxyWithAnthropicAutoCache: %v", err)
			}
			defer proxy.Close()

			before := getMetrics(t, proxy.URL)
			response, err := http.Post(proxy.URL+tc.path, "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("POST %s: %v", tc.path, err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
			}
			after := getMetrics(t, proxy.URL)

			for result, delta := range qa2Deltas(before, after, tc.surface, autoMarkerModel) {
				if delta != 0 {
					t.Errorf("disabled feature moved %s{surface=%q,result=%q} by %d, want 0",
						autoMarkerFamily, tc.surface, result, delta)
				}
			}
			if strings.Count(before, autoMarkerFamily) != strings.Count(after, autoMarkerFamily) {
				t.Errorf("disabled feature added %s series\n---\n%s", autoMarkerFamily, after)
			}
		})
	}
}

// TestQA2AutoMarkerModelLabelJoinsStatsByModel checks the label vocabulary
// against /stats by_model: the auto-marker series must use the resolved
// deployment name, never the alias the client sent.
func TestQA2AutoMarkerModelLabelJoinsStatsByModel(t *testing.T) {
	mock := testutil.NewMockSAPServer(t)
	defer mock.Close()
	proxy, err := testutil.NewTestProxyWithAnthropicAutoCache(mock.URL, true)
	if err != nil {
		t.Fatalf("NewTestProxyWithAnthropicAutoCache: %v", err)
	}
	defer proxy.Close()

	response, err := http.Post(proxy.URL+"/anthropic/v1/messages", "application/json",
		strings.NewReader(qa2NativeToolsBody))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()

	metrics := getMetrics(t, proxy.URL)
	if strings.Contains(metrics, `model="claude-3-5-sonnet-latest"`) {
		t.Errorf("auto-marker labels with the requested alias instead of the resolved name\n---\n%s", metrics)
	}

	stats := getStats(t, proxy.URL)
	var byModel []string
	for _, entry := range stats.ByModel {
		byModel = append(byModel, entry.Model)
	}
	found := false
	for _, key := range byModel {
		if key == autoMarkerModel {
			found = true
		}
	}
	if !found {
		t.Fatalf("/stats by_model has no %q entry: %v", autoMarkerModel, byModel)
	}
	if !strings.Contains(metrics, autoMarkerFamily+`{surface="anthropic",model="`+autoMarkerModel+`",result="added"}`) {
		t.Errorf("auto-marker series does not share the by_model vocabulary %q\n---\n%s", autoMarkerModel, metrics)
	}
}

// qa2SampleLine matches one well-formed counter sample for this family.
var qa2SampleLine = regexp.MustCompile(
	`^sap_proxy_cache_auto_marker_total\{surface="[^"\\]*",model="[^"\\]*",result="(?:added|skipped_client_marker|skipped_no_tools)"\} [0-9]+$`)

// TestQA2MetricsTextIsWellFormedPrometheus asserts the appended family does not
// corrupt the exposition: one HELP, one TYPE, and every sample line parseable.
func TestQA2MetricsTextIsWellFormedPrometheus(t *testing.T) {
	mock := testutil.NewMockSAPServer(t)
	defer mock.Close()
	proxy, err := testutil.NewTestProxyWithAnthropicAutoCache(mock.URL, true)
	if err != nil {
		t.Fatalf("NewTestProxyWithAnthropicAutoCache: %v", err)
	}
	defer proxy.Close()

	for _, body := range []string{
		qa2NativeToolsBody,
		`{"model":"claude-3-5-sonnet-latest","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`,
	} {
		response, err := http.Post(proxy.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}

	metrics := getMetrics(t, proxy.URL)
	if got := strings.Count(metrics, "# HELP "+autoMarkerFamily+" "); got != 1 {
		t.Errorf("HELP for %s appears %d times, want 1\n---\n%s", autoMarkerFamily, got, metrics)
	}
	if got := strings.Count(metrics, "# TYPE "+autoMarkerFamily+" "); got != 1 {
		t.Errorf("TYPE for %s appears %d times, want 1\n---\n%s", autoMarkerFamily, got, metrics)
	}
	if !strings.Contains(metrics, "# TYPE "+autoMarkerFamily+" counter") {
		t.Errorf("TYPE line is not 'counter'\n---\n%s", metrics)
	}
	if !strings.HasSuffix(metrics, "\n") {
		t.Error("/metrics payload does not end with a newline, so the next family would be glued on")
	}

	seen := make(map[string]bool)
	samples := 0
	for _, line := range strings.Split(metrics, "\n") {
		if !strings.HasPrefix(line, autoMarkerFamily+"{") {
			continue
		}
		samples++
		if !qa2SampleLine.MatchString(line) {
			t.Errorf("malformed sample line: %q", line)
		}
		if seen[line[:strings.LastIndex(line, "}")]] {
			t.Errorf("duplicate label set: %q", line)
		}
		seen[line[:strings.LastIndex(line, "}")]] = true
	}
	if samples == 0 {
		t.Errorf("no %s samples rendered after two requests\n---\n%s", autoMarkerFamily, metrics)
	}

	// Every non-comment, non-blank line must be a sample whose braces balance;
	// a stray fragment here is what breaks a real scraper.
	for _, line := range strings.Split(strings.TrimSuffix(metrics, "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Count(line, "{") != strings.Count(line, "}") {
			t.Errorf("unbalanced braces in /metrics line: %q", line)
		}
		if !regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*(\{[^}]*\})? \S+$`).MatchString(line) {
			t.Errorf("/metrics line is not a valid sample: %q", line)
		}
	}
}

// TestQA2AutoMarkerAllResultsReachableOnBothSurfaces makes sure every result
// label is actually produced by the handler on both surfaces, with the surface
// label the PRD names. A label that can never be emitted is worse than a missing
// one, because it silently reads as "never happened".
func TestQA2AutoMarkerAllResultsReachableOnBothSurfaces(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		body    string
		surface string
		result  string
	}{
		{
			name:    "native added",
			path:    "/anthropic/v1/messages",
			body:    qa2NativeToolsBody,
			surface: "anthropic",
			result:  "added",
		},
		{
			name:    "native skipped_client_marker",
			path:    "/anthropic/v1/messages",
			body:    `{"model":"claude-3-5-sonnet-latest","max_tokens":10,"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`,
			surface: "anthropic",
			result:  "skipped_client_marker",
		},
		{
			name:    "native skipped_no_tools",
			path:    "/anthropic/v1/messages",
			body:    `{"model":"claude-3-5-sonnet-latest","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`,
			surface: "anthropic",
			result:  "skipped_no_tools",
		},
		{
			name:    "native empty tools array",
			path:    "/anthropic/v1/messages",
			body:    `{"model":"claude-3-5-sonnet-latest","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"tools":[]}`,
			surface: "anthropic",
			result:  "skipped_no_tools",
		},
		{
			name:    "openai added",
			path:    "/openai/v1/chat/completions",
			body:    qa2OpenAIToolsBody,
			surface: "openai",
			result:  "added",
		},
		{
			name:    "openai skipped_client_marker",
			path:    "/openai/v1/chat/completions",
			body:    `{"model":"claude-3-5-sonnet-latest","max_tokens":10,"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`,
			surface: "openai",
			result:  "skipped_client_marker",
		},
		{
			name:    "openai skipped_no_tools",
			path:    "/openai/v1/chat/completions",
			body:    `{"model":"claude-3-5-sonnet-latest","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`,
			surface: "openai",
			result:  "skipped_no_tools",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			openai.ClearThinkingCache()
			mock := testutil.NewMockSAPServer(t)
			defer mock.Close()
			proxy, err := testutil.NewTestProxyWithAnthropicAutoCache(mock.URL, true)
			if err != nil {
				t.Fatalf("NewTestProxyWithAnthropicAutoCache: %v", err)
			}
			defer proxy.Close()

			before := getMetrics(t, proxy.URL)
			response, err := http.Post(proxy.URL+tc.path, "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("POST %s: %v", tc.path, err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
			}
			after := getMetrics(t, proxy.URL)

			deltas := qa2Deltas(before, after, tc.surface, autoMarkerModel)
			if deltas[tc.result] != 1 {
				t.Errorf("%s{surface=%q,model=%q} moved by %d, want 1; all deltas %v\n---\n%s",
					autoMarkerFamily, tc.surface, autoMarkerModel, deltas[tc.result], deltas, after)
			}
			for result, delta := range deltas {
				if result != tc.result && delta != 0 {
					t.Errorf("unrelated result %q moved by %d on a single request", result, delta)
				}
			}
		})
	}
}

// TestQA2AutoMarkerIsLinearInRequestCount checks the counter is one-per-request
// rather than one-per-body-build: five identical requests must add exactly five.
func TestQA2AutoMarkerIsLinearInRequestCount(t *testing.T) {
	const requests = 5
	for _, tc := range []struct {
		name    string
		path    string
		body    string
		surface string
	}{
		{name: "native", path: "/anthropic/v1/messages", body: qa2NativeToolsBody, surface: "anthropic"},
		{name: "openai", path: "/openai/v1/chat/completions", body: qa2OpenAIToolsBody, surface: "openai"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			openai.ClearThinkingCache()
			mock := testutil.NewMockSAPServer(t)
			defer mock.Close()
			proxy, err := testutil.NewTestProxyWithAnthropicAutoCache(mock.URL, true)
			if err != nil {
				t.Fatalf("NewTestProxyWithAnthropicAutoCache: %v", err)
			}
			defer proxy.Close()

			before := getMetrics(t, proxy.URL)
			for i := 0; i < requests; i++ {
				response, err := http.Post(proxy.URL+tc.path, "application/json", strings.NewReader(tc.body))
				if err != nil {
					t.Fatalf("POST %d: %v", i, err)
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Fatalf("request %d: status = %d, want 200", i, response.StatusCode)
				}
			}
			after := getMetrics(t, proxy.URL)

			if delta := autoMarkerCount(after, tc.surface, autoMarkerModel, "added") -
				autoMarkerCount(before, tc.surface, autoMarkerModel, "added"); delta != requests {
				t.Errorf("added moved by %d for %d requests, want %d", delta, requests, requests)
			}
		})
	}
}

// TestQA2StatsCacheAutoMarkerNeverNull checks the JSON contract: the field is
// always an array, even when nothing was ever observed.
func TestQA2StatsCacheAutoMarkerNeverNull(t *testing.T) {
	mock := testutil.NewMockSAPServer(t)
	defer mock.Close()
	proxy, err := testutil.NewTestProxyWithAnthropicAutoCache(mock.URL, false)
	if err != nil {
		t.Fatalf("NewTestProxyWithAnthropicAutoCache: %v", err)
	}
	defer proxy.Close()

	raw := getMetricsAt(t, proxy.URL, "/stats/cache")
	if !strings.Contains(raw, `"auto_marker":`) {
		t.Fatalf("/stats/cache has no auto_marker field: %s", raw)
	}
	if strings.Contains(raw, `"auto_marker":null`) {
		t.Errorf("/stats/cache auto_marker is null, want an array: %s", raw)
	}

	var stats struct {
		AutoMarker []struct {
			Surface string `json:"surface"`
			Model   string `json:"model"`
			Result  string `json:"result"`
			Count   int64  `json:"count"`
		} `json:"auto_marker"`
	}
	if err := json.Unmarshal([]byte(raw), &stats); err != nil {
		t.Fatalf("decode /stats/cache: %v", err)
	}
	if stats.AutoMarker == nil {
		t.Error("decoded auto_marker is nil, want an empty array")
	}
}

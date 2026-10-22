package test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/shipengqi/sap-ai-core-proxy/test/testutil"
)

// autoMarkerFamily is the metric family this suite watches.
const autoMarkerFamily = "sap_proxy_cache_auto_marker_total"

// autoMarkerModel is the resolved deployment model name the mock exposes for
// claude-3-5-sonnet-latest. The proxy labels with the resolved name, not with
// the alias the client sent.
const autoMarkerModel = "anthropic--claude-3.5-sonnet"

// autoMarkerCount returns the value of one auto-marker series, or 0 when the
// series is absent.
//
// These counters are process-wide and accumulate across every test in this
// package, so the tests below compare a count before and after their own
// request instead of asserting an absolute value.
func autoMarkerCount(metrics, surface, model, result string) int64 {
	prefix := autoMarkerFamily + `{surface="` + surface + `",model="` + model + `",result="` + result + `"} `
	for _, line := range strings.Split(metrics, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		value, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, prefix)), 10, 64)
		if err != nil {
			return 0
		}
		return value
	}
	return 0
}

// TestAutoMarkerMetricsRecordsAddedAndSkipped drives one request per outcome and
// asserts each one lands in sap_proxy_cache_auto_marker_total with the right
// surface, resolved model, and result label.
func TestAutoMarkerMetricsRecordsAddedAndSkipped(t *testing.T) {
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
			body:    `{"model":"claude-3-5-sonnet-latest","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`,
			surface: "anthropic",
			result:  "added",
		},
		{
			name:    "openai added",
			path:    "/openai/v1/chat/completions",
			body:    `{"model":"claude-3-5-sonnet-latest","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`,
			surface: "openai",
			result:  "added",
		},
		{
			name:    "native skipped client marker",
			path:    "/anthropic/v1/messages",
			body:    `{"model":"claude-3-5-sonnet-latest","max_tokens":10,"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`,
			surface: "anthropic",
			result:  "skipped_client_marker",
		},
		{
			name:    "native skipped no tools",
			path:    "/anthropic/v1/messages",
			body:    `{"model":"claude-3-5-sonnet-latest","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`,
			surface: "anthropic",
			result:  "skipped_no_tools",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := testutil.NewMockSAPServer(t)
			defer mock.Close()
			proxy, err := testutil.NewTestProxyWithAnthropicAutoCache(mock.URL, true)
			if err != nil {
				t.Fatalf("NewTestProxyWithAnthropicAutoCache: %v", err)
			}
			defer proxy.Close()

			before := autoMarkerCount(getMetrics(t, proxy.URL), tc.surface, autoMarkerModel, tc.result)

			response, err := http.Post(proxy.URL+tc.path, "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("POST %s: %v", tc.path, err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
			}

			metrics := getMetrics(t, proxy.URL)
			after := autoMarkerCount(metrics, tc.surface, autoMarkerModel, tc.result)
			if after != before+1 {
				t.Errorf("%s{surface=%q,model=%q,result=%q} = %d, want %d\n---\n%s",
					autoMarkerFamily, tc.surface, autoMarkerModel, tc.result, after, before+1, metrics)
			}
			if !strings.Contains(metrics, "# TYPE "+autoMarkerFamily+" counter") {
				t.Errorf("/metrics is missing the %s counter header\n---\n%s", autoMarkerFamily, metrics)
			}
		})
	}
}

// TestAutoMarkerMetricsSilentWhenDisabled pins the disabled contract: the
// feature emits no signal at all, so no new series appears for a request that
// would otherwise have been marked.
func TestAutoMarkerMetricsSilentWhenDisabled(t *testing.T) {
	mock := testutil.NewMockSAPServer(t)
	defer mock.Close()
	proxy, err := testutil.NewTestProxyWithAnthropicAutoCache(mock.URL, false)
	if err != nil {
		t.Fatalf("NewTestProxyWithAnthropicAutoCache: %v", err)
	}
	defer proxy.Close()

	before := getMetrics(t, proxy.URL)

	body := `{"model":"claude-3-5-sonnet-latest","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`
	response, err := http.Post(proxy.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /anthropic/v1/messages: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	after := getMetrics(t, proxy.URL)
	for _, result := range []string{"added", "skipped_client_marker", "skipped_no_tools"} {
		gotBefore := autoMarkerCount(before, "anthropic", autoMarkerModel, result)
		gotAfter := autoMarkerCount(after, "anthropic", autoMarkerModel, result)
		if gotAfter != gotBefore {
			t.Errorf("disabled feature changed %s{result=%q}: %d -> %d\n---\n%s",
				autoMarkerFamily, result, gotBefore, gotAfter, after)
		}
	}
}

// TestCacheStatsExposesAutoMarker checks the JSON side of the same counters, so
// /stats/cache answers "did the marker land?" without a scrape.
func TestCacheStatsExposesAutoMarker(t *testing.T) {
	mock := testutil.NewMockSAPServer(t)
	defer mock.Close()
	proxy, err := testutil.NewTestProxyWithAnthropicAutoCache(mock.URL, true)
	if err != nil {
		t.Fatalf("NewTestProxyWithAnthropicAutoCache: %v", err)
	}
	defer proxy.Close()

	body := `{"model":"claude-3-5-sonnet-latest","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`
	response, err := http.Post(proxy.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /anthropic/v1/messages: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	var stats struct {
		AutoMarker []struct {
			Surface string `json:"surface"`
			Model   string `json:"model"`
			Result  string `json:"result"`
			Count   int64  `json:"count"`
		} `json:"auto_marker"`
	}
	statsResponse, err := http.Get(proxy.URL + "/stats/cache")
	if err != nil {
		t.Fatalf("GET /stats/cache: %v", err)
	}
	defer func() { _ = statsResponse.Body.Close() }()
	if statsResponse.StatusCode != http.StatusOK {
		t.Fatalf("/stats/cache status = %d, want %d", statsResponse.StatusCode, http.StatusOK)
	}
	if err := json.NewDecoder(statsResponse.Body).Decode(&stats); err != nil {
		t.Fatalf("decode /stats/cache: %v", err)
	}

	found := false
	for _, series := range stats.AutoMarker {
		if series.Surface == "anthropic" && series.Model == autoMarkerModel && series.Result == "added" && series.Count > 0 {
			found = true
		}
	}
	if !found {
		t.Errorf("/stats/cache auto_marker has no anthropic/%s/added series: %+v", autoMarkerModel, stats.AutoMarker)
	}
}

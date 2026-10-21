package test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// cacheStats mirrors the JSON shape of GET /stats/cache.
type cacheStats struct {
	TrackedSessions int `json:"tracked_sessions"`
	ByResult        []struct {
		Surface string `json:"surface"`
		Model   string `json:"model"`
		Result  string `json:"result"`
		Count   int64  `json:"count"`
	} `json:"by_result"`
	ByOutcome []struct {
		Surface string `json:"surface"`
		Model   string `json:"model"`
		Outcome string `json:"outcome"`
		Count   int64  `json:"count"`
	} `json:"by_outcome"`
	Volatile []struct {
		Surface string `json:"surface"`
		Kind    string `json:"kind"`
		Count   int64  `json:"count"`
	} `json:"volatile"`
	Skipped []struct {
		Reason string `json:"reason"`
		Count  int64  `json:"count"`
	} `json:"skipped"`
	TopSessions []struct {
		Key      string `json:"key"`
		Requests int64  `json:"requests"`
		Busted   int64  `json:"busted"`
	} `json:"top_sessions"`
}

func (s cacheStats) outcome(name string) int64 {
	var n int64
	for _, o := range s.ByOutcome {
		if o.Outcome == name {
			n += o.Count
		}
	}
	return n
}

func (s cacheStats) result(name string) int64 {
	var n int64
	for _, r := range s.ByResult {
		if r.Result == name {
			n += r.Count
		}
	}
	return n
}

func getCacheStats(t *testing.T, proxyURL string) cacheStats {
	t.Helper()
	resp, err := http.Get(proxyURL + "/stats/cache")
	if err != nil {
		t.Fatalf("GET /stats/cache: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /stats/cache: expected 200, got %d: %s", resp.StatusCode, body)
	}

	var stats cacheStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		t.Fatalf("decode /stats/cache: %v", err)
	}
	return stats
}

func postMessages(t *testing.T, proxyURL, body string) {
	t.Helper()
	resp, err := http.Post(proxyURL+"/anthropic/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /anthropic/v1/messages: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /anthropic/v1/messages: expected 200, got %d", resp.StatusCode)
	}
}

// TestCacheDiag_AttributionLadder drives one conversation through the three
// shapes that matter: first request, append-only growth, and a system-prompt
// change. The mock reports cache_read=4, so a stable prefix must attribute to
// "hit" and a changed prefix to "busted".
func TestCacheDiag_AttributionLadder(t *testing.T) {
	proxyURL, cleanup := setupMetrics(t)
	defer cleanup()

	first := `{"model":"claude-3-5-sonnet-latest","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`
	grown := `{"model":"claude-3-5-sonnet-latest","max_tokens":16,"messages":[` +
		`{"role":"user","content":"hello"},` +
		`{"role":"assistant","content":"hi"},` +
		`{"role":"user","content":"again"}]}`
	drifted := `{"model":"claude-3-5-sonnet-latest","max_tokens":16,"system":"you are terse","messages":[` +
		`{"role":"user","content":"hello"},` +
		`{"role":"assistant","content":"hi"},` +
		`{"role":"user","content":"again"}]}`

	postMessages(t, proxyURL, first)
	postMessages(t, proxyURL, grown)
	postMessages(t, proxyURL, drifted)

	stats := getCacheStats(t, proxyURL)

	if got := stats.outcome("first_request"); got != 1 {
		t.Errorf("first_request = %d, want 1", got)
	}
	if got := stats.outcome("append_only"); got != 1 {
		t.Errorf("append_only = %d, want 1 (normal growth must never look like drift)", got)
	}
	if got := stats.outcome("system_changed"); got != 1 {
		t.Errorf("system_changed = %d, want 1", got)
	}
	if got := stats.outcome("messages_rewritten"); got != 0 {
		t.Errorf("messages_rewritten = %d, want 0", got)
	}

	if got := stats.result("first"); got != 1 {
		t.Errorf("result first = %d, want 1", got)
	}
	if got := stats.result("hit"); got != 1 {
		t.Errorf("result hit = %d, want 1 (stable prefix with cache_read>0)", got)
	}
	if got := stats.result("busted"); got != 1 {
		t.Errorf("result busted = %d, want 1 (system changed)", got)
	}
	if got := stats.result("no_cache"); got != 0 {
		t.Errorf("result no_cache = %d, want 0", got)
	}

	if len(stats.Skipped) != 0 {
		t.Errorf("skipped = %+v, want none", stats.Skipped)
	}
	if stats.TrackedSessions != 1 {
		t.Errorf("tracked sessions = %d, want 1", stats.TrackedSessions)
	}
	if len(stats.TopSessions) != 1 || stats.TopSessions[0].Requests != 3 {
		t.Errorf("top sessions = %+v, want one session with 3 requests", stats.TopSessions)
	}
	if len(stats.TopSessions) == 1 && stats.TopSessions[0].Busted != 1 {
		t.Errorf("session busted = %d, want 1", stats.TopSessions[0].Busted)
	}
}

// TestCacheDiag_VolatilePrefixIsReported proves the scan looks at the cache hot
// zone: a timestamp in the system prompt is reported, a timestamp in the live
// zone is not (the live zone changes every turn, so a clock there is harmless).
func TestCacheDiag_VolatilePrefixIsReported(t *testing.T) {
	proxyURL, cleanup := setupMetrics(t)
	defer cleanup()

	withVolatileSystem := `{"model":"claude-3-5-sonnet-latest","max_tokens":16,` +
		`"system":"Today is 2026-10-21T09:53:50Z. Be terse.",` +
		`"messages":[{"role":"user","content":"hello"}]}`
	volatileInLiveZone := `{"model":"claude-3-5-sonnet-latest","max_tokens":16,` +
		`"messages":[{"role":"user","content":"the build failed at 2026-10-21T09:53:50Z"}]}`

	postMessages(t, proxyURL, withVolatileSystem)
	postMessages(t, proxyURL, volatileInLiveZone)

	stats := getCacheStats(t, proxyURL)

	var timestamps int64
	for _, v := range stats.Volatile {
		if v.Kind == "iso_timestamp" {
			timestamps += v.Count
		}
	}
	if timestamps != 1 {
		t.Errorf("iso_timestamp count = %d, want 1 (only the system prompt is scanned, not the live zone)", timestamps)
	}
}

// TestCacheDiag_MetricsFamiliesAreExposed checks that the attribution families
// land in the same /metrics payload as the token counters.
func TestCacheDiag_MetricsFamiliesAreExposed(t *testing.T) {
	proxyURL, cleanup := setupMetrics(t)
	defer cleanup()

	postMessages(t, proxyURL, `{"model":"claude-3-5-sonnet-latest","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)

	body := getMetrics(t, proxyURL)
	for _, want := range []string{
		"# TYPE sap_proxy_cache_prefix_total counter",
		"# TYPE sap_proxy_cache_result_total counter",
		"# TYPE sap_proxy_cache_volatile_total counter",
		"# TYPE sap_proxy_cache_skipped_total counter",
		`sap_proxy_cache_prefix_total{surface="anthropic",model="anthropic--claude-3.5-sonnet",outcome="first_request"} 1`,
		`sap_proxy_cache_result_total{surface="anthropic",model="anthropic--claude-3.5-sonnet",result="first"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing %q", want)
		}
	}

	// The usage families must still be there: /metrics is one endpoint, not two.
	if !strings.Contains(body, "# TYPE sap_proxy_requests_total counter") {
		t.Error("/metrics lost the usage families when the diagnostics families were appended")
	}
}

// TestCacheDiag_NonInferenceRoutesAreNotCounted makes sure scraping and health
// checks do not create sessions or outcomes.
func TestCacheDiag_NonInferenceRoutesAreNotCounted(t *testing.T) {
	proxyURL, cleanup := setupMetrics(t)
	defer cleanup()

	before := getCacheStats(t, proxyURL)

	resp, err := http.Get(proxyURL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	_ = resp.Body.Close()

	after := getCacheStats(t, proxyURL)
	if len(after.ByOutcome) != len(before.ByOutcome) || after.TrackedSessions != before.TrackedSessions {
		t.Errorf("health check changed the cache attribution: before=%+v after=%+v", before, after)
	}
}

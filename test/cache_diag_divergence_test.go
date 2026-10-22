package test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// divergenceEntry mirrors one element of the divergence array in /stats/cache.
type divergenceEntry struct {
	Surface string `json:"surface"`
	Model   string `json:"model"`
	Where   string `json:"where"`
	Count   int64  `json:"count"`
}

// getCacheStatsRaw returns the undecoded body of GET /stats/cache, which is what
// a null-versus-empty-array assertion needs: a decoded nil slice cannot tell the
// two apart.
func getCacheStatsRaw(t *testing.T, proxyURL string) string {
	t.Helper()
	resp, err := http.Get(proxyURL + "/stats/cache")
	if err != nil {
		t.Fatalf("GET /stats/cache: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /stats/cache: expected 200, got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /stats/cache: %v", err)
	}
	return string(body)
}

// getDivergence decodes just the divergence array from /stats/cache.
func getDivergence(t *testing.T, proxyURL string) []divergenceEntry {
	t.Helper()
	var doc struct {
		Divergence []divergenceEntry `json:"divergence"`
	}
	raw := getCacheStatsRaw(t, proxyURL)
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("decode /stats/cache: %v\n%s", err, raw)
	}
	return doc.Divergence
}

// TestCacheDiag_DivergenceIsAnEmptyArrayWhenUnused pins the wire shape before
// any rewrite has been seen: the field must be present and be [], not null.
// Consumers iterate it blindly, so a null would make the empty case special.
func TestCacheDiag_DivergenceIsAnEmptyArrayWhenUnused(t *testing.T) {
	proxyURL, cleanup := setupMetrics(t)
	defer cleanup()

	raw := getCacheStatsRaw(t, proxyURL)
	if !strings.Contains(raw, `"divergence":[]`) {
		t.Errorf("/stats/cache must report an empty divergence array, got: %s", raw)
	}
	if strings.Contains(raw, `"divergence":null`) {
		t.Errorf("/stats/cache reported a null divergence array: %s", raw)
	}

	// A healthy request must not create a series either.
	postMessages(t, proxyURL, `{"model":"claude-3-5-sonnet-latest","max_tokens":16,`+
		`"messages":[{"role":"user","content":"hello"}]}`)
	if got := getDivergence(t, proxyURL); len(got) != 0 {
		t.Errorf("a first request produced divergence entries: %+v", got)
	}
}

// TestCacheDiag_DivergenceSplitsTailFromHistory drives both shapes through the
// real proxy. Both are messages_rewritten; only the divergence label separates
// "the client re-sent its last turn" from "something deeper in the history was
// rewritten", and that separation is the whole point of the counter.
func TestCacheDiag_DivergenceSplitsTailFromHistory(t *testing.T) {
	proxyURL, cleanup := setupMetrics(t)
	defer cleanup()

	msg := func(role, text string) string {
		return `{"role":"` + role + `","content":"` + text + `"}`
	}
	post := func(messages ...string) {
		postMessages(t, proxyURL, `{"model":"claude-3-5-sonnet-latest","max_tokens":16,"messages":[`+
			strings.Join(messages, ",")+`]}`)
	}

	// Conversation A: the previous last message is edited, then the turn goes on.
	// Everything before it stays byte-identical, so the divergence is at the tail.
	post(msg("user", "alpha"), msg("assistant", "r"), msg("user", "beta"))
	post(msg("user", "alpha"), msg("assistant", "r"), msg("user", "beta edited"), msg("assistant", "ack"))

	// Conversation B: a message inside the retained history is edited. That is
	// what thinking-block stripping, an injected reminder or compaction looks
	// like, and it invalidates the whole cacheable prefix behind it.
	post(msg("user", "gamma"), msg("assistant", "r"), msg("user", "delta"))
	post(msg("user", "gamma"), msg("assistant", "r edited"), msg("user", "delta edited"), msg("assistant", "ack"))

	entries := getDivergence(t, proxyURL)
	got := map[string]int64{}
	for _, e := range entries {
		if e.Surface != "anthropic" {
			t.Errorf("divergence surface = %q, want anthropic", e.Surface)
		}
		if e.Model != "anthropic--claude-3.5-sonnet" {
			t.Errorf("divergence model = %q, want the normalised deployment name", e.Model)
		}
		got[e.Where] += e.Count
	}
	if got["tail"] != 1 {
		t.Errorf("divergence{where=tail} = %d, want 1: %+v", got["tail"], entries)
	}
	if got["history"] != 1 {
		t.Errorf("divergence{where=history} = %d, want 1: %+v", got["history"], entries)
	}
	if len(got) != 2 {
		t.Errorf("divergence has %d distinct where values, want 2: %+v", len(got), entries)
	}

	// The split must account for exactly the rewrites the outcome family reports.
	stats := getCacheStats(t, proxyURL)
	if rewrites := stats.outcome("messages_rewritten"); rewrites != 2 {
		t.Errorf("messages_rewritten = %d, want 2 (one per conversation)", rewrites)
	}

	// And the same numbers must be scrapeable, not only pollable.
	body := getMetrics(t, proxyURL)
	for _, want := range []string{
		"# TYPE sap_proxy_cache_divergence_total counter",
		`sap_proxy_cache_divergence_total{surface="anthropic",model="anthropic--claude-3.5-sonnet",where="tail"} 1`,
		`sap_proxy_cache_divergence_total{surface="anthropic",model="anthropic--claude-3.5-sonnet",where="history"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics is missing %q\n%s", want, body)
		}
	}
}

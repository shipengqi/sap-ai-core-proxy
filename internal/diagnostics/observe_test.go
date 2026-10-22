package diagnostics

import (
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

// countResults folds the result series into a map keyed by result name.
func countResults(s Snapshot) map[string]int64 {
	out := make(map[string]int64, len(s.ByResult))
	for _, r := range s.ByResult {
		out[string(r.Result)] += r.Count
	}
	return out
}

// TestObserveAttributesRewriteAndNoUsage covers the three attribution cells the
// end-to-end tests cannot reach with the shared mock, because that mock always
// reports a cache read.
func TestObserveAttributesRewriteAndNoUsage(t *testing.T) {
	req := parseRequest(body(userMsg("hello")), "anthropic", "m", "ip")
	col := NewCollector()

	// First sight of the conversation.
	col.observe(req, usage.Tokens{CacheRead: 10}, true, "m")
	// Same prefix, nothing read but something written: expiry or another backend.
	col.observe(req, usage.Tokens{CacheWrite: 50}, true, "m")
	// Same prefix, upstream said nothing at all.
	col.observe(req, usage.Tokens{}, false, "m")

	got := countResults(col.Snapshot())
	if got[string(ResultFirst)] != 1 {
		t.Errorf("first = %d, want 1", got[string(ResultFirst)])
	}
	if got[string(ResultRewrite)] != 1 {
		t.Errorf("rewrite = %d, want 1 (stable prefix with only cache_write)", got[string(ResultRewrite)])
	}
	if got[string(ResultNoUsage)] != 1 {
		t.Errorf("no_usage = %d, want 1", got[string(ResultNoUsage)])
	}
	if got[string(ResultNoCache)] != 0 {
		t.Errorf("no_cache = %d, want 0: a missing usage block must not be blamed on the client", got[string(ResultNoCache)])
	}
	if got[string(ResultBusted)] != 0 {
		t.Errorf("busted = %d, want 0", got[string(ResultBusted)])
	}
}

// TestObserveReportsBustedEvenWhenTheProviderHitCache guards the most misleading
// possible number: a provider that serves a hit cannot make a rewritten prefix
// look healthy.
func TestObserveReportsBustedEvenWhenTheProviderHitCache(t *testing.T) {
	col := NewCollector()
	first := parseRequest(body(userMsg("hello")), "anthropic", "m", "ip")
	drifted := parseRequest(
		[]byte(`{"system":"new prompt","messages":[`+userMsg("hello")+`]}`),
		"anthropic", "m", "ip",
	)

	col.observe(first, usage.Tokens{CacheRead: 10}, true, "m")
	col.observe(drifted, usage.Tokens{CacheRead: 999}, true, "m")

	got := countResults(col.Snapshot())
	if got[string(ResultBusted)] != 1 {
		t.Errorf("busted = %d, want 1: a changed system prompt must not be masked by a cache hit", got[string(ResultBusted)])
	}
	if got[string(ResultHit)] != 0 {
		t.Errorf("hit = %d, want 0", got[string(ResultHit)])
	}
}

// TestSessionStoreHoldsTheBound checks the memory ceiling rather than the
// eviction order: an unbounded map is the failure mode that matters here.
func TestSessionStoreHoldsTheBound(t *testing.T) {
	s := newSessionStore(defaultSessionCapacity, time.Hour)
	for i := 0; i < defaultSessionCapacity+200; i++ {
		s.put(fmt.Sprintf("session-%d", i), sessionState{msgCount: 1})
	}
	if got := s.len(); got != defaultSessionCapacity {
		t.Errorf("tracked sessions = %d, want the cap %d", got, defaultSessionCapacity)
	}
}

// TestAppendOnlyIsExact pins that append-only detection covers the whole
// history rather than a capped window. The capped version missed every rewrite
// past the window — 84% of them on a 200-message conversation — and missed
// exactly where they are hardest to see by eye.
func TestAppendOnlyIsExact(t *testing.T) {
	// A conversation that already ran past any reasonable window.
	const total = 100
	hashes := make([][32]byte, total)
	for i := range hashes {
		hashes[i] = sha256.Sum256([]byte{byte(i)})
	}
	prev := sessionState{
		msgCount: total,
		fp:       fingerprints{FullChain: chainAt(hashes, total)},
	}

	clone := func() [][32]byte {
		out := make([][32]byte, total)
		copy(out, hashes)
		return out
	}
	if isAppendOnly(prev, total, clone()) {
		t.Error("an identical history must not read as append-only: growth requires more messages")
	}

	grown := append(clone(), [32]byte{99})
	if !isAppendOnly(prev, total+1, grown) {
		t.Error("a conversation that keeps growing should classify as append_only")
	}

	// An edit deep in the history, far past where a window would have stopped.
	deep := clone()
	deep[total-10] = [32]byte{42}
	if isAppendOnly(prev, total+1, deep) {
		t.Errorf("an edit at message %d must be detected, not folded into append_only", total-10)
	}

	near := clone()
	near[3] = [32]byte{42}
	if isAppendOnly(prev, total+1, near) {
		t.Error("an edit near the start of the history must be detected")
	}

	if isAppendOnly(prev, 0, nil) {
		t.Error("an empty current conversation is not growth")
	}
}

// TestPerSessionCountersFollowTheLRUBound proves the per-conversation counters
// are retired together with the session, which is what keeps the store bounded.
func TestPerSessionCountersFollowTheLRUBound(t *testing.T) {
	col := NewCollector()

	first := parseRequest(body(userMsg("conversation one")), "anthropic", "m", "ip")
	col.observe(first, usage.Tokens{CacheRead: 10}, true, "m")

	stats := col.sessions.topStats(maxTopSessions)
	if len(stats) != 1 || stats[0].Requests != 1 {
		t.Fatalf("top stats = %+v, want one session with one request", stats)
	}

	// Push the store past its capacity so the first conversation is evicted.
	for i := 0; i < defaultSessionCapacity+10; i++ {
		col.observe(parseRequest(body(userMsg(fmt.Sprintf("filler %d", i))), "anthropic", "m", "ip"),
			usage.Tokens{}, true, "m")
	}

	for _, s := range col.sessions.topStats(maxTopSessions) {
		if s.Requests == 1 && s.Key == "" {
			t.Errorf("evicted conversation still present: %+v", s)
		}
	}
	if got := col.sessions.len(); got != defaultSessionCapacity {
		t.Errorf("tracked sessions = %d, want the cap %d", got, defaultSessionCapacity)
	}
}

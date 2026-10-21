package diagnostics

import (
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

// TestAppendOnlyWindowIsBounded documents the deliberate blind spot: a rewrite
// beyond the stored window is not detected.
func TestAppendOnlyWindowIsBounded(t *testing.T) {
	hashes := make([][32]byte, maxStoredMessageHashes)
	for i := range hashes {
		hashes[i] = [32]byte{byte(i + 1)}
	}
	prev := sessionState{msgCount: 100, msgHash: hashes}

	// A long conversation that keeps growing is append-only.
	grown := make([][32]byte, maxStoredMessageHashes+1)
	copy(grown, hashes)
	grown[maxStoredMessageHashes] = [32]byte{99}
	if !isAppendOnly(prev, 101, grown) {
		t.Error("growth beyond the stored window should still classify as append_only")
	}

	// A rewrite inside the stored window is detected.
	edited := make([][32]byte, maxStoredMessageHashes+1)
	copy(edited, hashes)
	edited[5] = [32]byte{42}
	if isAppendOnly(prev, 101, edited) {
		t.Error("an edit inside the stored window must not classify as append_only")
	}

	// A rewrite beyond the stored window is not detected. This is the accepted
	// trade documented on isAppendOnly; the assertion pins the behaviour so a
	// future change has to update it deliberately.
	deep := make([][32]byte, maxStoredMessageHashes+1)
	copy(deep, hashes)
	deep[maxStoredMessageHashes] = [32]byte{77}
	if !isAppendOnly(prev, 101, deep) {
		t.Error("a rewrite beyond the stored window is expected to be missed")
	}
}

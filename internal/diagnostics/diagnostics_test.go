package diagnostics

import (
	"strings"
	"testing"
	"time"

	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

func body(messages ...string) []byte {
	return []byte(`{"model":"claude-3-5-sonnet-latest","max_tokens":16,"messages":[` + strings.Join(messages, ",") + `]}`)
}

func userMsg(text string) string {
	return `{"role":"user","content":"` + text + `"}`
}

func TestParseRequestFingerprintsAreStable(t *testing.T) {
	b := body(userMsg("hello"), userMsg("again"))

	a := parseRequest(b, "anthropic", "claude-3-5-sonnet-latest", "1.2.3.4")
	c := parseRequest(b, "anthropic", "claude-3-5-sonnet-latest", "1.2.3.4")

	if a.skipped != "" {
		t.Fatalf("unexpected skip: %s", a.skipped)
	}
	if a.fp != c.fp {
		t.Fatal("fingerprints are not deterministic for identical bodies")
	}
	if a.session != c.session {
		t.Fatal("session key is not deterministic for identical bodies")
	}
	if a.fp.System != ([32]byte{}) {
		t.Error("system fingerprint should be zero when the body has no system field")
	}
	if a.msgCount != 2 || len(a.msgHash) != 2 {
		t.Errorf("msgCount=%d msgHash=%d, want 2/2", a.msgCount, len(a.msgHash))
	}
	if a.fp.FullChain == (fingerprints{}.FullChain) {
		t.Error("FullChain was not set for a body with messages")
	}
}

func TestParseRequestSystemChangeKeepsPrefix(t *testing.T) {
	withA := []byte(`{"system":"aaa","messages":[` + userMsg("hello") + `]}`)
	withB := []byte(`{"system":"bbb","messages":[` + userMsg("hello") + `]}`)

	a := parseRequest(withA, "anthropic", "m", "ip")
	b := parseRequest(withB, "anthropic", "m", "ip")

	if a.fp.System == b.fp.System {
		t.Error("system fingerprint did not change when the system prompt changed")
	}
	if a.fp.LiveZone != b.fp.LiveZone {
		t.Error("live zone fingerprint changed although the messages are identical")
	}
}

func TestSessionKeyIsolatesConversations(t *testing.T) {
	one := parseRequest(body(userMsg("first conversation")), "anthropic", "m", "1.1.1.1")
	two := parseRequest(body(userMsg("second conversation")), "anthropic", "m", "1.1.1.1")

	if one.session == two.session {
		t.Fatal("two conversations from the same client collapsed into one session key")
	}
}

func TestSessionKeyUsesUserID(t *testing.T) {
	a := parseRequest([]byte(`{"metadata":{"user_id":"u1"},"messages":[`+userMsg("x")+`]}`), "anthropic", "m", "1.1.1.1")
	b := parseRequest([]byte(`{"metadata":{"user_id":"u2"},"messages":[`+userMsg("x")+`]}`), "anthropic", "m", "1.1.1.1")
	if a.session == b.session {
		t.Fatal("different user_id values produced the same session key")
	}
}

func TestCountCacheControl(t *testing.T) {
	sys := []byte(`[{"type":"text","text":"a"},{"type":"text","text":"b","cache_control":{"type":"ephemeral"}}]`)
	if got := countCacheControl(sys); got != 1 {
		t.Errorf("system markers = %d, want 1", got)
	}

	tools := []byte(`[{"name":"t1","cache_control":{"type":"ephemeral"}},{"name":"t2"}]`)
	if got := countCacheControl(tools); got != 1 {
		t.Errorf("tools markers = %d, want 1", got)
	}

	msg := []byte(`{"role":"user","content":[{"type":"text","text":"x","cache_control":{"type":"ephemeral"}}]}`)
	if got := countCacheControlInMessage(msg); got != 1 {
		t.Errorf("message markers = %d, want 1", got)
	}

	if got := countCacheControl([]byte(`"plain string"`)); got != 0 {
		t.Errorf("string system markers = %d, want 0", got)
	}
}

func TestClassifyLadder(t *testing.T) {
	// The previous request carried exactly these two messages, so its history
	// chain is the fold of both. The appended case must replay the same first two
	// hashes to be recognised as growth rather than as a rewrite.
	prevHashes := [][32]byte{{7}, {8}}
	base := fingerprints{
		System:    [32]byte{1},
		Tools:     [32]byte{2},
		Prefix:    [32]byte{3},
		LiveZone:  [32]byte{4},
		FullChain: chainAt(prevHashes, len(prevHashes)),
	}
	prev := sessionState{fp: base, msgCount: len(prevHashes)}
	appended := func(extra [32]byte) [][32]byte {
		return append(append([][32]byte{}, prevHashes...), extra)
	}

	changed := func(mutate func(*fingerprints)) fingerprints {
		f := base
		mutate(&f)
		return f
	}

	tests := []struct {
		name  string
		known bool
		prev  sessionState
		cur   fingerprints
		count int
		hash  [][32]byte
		want  Outcome
	}{
		{"unknown session", false, sessionState{}, base, 2, nil, OutcomeFirstRequest},
		{"nothing changed", true, prev, base, 2, prevHashes, OutcomeIdentical},
		{
			"system changed", true, prev,
			changed(func(f *fingerprints) { f.System = [32]byte{9} }),
			2, prevHashes, OutcomeSystemChanged,
		},
		{
			"tools changed", true, prev,
			changed(func(f *fingerprints) { f.Tools = [32]byte{9} }),
			2, prevHashes, OutcomeToolsChanged,
		},
		{
			"live zone only", true, prev,
			changed(func(f *fingerprints) { f.LiveZone = [32]byte{9} }),
			2, prevHashes, OutcomeLiveZoneOnly,
		},
		{
			"appended", true, prev,
			changed(func(f *fingerprints) { f.Prefix = [32]byte{9} }),
			4, appended([32]byte{10}), OutcomeAppendOnly,
		},
		{
			"rewritten", true, prev,
			changed(func(f *fingerprints) { f.Prefix = [32]byte{9} }),
			2, [][32]byte{{7}, {99}}, OutcomeMessagesRewritten,
		},
		{
			"system wins over growth", true, prev,
			fingerprints{System: [32]byte{9}, Tools: [32]byte{2}, Prefix: [32]byte{9}, LiveZone: [32]byte{4}},
			4, [][32]byte{{7}, {8}, {10}, {11}}, OutcomeSystemChanged,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.prev, tc.known, tc.cur, tc.count, tc.hash)
			if got != tc.want {
				t.Errorf("classify = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestAttributeCrossesCountersWithClassification(t *testing.T) {
	tests := []struct {
		name     string
		outcome  Outcome
		tokens   usage.Tokens
		hasUsage bool
		want     Result
	}{
		{"first", OutcomeFirstRequest, usage.Tokens{}, true, ResultFirst},
		{"busted ignores a hit", OutcomeSystemChanged, usage.Tokens{CacheRead: 500}, true, ResultBusted},
		{"stable hit", OutcomeAppendOnly, usage.Tokens{CacheRead: 500}, true, ResultHit},
		{"stable rewrite", OutcomeAppendOnly, usage.Tokens{CacheWrite: 500}, true, ResultRewrite},
		{"stable no cache", OutcomeLiveZoneOnly, usage.Tokens{}, true, ResultNoCache},
		{"unknown usage is not no_cache", OutcomeAppendOnly, usage.Tokens{}, false, ResultNoUsage},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := attribute(tc.outcome, tc.tokens, tc.hasUsage); got != tc.want {
				t.Errorf("attribute = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSessionStoreEvictsLeastRecentlyUsed(t *testing.T) {
	s := newSessionStore(2, time.Hour)
	s.put("a", sessionState{msgCount: 1})
	s.put("b", sessionState{msgCount: 1})
	// Touch a so b becomes the oldest.
	if _, ok := s.get("a"); !ok {
		t.Fatal("a should be present")
	}
	s.put("c", sessionState{msgCount: 1})

	if _, ok := s.get("b"); ok {
		t.Error("b should have been evicted")
	}
	if _, ok := s.get("a"); !ok {
		t.Error("a should still be present after being touched")
	}
	if got := s.len(); got != 2 {
		t.Errorf("len = %d, want 2", got)
	}
}

func TestSessionStoreExpiresAfterTTL(t *testing.T) {
	s := newSessionStore(4, time.Minute)
	now := time.Now()
	s.now = func() time.Time { return now }

	s.put("a", sessionState{msgCount: 1})
	if _, ok := s.get("a"); !ok {
		t.Fatal("a should be present before the TTL")
	}

	now = now.Add(2 * time.Minute)
	if _, ok := s.get("a"); ok {
		t.Error("a should have expired")
	}
	if got := s.len(); got != 0 {
		t.Errorf("expired entry was not removed: len = %d", got)
	}
}

func TestScanVolatile(t *testing.T) {
	region := []byte(strings.Join([]string{
		`build at 2026-10-21T09:53:50Z`,           // timestamp (+ the date inside it)
		`id 123e4567-e89b-42d3-a456-426614174000`, // uuid v4
		`sha 0123456789abcdef0123456789abcdef`,    // long hex
		`release 2026-10-20 shipped`,              // plain date, not a timestamp
		`short deadbeef should not match`,         // too short
	}, "\n"))

	counts := scanVolatile(region)

	if counts[VolatileTimestamp] != 1 {
		t.Errorf("timestamps = %d, want 1", counts[VolatileTimestamp])
	}
	if counts[VolatileUUID] != 1 {
		t.Errorf("uuid = %d, want 1", counts[VolatileUUID])
	}
	if counts[VolatileLongHex] != 1 {
		t.Errorf("long hex = %d, want 1 (the uuid must not be counted twice)", counts[VolatileLongHex])
	}
	if counts[VolatileDate] != 1 {
		t.Errorf("plain dates = %d, want 1 (the date inside the timestamp must be skipped)", counts[VolatileDate])
	}
}

func TestScanVolatileCapsPerKind(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 50; i++ {
		sb.WriteString("2026-10-21T09:53:50Z\n")
	}
	counts := scanVolatile([]byte(sb.String()))
	if counts[VolatileTimestamp] != volatileCap {
		t.Errorf("timestamps = %d, want the cap %d", counts[VolatileTimestamp], volatileCap)
	}
}

func TestParseRequestSkips(t *testing.T) {
	if got := parseRequest([]byte("not json"), "anthropic", "m", "ip").skipped; got != skipUnparsable {
		t.Errorf("invalid JSON skip = %q, want %q", got, skipUnparsable)
	}
	if got := parseRequest(nil, "anthropic", "m", "ip").skipped; got != skipEmptyBody {
		t.Errorf("empty body skip = %q, want %q", got, skipEmptyBody)
	}

	huge := make([]byte, maxBodyBytes+1)
	for i := range huge {
		huge[i] = ' '
	}
	if got := parseRequest(huge, "anthropic", "m", "ip").skipped; got != skipBodyTooLarge {
		t.Errorf("oversized body skip = %q, want %q", got, skipBodyTooLarge)
	}
}

func TestCollectorRendersPrometheusAndEscapesLabels(t *testing.T) {
	col := NewCollector()
	col.Add(entry{
		surface:  "anthropic",
		model:    `evil"}\nmodel`,
		session:  "abcdef0123456789",
		outcome:  OutcomeSystemChanged,
		result:   ResultBusted,
		volatile: map[string]int{VolatileTimestamp: 3},
	})
	col.Add(entry{skipped: skipBodyTooLarge})

	out := col.Snapshot().Prometheus()

	for _, want := range []string{
		"# TYPE sap_proxy_cache_prefix_total counter",
		"# TYPE sap_proxy_cache_result_total counter",
		"# TYPE sap_proxy_cache_volatile_total counter",
		"# TYPE sap_proxy_cache_skipped_total counter",
		`sap_proxy_cache_result_total{surface="anthropic",model="evil\"}\\nmodel",result="busted"} 1`,
		`sap_proxy_cache_volatile_total{surface="anthropic",kind="iso_timestamp"} 3`,
		`sap_proxy_cache_skipped_total{reason="body_too_large"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("payload missing %q\n--- payload ---\n%s", want, out)
		}
	}

	// A raw newline inside a label value would break the exposition; the escaper
	// must have turned it into the two-character sequence. Escaped quotes are
	// removed before counting so they are not mistaken for delimiters.
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "sap_proxy_cache_result_total{") {
			continue
		}
		if strings.Count(strings.ReplaceAll(line, `\"`, ""), `"`)%2 != 0 {
			t.Errorf("unbalanced quotes in sample line %q", line)
		}
	}
}

// TestCollectorCountsSessionsThroughObserve covers the path the middleware
// actually takes: the counters ride on the session store, so they are only
// maintained by observe(). Add() alone records the aggregates.
func TestCollectorCountsSessionsThroughObserve(t *testing.T) {
	col := NewCollector()

	steady := parseRequest(body(userMsg("steady conversation")), "anthropic", "m", "ip")
	// First sight, then a repeat so the second one is classified against the
	// first rather than being first_request itself.
	col.observe(steady, usage.Tokens{CacheRead: 10}, true, "m")
	col.observe(steady, usage.Tokens{CacheRead: 10}, true, "m")

	drifted := parseRequest(
		[]byte(`{"system":"changed","messages":[`+userMsg("steady conversation")+`]}`),
		"anthropic", "m", "ip",
	)
	col.observe(drifted, usage.Tokens{CacheRead: 10}, true, "m")

	snap := col.Snapshot()
	if len(snap.TopSessions) != 1 {
		t.Fatalf("top sessions = %d, want 1", len(snap.TopSessions))
	}
	if got := snap.TopSessions[0].Requests; got != 3 {
		t.Errorf("session requests = %d, want 3", got)
	}
	if got := snap.TopSessions[0].Busted; got != 1 {
		t.Errorf("session busted = %d, want 1", got)
	}
	if len(snap.TopSessions[0].Key) != sessionKeyPrefix {
		t.Errorf("session key was not truncated: %q", snap.TopSessions[0].Key)
	}
}

// TestNoMarkerSeparatesOptingOutFromBeingIgnored is the disambiguation the
// marker count exists for: a client that never sent cache_control and a client
// whose markers the provider ignored both show a zero cache read, but only one
// of them can be fixed.
func TestNoMarkerSeparatesOptingOutFromBeingIgnored(t *testing.T) {
	col := NewCollector()

	// No marker at all: the client never opted in. The first sight of a session
	// is first_request, so three requests give two attributable ones.
	col.observe(parseRequest(body(userMsg("one")), "anthropic", "m", "ip"), usage.Tokens{}, true, "m")
	col.observe(parseRequest(body(userMsg("one")), "anthropic", "m", "ip"), usage.Tokens{}, true, "m")
	col.observe(parseRequest(body(userMsg("one")), "anthropic", "m", "ip"), usage.Tokens{}, true, "m")

	// Markers present, still no cache activity: that points at the provider.
	withMarkers := []byte(`{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}],` +
		`"messages":[` + userMsg("one") + `]}`)
	col.observe(parseRequest(withMarkers, "anthropic", "m", "ip"), usage.Tokens{}, true, "m")
	col.observe(parseRequest(withMarkers, "anthropic", "m", "ip"), usage.Tokens{}, true, "m")
	col.observe(parseRequest(withMarkers, "anthropic", "m", "ip"), usage.Tokens{}, true, "m")

	snap := col.Snapshot()
	var noMarker int64
	for _, r := range snap.NoMarker {
		noMarker += r.Count
	}
	if noMarker != 2 {
		t.Errorf("no_marker = %d, want 2 (only the requests that never sent a marker)", noMarker)
	}

	var noCache int64
	for _, r := range snap.ByResult {
		if r.Result == ResultNoCache {
			noCache += r.Count
		}
	}
	if noCache != 4 {
		t.Errorf("no_cache = %d, want 4 (both causes still land there for the raw count)", noCache)
	}
}

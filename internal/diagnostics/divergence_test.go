package diagnostics

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

// asstMsg mirrors userMsg for the other half of a conversation.
func asstMsg(text string) string {
	return `{"role":"assistant","content":"` + text + `"}`
}

// TestDivergenceWhereDistinguishesTailFromHistory pins the two answers the new
// label exists to separate. Both bodies below are classified messages_rewritten
// — the prefix moved in both — so only the divergence split tells them apart.
func TestDivergenceWhereDistinguishesTailFromHistory(t *testing.T) {
	// Three messages: the prefix is the fold of the first two, which is exactly
	// the state a session keeps between requests.
	prev := parseRequest(
		body(userMsg("one"), asstMsg("reply"), userMsg("two")),
		"anthropic", "m", "ip",
	)
	if prev.msgCount != 3 {
		t.Fatalf("prev msgCount = %d, want 3", prev.msgCount)
	}
	prevState := sessionState{fp: prev.fp, msgCount: prev.msgCount}

	tests := []struct {
		name     string
		messages []string
		want     string
	}{
		{
			// The previous last message was edited and the conversation went on.
			// Everything before it is untouched, so only the last turn is to
			// blame and the client can stop doing it.
			name:     "only the previous last message changed",
			messages: []string{userMsg("one"), asstMsg("reply"), userMsg("two edited"), asstMsg("ack")},
			want:     DivergenceTail,
		},
		{
			// A message inside the retained history changed. The whole cacheable
			// prefix behind it is being recomputed, which is the expensive case.
			name:     "an earlier message changed",
			messages: []string{userMsg("one"), asstMsg("reply edited"), userMsg("two edited"), asstMsg("ack")},
			want:     DivergenceHistory,
		},
		{
			// The history shrank: compaction rewrote it. It cannot be a tail-only
			// rewrite because the run being compared is no longer there.
			name:     "history shrank",
			messages: []string{userMsg("one")},
			want:     DivergenceHistory,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cur := parseRequest(body(tc.messages...), "anthropic", "m", "ip")
			// Guard the premise: the divergence split is only asked for after a
			// messages_rewritten outcome, so a case that classifies differently
			// would prove nothing.
			if got := classify(prevState, true, cur.fp, cur.msgCount, cur.msgHash); got != OutcomeMessagesRewritten {
				t.Fatalf("classify = %s, want %s", got, OutcomeMessagesRewritten)
			}
			if got := divergenceWhere(prevState, cur.msgCount, cur.msgHash); got != tc.want {
				t.Errorf("divergenceWhere = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDivergenceWhereRequiresAPrefix covers the guard: with fewer than two
// previous messages there is no stored prefix to compare against, so the split
// stays unclassified instead of guessing "history".
func TestDivergenceWhereRequiresAPrefix(t *testing.T) {
	prev := sessionState{fp: fingerprints{Prefix: [32]byte{7}}, msgCount: 1}
	hashes := [][32]byte{{1}, {2}, {3}}

	if got := divergenceWhere(prev, 3, hashes); got != "" {
		t.Errorf("divergenceWhere = %q for a one-message history, want an empty result", got)
	}

	// A conversation with no messages at all is equally unclassifiable.
	if got := divergenceWhere(sessionState{}, 3, hashes); got != "" {
		t.Errorf("divergenceWhere = %q for an empty history, want an empty result", got)
	}
}

// TestCollectorRecordsDivergenceSplit drives the wiring end to end: the split is
// computed inside observe(), carried on the entry, counted per (surface, model,
// where) and rendered in both payloads.
func TestCollectorRecordsDivergenceSplit(t *testing.T) {
	col := NewCollector()
	const surface, model = "anthropic", "m"

	prevBody := body(userMsg("one"), asstMsg("reply"), userMsg("two"))
	tailBody := body(userMsg("one"), asstMsg("reply"), userMsg("two edited"), asstMsg("ack"))
	historyBody := body(userMsg("one"), asstMsg("reply edited"), userMsg("two edited"), asstMsg("ack"))

	col.observe(parseRequest(prevBody, surface, model, "ip"), usage.Tokens{CacheRead: 10}, true, model)
	col.observe(parseRequest(tailBody, surface, model, "ip"), usage.Tokens{CacheRead: 0}, true, model)
	col.observe(parseRequest(historyBody, surface, model, "ip"), usage.Tokens{CacheRead: 0}, true, model)

	snap := col.Snapshot()
	got := map[string]int64{}
	for _, d := range snap.Divergence {
		if d.Surface != surface || d.Model != model {
			t.Errorf("unexpected labels on %+v", d)
		}
		got[d.Where] += d.Count
	}
	if got[DivergenceTail] != 1 {
		t.Errorf("divergence{where=tail} = %d, want 1", got[DivergenceTail])
	}
	if got[DivergenceHistory] != 1 {
		t.Errorf("divergence{where=history} = %d, want 1", got[DivergenceHistory])
	}
	if len(got) != 2 {
		t.Errorf("divergence has %d distinct where values, want 2: %+v", len(got), snap.Divergence)
	}

	// The split must account for exactly the rewrites, no more and no less, or
	// the two families would disagree about how many rewrites happened.
	var rewrites int64
	for _, o := range snap.ByOutcome {
		if o.Outcome == OutcomeMessagesRewritten {
			rewrites += o.Count
		}
	}
	var diverged int64
	for _, d := range snap.Divergence {
		diverged += d.Count
	}
	if diverged != rewrites {
		t.Errorf("divergence total = %d, want the %d messages_rewritten outcomes", diverged, rewrites)
	}

	out := snap.Prometheus()
	for _, want := range []string{
		"# HELP " + divergenceMetricName,
		"# TYPE " + divergenceMetricName + " counter",
		`sap_proxy_cache_divergence_total{surface="anthropic",model="m",where="tail"} 1`,
		`sap_proxy_cache_divergence_total{surface="anthropic",model="m",where="history"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("payload missing %q\n--- payload ---\n%s", want, out)
		}
	}
}

// TestCollectorDoesNotCountDivergenceForHealthyTraffic guards the opposite
// mistake: a healthy conversation must not produce a divergence series, because
// a non-zero tail count is read as "the client keeps rewriting its last turn".
func TestCollectorDoesNotCountDivergenceForHealthyTraffic(t *testing.T) {
	col := NewCollector()
	const surface, model = "anthropic", "m"

	grown := body(userMsg("one"), asstMsg("reply"), userMsg("two"))
	col.observe(parseRequest(body(userMsg("one")), surface, model, "ip"), usage.Tokens{CacheRead: 5}, true, model)
	col.observe(parseRequest(grown, surface, model, "ip"), usage.Tokens{CacheRead: 5}, true, model)

	snap := col.Snapshot()
	if len(snap.Divergence) != 0 {
		t.Errorf("divergence = %+v, want none for first_request plus append_only", snap.Divergence)
	}
	// The family header is still emitted so a scrape has a stable shape.
	if !strings.Contains(snap.Prometheus(), "# TYPE "+divergenceMetricName+" counter") {
		t.Error("the divergence family header disappeared when it had no series")
	}
}

// TestFormatMessageHashes pins the exact line format and its ordering. The
// format is what makes two consecutive requests diffable by eye, and it is also
// the contract that keeps content out of the log: index, role, eight hex
// characters, nothing else.
func TestFormatMessageHashes(t *testing.T) {
	hashes := [][32]byte{
		{0x1a, 0x2b, 0x3c, 0x4d, 0xff},
		{0xde, 0xad, 0xbe, 0xef, 0x00},
		{0x00},
	}
	roles := []string{"user", "assistant", "user"}

	got := formatMessageHashes(hashes, roles)
	want := []string{
		"0:user:1a2b3c4d",
		"1:assistant:deadbeef",
		"2:user:00000000",
	}
	if len(got) != len(want) {
		t.Fatalf("formatMessageHashes returned %d entries, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}

	// A short role slice must not drop messages: the index is what makes the line
	// usable, so a missing role is reported rather than the entry being skipped.
	short := formatMessageHashes(hashes, []string{"user"})
	if len(short) != len(hashes) {
		t.Fatalf("formatMessageHashes with a short role slice returned %d entries, want %d", len(short), len(hashes))
	}
	if short[1] != "1:"+roleUnknown+":deadbeef" {
		t.Errorf("entry 1 = %q, want the unknown-role placeholder", short[1])
	}
	if empty := formatMessageHashes(nil, nil); len(empty) != 0 {
		t.Errorf("formatMessageHashes(nil, nil) = %v, want empty", empty)
	}
}

// TestParseRequestLogsHashesWithoutContent is the safety check for the debug
// line: it must carry roles and digest prefixes, and it must not carry a single
// byte of message content.
func TestParseRequestLogsHashesWithoutContent(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)

	const secret = "SUPER-SECRET-PROMPT-TEXT"
	req := parseRequest(
		body(userMsg(secret), asstMsg(secret+"-reply")),
		"anthropic", "m", "ip",
	)
	if req.skipped != "" {
		t.Fatalf("unexpected skip: %s", req.skipped)
	}

	logged := buf.String()
	if logged == "" {
		t.Fatal("no debug line was emitted at debug level")
	}
	if !strings.Contains(logged, "cache message hashes") {
		t.Errorf("log line has the wrong message: %s", logged)
	}
	if !strings.Contains(logged, "0:user:") || !strings.Contains(logged, "1:assistant:") {
		t.Errorf("log line is missing the index:role:hash entries: %s", logged)
	}
	if strings.Contains(logged, secret) {
		t.Errorf("the debug line leaked message content: %s", logged)
	}
	if !strings.Contains(logged, `"count":2`) {
		t.Errorf("log line is missing the message count: %s", logged)
	}
	if !strings.Contains(logged, req.session) {
		t.Errorf("log line is missing the session key: %s", logged)
	}
}

// TestParseRequestIsSilentWhenDebugIsOff is the cost guard: with debug logging
// disabled the observation must emit nothing at all.
func TestParseRequestIsSilentWhenDebugIsOff(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(previous)

	parseRequest(body(userMsg("hello")), "anthropic", "m", "ip")

	if buf.Len() != 0 {
		t.Errorf("debug logging was off but the request still logged: %s", buf.String())
	}
}

package test

import (
	"net/http"
	"strings"
	"sync"
	"testing"
)

func (s cacheStats) skippedCount(reason string) int64 {
	var n int64
	for _, sk := range s.Skipped {
		if sk.Reason == reason {
			n += sk.Count
		}
	}
	return n
}

// TestCacheDiag_FiveStepLadder walks every rung of the classification in one
// conversation. The steps are chosen so each one differs from the previous by
// exactly one dimension, which is what makes a mis-ordered ladder visible.
func TestCacheDiag_FiveStepLadder(t *testing.T) {
	proxyURL, cleanup := setupMetrics(t)
	defer cleanup()

	msg := func(role, text string) string {
		return `{"role":"` + role + `","content":"` + text + `"}`
	}
	req := func(system string, messages ...string) string {
		sys := ""
		if system != "" {
			sys = `"system":"` + system + `",`
		}
		return `{"model":"claude-3-5-sonnet-latest","max_tokens":16,` + sys +
			`"messages":[` + strings.Join(messages, ",") + `]}`
	}

	u1, a1, u2 := msg("user", "one"), msg("assistant", "reply"), msg("user", "two")

	steps := []struct {
		name string
		body string
		want string
	}{
		{"first sight", req("", u1), "first_request"},
		{"append two messages", req("", u1, a1, u2), "append_only"},
		// Same message count, only the last message differs: the live zone moved
		// but the cacheable prefix did not.
		{"rewrite the last message", req("", u1, a1, msg("user", "two edited")), "live_zone_only"},
		// Same message count, an earlier message differs: the prefix was rewritten.
		// The edited message is the assistant reply rather than the first user
		// message on purpose: the session key contains the first message digest,
		// so editing that one looks like a different conversation (see
		// TestCacheDiag_EditingTheFirstMessageStartsANewSession).
		{"rewrite history", req("", u1, msg("assistant", "reply edited"), msg("user", "two edited")), "messages_rewritten"},
		{"change the system prompt", req("be terse", u1, msg("assistant", "reply edited"), msg("user", "two edited")), "system_changed"},
	}
	for i, step := range steps {
		postMessages(t, proxyURL, step.body)

		stats := getCacheStats(t, proxyURL)
		if got := stats.outcome(step.want); got != 1 {
			t.Errorf("step %d (%s): outcome %s = %d, want 1; all outcomes = %+v",
				i+1, step.name, step.want, got, stats.ByOutcome)
		}
	}

	// Every step must have produced exactly one attribution; a step that was
	// classified twice would mean the ladder ran more than once per request.
	stats := getCacheStats(t, proxyURL)
	var total int64
	for _, o := range stats.ByOutcome {
		total += o.Count
	}
	if total != int64(len(steps)) {
		t.Errorf("total outcomes = %d, want %d", total, len(steps))
	}
}

// TestCacheDiag_FailedRequestIsSkipped proves a failed request does not land in
// an attribution bucket. The mock has no deployment for this model, so the proxy
// answers 404 without ever calling upstream.
func TestCacheDiag_FailedRequestIsSkipped(t *testing.T) {
	proxyURL, cleanup := setupMetrics(t)
	defer cleanup()

	resp, err := http.Post(proxyURL+"/anthropic/v1/messages", "application/json",
		strings.NewReader(`{"model":"no-such-model","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected a failure status for an unknown model, got 200")
	}

	stats := getCacheStats(t, proxyURL)
	if got := stats.skippedCount("failed_request"); got != 1 {
		t.Errorf("skipped{failed_request} = %d, want 1; skipped = %+v", got, stats.Skipped)
	}
	var total int64
	for _, o := range stats.ByOutcome {
		total += o.Count
	}
	if total != 0 {
		t.Errorf("a failed request produced %d attributions, want 0", total)
	}
}

// TestCacheDiag_ConcurrentRequestsKeepOneSession exercises the shared session
// store under load. The attribution of each individual request is allowed to
// race (two parallel requests can both see the same predecessor), but the
// session count and the request total must stay exact.
func TestCacheDiag_ConcurrentRequestsKeepOneSession(t *testing.T) {
	proxyURL, cleanup := setupMetrics(t)
	defer cleanup()

	const n = 50
	body := `{"model":"claude-3-5-sonnet-latest","max_tokens":16,"messages":[{"role":"user","content":"concurrent"}]}`

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			resp, err := http.Post(proxyURL+"/anthropic/v1/messages", "application/json", strings.NewReader(body))
			if err != nil {
				return
			}
			_ = resp.Body.Close()
		}()
	}
	wg.Wait()

	stats := getCacheStats(t, proxyURL)
	if stats.TrackedSessions != 1 {
		t.Errorf("tracked sessions = %d, want 1", stats.TrackedSessions)
	}
	if len(stats.TopSessions) != 1 {
		t.Fatalf("top sessions = %d, want 1", len(stats.TopSessions))
	}
	if got := stats.TopSessions[0].Requests; got != n {
		t.Errorf("session requests = %d, want %d", got, n)
	}
}

// TestCacheDiag_EditingTheFirstMessageStartsANewSession pins a deliberate
// consequence of the session key: it contains the digest of the first message,
// which is what keeps two conversations from the same user apart. Editing that
// message therefore reads as a new conversation rather than as a rewrite. Real
// clients never edit it mid-conversation, and a client that rewrites its whole
// history (compaction) is starting over anyway.
func TestCacheDiag_EditingTheFirstMessageStartsANewSession(t *testing.T) {
	proxyURL, cleanup := setupMetrics(t)
	defer cleanup()

	first := `{"model":"claude-3-5-sonnet-latest","max_tokens":16,"messages":[{"role":"user","content":"original"}]}`
	edited := `{"model":"claude-3-5-sonnet-latest","max_tokens":16,"messages":[{"role":"user","content":"edited"}]}`

	postMessages(t, proxyURL, first)
	postMessages(t, proxyURL, edited)

	stats := getCacheStats(t, proxyURL)
	if stats.TrackedSessions != 2 {
		t.Errorf("tracked sessions = %d, want 2", stats.TrackedSessions)
	}
	if got := stats.outcome("first_request"); got != 2 {
		t.Errorf("first_request = %d, want 2", got)
	}
	if got := stats.outcome("messages_rewritten"); got != 0 {
		t.Errorf("messages_rewritten = %d, want 0: an edited first message is a new conversation, not a rewrite", got)
	}
}

// TestCacheDiag_ConcurrentFirstSightIsCountedOnce is a regression guard, not a
// reproduction. The sequence bug it guards against — classify running as
// get/classify/put with no lock around the three — could not be triggered even
// with 64 goroutines released from a barrier, because each store operation
// finishes before the scheduler moves on. The assertion holds with and without
// the lock, so it proves nothing about the lock; it would catch a far grosser
// regression such as classification being skipped entirely.
func TestCacheDiag_ConcurrentFirstSightIsCountedOnce(t *testing.T) {
	proxyURL, cleanup := setupMetrics(t)
	defer cleanup()

	const n = 20
	body := `{"model":"claude-3-5-sonnet-latest","max_tokens":16,"messages":[{"role":"user","content":"first sight under load"}]}`

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			resp, err := http.Post(proxyURL+"/anthropic/v1/messages", "application/json", strings.NewReader(body))
			if err != nil {
				return
			}
			_ = resp.Body.Close()
		}()
	}
	wg.Wait()

	stats := getCacheStats(t, proxyURL)
	if got := stats.outcome("first_request"); got > 1 {
		t.Errorf("first_request = %d, want at most 1: concurrent first sightings of one conversation must not double count", got)
	}
	var total int64
	for _, o := range stats.ByOutcome {
		total += o.Count
	}
	if total != n {
		t.Errorf("total outcomes = %d, want %d", total, n)
	}
}

// TestCacheDiag_DeepHistoryEditIsDetected pins the exact append-only chain: a
// rewrite 40 messages into a 60-message conversation must not be folded into the
// healthy bucket. The previous windowed comparison missed it, and busted is the
// signal this feature exists to report.
func TestCacheDiag_DeepHistoryEditIsDetected(t *testing.T) {
	proxyURL, cleanup := setupMetrics(t)
	defer cleanup()

	msg := func(role, text string) string {
		return `{"role":"` + role + `","content":"` + text + `"}`
	}
	const total = 60
	var history []string
	for i := 0; i < total; i++ {
		if i%2 == 0 {
			history = append(history, msg("user", "m"))
		} else {
			history = append(history, msg("assistant", "r"))
		}
	}

	post := func(text []string) {
		postMessages(t, proxyURL, `{"model":"claude-3-5-sonnet-latest","max_tokens":16,"messages":[`+
			strings.Join(text, ",")+`]}`)
	}
	post(history)
	post(append(append([]string{}, history...), msg("user", "next")))

	// Rewrite a message deep in the history AND grow the conversation, so the
	// message count still rises. Both are needed: without the growth the
	// curCount <= prev.msgCount guard already rejects append-only and the deep
	// edit path is never reached.
	edited := append([]string{}, history...)
	edited[40] = msg("assistant", "REWRITTEN")
	edited = append(edited, msg("user", "next"), msg("assistant", "ack"))
	post(edited)

	stats := getCacheStats(t, proxyURL)
	if got := stats.outcome("append_only"); got != 1 {
		t.Errorf("append_only = %d, want 1 (the growth step)", got)
	}
	if got := stats.outcome("messages_rewritten"); got != 1 {
		t.Errorf("messages_rewritten = %d, want 1: an edit at message 40 of 60 must be detected", got)
	}
	if got := stats.result("busted"); got != 1 {
		t.Errorf("busted = %d, want 1", got)
	}
}

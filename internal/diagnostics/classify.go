package diagnostics

import "github.com/shipengqi/sap-ai-core-proxy/internal/usage"

// Outcome is what changed since the previous request of the same conversation.
// Values are Prometheus label values, so they must stay stable.
type Outcome string

// The classification ladder, from most benign to most expensive.
const (
	// OutcomeFirstRequest means the conversation had no previous request, so
	// there is nothing to compare against.
	OutcomeFirstRequest = Outcome("first_request")
	// OutcomeIdentical means nothing changed at all. Rare in practice: a turn
	// normally appends the assistant reply and the next user message.
	OutcomeIdentical = Outcome("identical")
	// OutcomeLiveZoneOnly means only the last message changed. That is the
	// normal shape of a retry and never busts the cache.
	OutcomeLiveZoneOnly = Outcome("live_zone_only")
	// OutcomeSystemChanged is drift caused by the system prompt.
	OutcomeSystemChanged = Outcome("system_changed")
	// OutcomeToolsChanged is drift caused by the tool definitions.
	OutcomeToolsChanged = Outcome("tools_changed")
	// OutcomeAppendOnly means the conversation grew and the earlier messages are
	// untouched. This is the healthy main path and must never be reported as
	// drift, or every normal session would look like a cache buster.
	OutcomeAppendOnly = Outcome("append_only")
	// OutcomeMessagesRewritten means history was edited in place.
	OutcomeMessagesRewritten = Outcome("messages_rewritten")
)

// Result is the attribution: what the cache counters say, given the
// classification.
type Result string

// Attribution results. Values are Prometheus label values.
const (
	// ResultHit means the prefix was stable and the provider served it from
	// cache.
	ResultHit = Result("hit")
	// ResultRewrite means the prefix was stable but nothing was read and
	// something was written: the cache had expired (five-minute TTL) or the
	// request landed on a different backend. Not the client's fault, and not
	// something the proxy can fix.
	ResultRewrite = Result("rewrite")
	// ResultNoCache means the prefix was stable and the provider reported no
	// cache activity at all. Either the client sent no cache_control marker, or
	// the provider ignored the ones it sent; the marker count tells them apart.
	ResultNoCache = Result("no_cache")
	// ResultBusted means the prefix changed, so the provider had to recompute it.
	// This is the only result the client (or the proxy) can act on.
	ResultBusted = Result("busted")
	// ResultFirst is the first request of a conversation: nothing to hit yet.
	ResultFirst = Result("first")
	// ResultNoUsage means the prefix was classified but the upstream reported no
	// usage block, so there is nothing to cross it with. The classification is
	// still recorded; the attribution is simply unknown.
	ResultNoUsage = Result("no_usage")
)

// classify compares the current fingerprints with the previous request of the
// same conversation.
//
// Order matters. system and tools are checked before anything else because a
// change there is always the interesting answer, even when the message prefix
// also grew. live_zone_only is checked only after system, tools and prefix are
// known to be equal, which is exactly what "only the live zone changed" means.
func classify(prev sessionState, known bool, cur fingerprints, curCount int, curHashes [][32]byte) Outcome {
	if !known {
		return OutcomeFirstRequest
	}
	if cur.System != prev.fp.System {
		return OutcomeSystemChanged
	}
	if cur.Tools != prev.fp.Tools {
		return OutcomeToolsChanged
	}
	if cur.Prefix == prev.fp.Prefix && cur.LiveZone == prev.fp.LiveZone {
		return OutcomeIdentical
	}
	if cur.Prefix == prev.fp.Prefix {
		return OutcomeLiveZoneOnly
	}
	if isAppendOnly(prev, curCount, curHashes) {
		return OutcomeAppendOnly
	}
	return OutcomeMessagesRewritten
}

// isAppendOnly reports whether the current conversation extends the previous
// one without editing it.
//
// The comparison is exact, not windowed: the chain digest of the previous
// request's whole history is replayed over the first prevCount messages of the
// current request, so an edit anywhere in the retained history is detected. The
// earlier design compared only the first 32 message hashes, which let a rewrite
// beyond that window land in the healthy bucket — on a 200-message conversation
// that missed 84% of rewrites, and busted is the very signal this package exists
// to report.
//
// The cost is one extra pass over the current request's message hashes, which is
// linear in a length the caller already walked. No per-session storage grows
// with conversation length: only the chain digest is kept.
func isAppendOnly(prev sessionState, curCount int, curHashes [][32]byte) bool {
	if curCount <= prev.msgCount {
		return false
	}
	if prev.fp.FullChain == (fingerprints{}.FullChain) {
		// The previous request carried no messages at all, so there is no history
		// to extend.
		return false
	}
	return chainAt(curHashes, prev.msgCount) == prev.fp.FullChain
}

// attribute crosses the classification with the cache counters the upstream
// reported.
//
// This crossing is the whole point of the package: the classification alone
// cannot tell an expired cache from a busted one, and the counters alone cannot
// tell either from a client that never opted into caching.
func attribute(outcome Outcome, t usage.Tokens, hasUsage bool) Result {
	switch outcome {
	case OutcomeFirstRequest:
		return ResultFirst
	case OutcomeSystemChanged, OutcomeToolsChanged, OutcomeMessagesRewritten:
		return ResultBusted
	}
	if !hasUsage {
		// The upstream said nothing, so a zero cache_read means "unknown", not
		// "no cache". Reporting no_cache here would blame the client for a
		// measurement gap.
		return ResultNoUsage
	}
	if t.CacheRead > 0 {
		return ResultHit
	}
	if t.CacheWrite > 0 {
		return ResultRewrite
	}
	return ResultNoCache
}

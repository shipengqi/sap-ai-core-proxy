package diagnostics

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shipengqi/sap-ai-core-proxy/internal/catalogue"
)

const (
	// maxModelLabels mirrors the usage package's cardinality cap. Model names are
	// caller-controlled, so an unbounded set would let a client grow the payload.
	maxModelLabels    = 200
	otherModelLabel   = "__other__"
	unknownModelLabel = "unknown"
	// maxTopSessions bounds the per-session list in /stats/cache.
	maxTopSessions = 20
	// sessionKeyPrefix is how many hex characters of the session key are
	// published. Enough to correlate two requests by eye, short enough that it
	// cannot be used to reconstruct anything.
	sessionKeyPrefix = 8
)

type modelKey struct {
	surface string
	model   string
}

type volatileKey struct {
	surface string
	kind    string
}

type divergenceKey struct {
	modelKey
	where string
}

type outcomeSeries struct {
	key     modelKey
	outcome Outcome
	count   int64
}

type resultSeries struct {
	key    modelKey
	result Result
	count  int64
}

type sessionStat struct {
	key      string
	requests int64
	busted   int64
}

// Collector aggregates cache attribution for the lifetime of the process.
type Collector struct {
	mu       sync.Mutex
	since    time.Time
	models   map[string]struct{}
	outcome  map[outcomeKey]int64
	result   map[resultKey]int64
	volatile map[volatileKey]int64
	skipped  map[string]int64
	noMarker map[modelKey]int64
	// divergence splits messages_rewritten by where the prefix moved. It answers
	// the follow-up question the outcome alone cannot: was it the last message
	// (a client re-sending its final turn) or something deeper in the history
	// (thinking stripped, a reminder injected, compaction)?
	divergence map[divergenceKey]int64
	// sessions is both the LRU used for classification and the source of the
	// per-conversation counters, so there is one notion of "a conversation" and
	// one bound that retires both its state and its counters.
	sessions *sessionStore
}

type outcomeKey struct {
	modelKey
	outcome Outcome
}

type resultKey struct {
	modelKey
	result Result
}

// NewCollector returns an empty collector whose measurement window starts now.
func NewCollector() *Collector {
	return &Collector{
		since:      time.Now(),
		models:     make(map[string]struct{}),
		outcome:    make(map[outcomeKey]int64),
		result:     make(map[resultKey]int64),
		volatile:   make(map[volatileKey]int64),
		skipped:    make(map[string]int64),
		noMarker:   make(map[modelKey]int64),
		divergence: make(map[divergenceKey]int64),
		sessions:   newSessionStore(defaultSessionCapacity, defaultSessionTTL),
	}
}

// entry is one finished request as the middleware hands it over.
type entry struct {
	surface  string
	model    string
	session  string
	outcome  Outcome
	result   Result
	volatile map[string]int
	// markers counts explicit cache_control blocks the client sent. It is what
	// separates "the client never opted into caching" from "the provider ignored
	// the markers", which a zero cache_read alone cannot tell apart.
	markers int
	// divergence is DivergenceTail or DivergenceHistory when the request was
	// classified as messages_rewritten, and empty otherwise. Empty is not a
	// value of its own: a request that was not a rewrite has no divergence, and
	// counting it under a third label would make the family's total disagree
	// with the messages_rewritten outcome.
	divergence string
	skipped    string
}

// Add records one request. A skipped request only increments the skip counter:
// it carries no classification, and folding it into the outcome totals would
// make the denominator disagree with /stats.
func (c *Collector) Add(e entry) {
	if e.skipped != "" {
		c.mu.Lock()
		c.skipped[e.skipped]++
		c.mu.Unlock()
		return
	}
	if e.surface == "" {
		return
	}

	model := c.label(e.model)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.outcome[outcomeKey{modelKey{e.surface, model}, e.outcome}]++
	c.result[resultKey{modelKey{e.surface, model}, e.result}]++
	for kind, n := range e.volatile {
		if n > 0 {
			c.volatile[volatileKey{e.surface, kind}] += int64(n)
		}
	}

	// A stable prefix with markers and no cache activity means the client asked
	// for a cache and got none. A stable prefix without markers means it never
	// asked. Those need different follow-ups, so they are counted apart.
	if e.markers == 0 && e.result == ResultNoCache {
		c.noMarker[modelKey{e.surface, model}]++
	}

	// The divergence split is a property of the classification, not of the cache
	// counters, so it is recorded whenever the classification produced it.
	if e.divergence != "" {
		c.divergence[divergenceKey{modelKey{e.surface, model}, e.divergence}]++
	}
}

// label normalises a model name and enforces the cardinality cap. It takes the
// lock itself so callers can use it before the main critical section.
func (c *Collector) label(model string) string {
	model = catalogue.Normalize(model)
	if model == "" {
		return unknownModelLabel
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if model == otherModelLabel {
		return otherModelLabel
	}
	if _, ok := c.models[model]; ok {
		return model
	}
	if len(c.models) >= maxModelLabels {
		return otherModelLabel
	}
	c.models[model] = struct{}{}
	return model
}

// OutcomeSeries is one (surface, model, outcome) counter.
type OutcomeSeries struct {
	Surface string  `json:"surface"`
	Model   string  `json:"model"`
	Outcome Outcome `json:"outcome"`
	Count   int64   `json:"count"`
}

// ResultSeries is one (surface, model, result) counter.
type ResultSeries struct {
	Surface string `json:"surface"`
	Model   string `json:"model"`
	Result  Result `json:"result"`
	Count   int64  `json:"count"`
}

// VolatileSeries is one (surface, kind) volatile-pattern counter.
type VolatileSeries struct {
	Surface string `json:"surface"`
	Kind    string `json:"kind"`
	Count   int64  `json:"count"`
}

// SkippedSeries is one skip-reason counter.
type SkippedSeries struct {
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

// NoMarkerSeries counts requests whose client never opted into caching.
type NoMarkerSeries struct {
	Surface string `json:"surface"`
	Model   string `json:"model"`
	Count   int64  `json:"count"`
}

// DivergenceSeries is one (surface, model, where) divergence counter.
type DivergenceSeries struct {
	Surface string `json:"surface"`
	Model   string `json:"model"`
	Where   string `json:"where"`
	Count   int64  `json:"count"`
}

// SessionSeries is one conversation, identified only by a truncated key.
type SessionSeries struct {
	Key      string `json:"key"`
	Requests int64  `json:"requests"`
	Busted   int64  `json:"busted"`
}

// Snapshot is an immutable view backing both endpoints.
type Snapshot struct {
	Since         time.Time          `json:"-"`
	UptimeSeconds float64            `json:"uptime_seconds"`
	Sessions      int                `json:"tracked_sessions"`
	ByResult      []ResultSeries     `json:"by_result"`
	ByOutcome     []OutcomeSeries    `json:"by_outcome"`
	Volatile      []VolatileSeries   `json:"volatile"`
	Skipped       []SkippedSeries    `json:"skipped"`
	NoMarker      []NoMarkerSeries   `json:"no_marker"`
	Divergence    []DivergenceSeries `json:"divergence"`
	TopSessions   []SessionSeries    `json:"top_sessions"`
}

// Snapshot returns a consistent copy of all counters.
func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	snap := Snapshot{
		Since:         c.since,
		UptimeSeconds: time.Since(c.since).Seconds(),
		Sessions:      c.sessions.len(),
	}

	for k, n := range c.result {
		snap.ByResult = append(snap.ByResult, ResultSeries{
			Surface: k.surface, Model: k.model, Result: k.result, Count: n,
		})
	}
	for k, n := range c.outcome {
		snap.ByOutcome = append(snap.ByOutcome, OutcomeSeries{
			Surface: k.surface, Model: k.model, Outcome: k.outcome, Count: n,
		})
	}
	for k, n := range c.volatile {
		snap.Volatile = append(snap.Volatile, VolatileSeries{
			Surface: k.surface, Kind: k.kind, Count: n,
		})
	}
	for reason, n := range c.skipped {
		snap.Skipped = append(snap.Skipped, SkippedSeries{Reason: reason, Count: n})
	}
	for k, n := range c.noMarker {
		snap.NoMarker = append(snap.NoMarker, NoMarkerSeries{
			Surface: k.surface, Model: k.model, Count: n,
		})
	}
	for k, n := range c.divergence {
		snap.Divergence = append(snap.Divergence, DivergenceSeries{
			Surface: k.surface, Model: k.model, Where: k.where, Count: n,
		})
	}

	// The per-conversation list comes from the same store that bounds it, so an
	// evicted conversation takes its counters with it.
	snap.TopSessions = c.sessions.topStats(maxTopSessions)

	sort.Slice(snap.ByResult, func(i, j int) bool {
		return lessSeries(snap.ByResult[i].Surface, snap.ByResult[i].Model, string(snap.ByResult[i].Result),
			snap.ByResult[j].Surface, snap.ByResult[j].Model, string(snap.ByResult[j].Result))
	})
	sort.Slice(snap.ByOutcome, func(i, j int) bool {
		return lessSeries(snap.ByOutcome[i].Surface, snap.ByOutcome[i].Model, string(snap.ByOutcome[i].Outcome),
			snap.ByOutcome[j].Surface, snap.ByOutcome[j].Model, string(snap.ByOutcome[j].Outcome))
	})
	sort.Slice(snap.Volatile, func(i, j int) bool {
		if snap.Volatile[i].Surface != snap.Volatile[j].Surface {
			return snap.Volatile[i].Surface < snap.Volatile[j].Surface
		}
		return snap.Volatile[i].Kind < snap.Volatile[j].Kind
	})
	sort.Slice(snap.Skipped, func(i, j int) bool {
		return snap.Skipped[i].Reason < snap.Skipped[j].Reason
	})
	sort.Slice(snap.NoMarker, func(i, j int) bool {
		if snap.NoMarker[i].Surface != snap.NoMarker[j].Surface {
			return snap.NoMarker[i].Surface < snap.NoMarker[j].Surface
		}
		return snap.NoMarker[i].Model < snap.NoMarker[j].Model
	})
	sort.Slice(snap.Divergence, func(i, j int) bool {
		return lessSeries(snap.Divergence[i].Surface, snap.Divergence[i].Model, snap.Divergence[i].Where,
			snap.Divergence[j].Surface, snap.Divergence[j].Model, snap.Divergence[j].Where)
	})

	return snap
}

func lessSeries(as, am, al, bs, bm, bl string) bool {
	if as != bs {
		return as < bs
	}
	if am != bm {
		return am < bm
	}
	return al < bl
}

// metricsContentType is the Prometheus text exposition format.
const metricsContentType = "text/plain; version=0.0.4; charset=utf-8"

// divergenceMetricName is the family that splits messages_rewritten by where the
// prefix moved. It exists because the outcome alone cannot be acted on: a tail
// rewrite is a client re-sending its last turn, while a history rewrite means
// something inside the retained history changed and the whole cache behind it is
// being paid for again.
const divergenceMetricName = "sap_proxy_cache_divergence_total"

// divergenceHelp describes the family. It names the three causes a history
// divergence is likely to be, because that is the diagnosis this counter exists
// to hand to whoever reads the dashboard.
const divergenceHelp = "Where a rewritten message history diverged from the previous request of the same conversation. tail means only the previous request's last message was rewritten, so everything before it is still cacheable; history means a message inside the retained history changed, which is what thinking-block stripping, a system reminder injected into history, or compaction looks like. Only requests classified as messages_rewritten produce a series."

// labelEscaper matches the usage package's escaper exactly. The format defines
// only three escapes; emitting \r or \t would be an illegal escape sequence and
// would make the whole scrape fail.
var labelEscaper = strings.NewReplacer(
	`\`, `\\`,
	`"`, `\"`,
	"\n", `\n`,
)

// Prometheus renders the snapshot in the Prometheus text exposition format.
func (s Snapshot) Prometheus() string {
	var b strings.Builder

	writeHeader := func(name, help, kind string) {
		fmt.Fprintf(&b, "# HELP %s %s\n", name, help)
		fmt.Fprintf(&b, "# TYPE %s %s\n", name, kind)
	}
	writeSeries := func(name string, labels [][2]string, value int64) {
		b.WriteString(name)
		if len(labels) > 0 {
			b.WriteString("{")
			for i, l := range labels {
				if i > 0 {
					b.WriteString(",")
				}
				b.WriteString(l[0])
				b.WriteString(`="`)
				b.WriteString(labelEscaper.Replace(l[1]))
				b.WriteString(`"`)
			}
			b.WriteString("}")
		}
		b.WriteString(" ")
		b.WriteString(strconv.FormatInt(value, 10))
		b.WriteString("\n")
	}

	writeHeader(
		"sap_proxy_cache_prefix_total",
		"What changed in the cache hot zone since the previous request of the same conversation: identical, live_zone_only, append_only are healthy; system_changed, tools_changed and messages_rewritten are drift the client can fix.",
		"counter",
	)
	for _, r := range s.ByOutcome {
		writeSeries("sap_proxy_cache_prefix_total", [][2]string{
			{"surface", r.Surface}, {"model", r.Model}, {"outcome", string(r.Outcome)},
		}, r.Count)
	}

	writeHeader(
		"sap_proxy_cache_result_total",
		"Attribution of prompt-cache behaviour, crossing the prefix classification with the cache counters the upstream reported. busted means the prefix changed; rewrite means the prefix was stable but the cache had expired or the request landed elsewhere; no_cache means the upstream reported no cache activity at all.",
		"counter",
	)
	for _, r := range s.ByResult {
		writeSeries("sap_proxy_cache_result_total", [][2]string{
			{"surface", r.Surface}, {"model", r.Model}, {"result", string(r.Result)},
		}, r.Count)
	}

	writeHeader(
		"sap_proxy_cache_volatile_total",
		"Volatile patterns found in the cache hot zone (system, tools, message prefix). A non-zero value is a likely reason the prefix never matches: a timestamp or random id changes on every request.",
		"counter",
	)
	for _, r := range s.Volatile {
		writeSeries("sap_proxy_cache_volatile_total", [][2]string{
			{"surface", r.Surface}, {"kind", r.Kind},
		}, r.Count)
	}

	writeHeader(
		"sap_proxy_cache_skipped_total",
		"Requests the cache attribution did not examine, by reason. Skipping is not an error: the request was proxied normally.",
		"counter",
	)
	for _, r := range s.Skipped {
		writeSeries("sap_proxy_cache_skipped_total", [][2]string{{"reason", r.Reason}}, r.Count)
	}

	writeHeader(
		"sap_proxy_cache_no_marker_total",
		"Requests with a stable prefix that reported no cache activity and carried no cache_control marker at all. The client never opted into caching, so the provider had nothing to serve. Distinct from a request that sent markers and still saw no activity, which points at the provider.",
		"counter",
	)
	for _, r := range s.NoMarker {
		writeSeries("sap_proxy_cache_no_marker_total", [][2]string{
			{"surface", r.Surface}, {"model", r.Model},
		}, r.Count)
	}

	writeHeader(
		divergenceMetricName,
		divergenceHelp,
		"counter",
	)
	for _, r := range s.Divergence {
		writeSeries(divergenceMetricName, [][2]string{
			{"surface", r.Surface}, {"model", r.Model}, {"where", r.Where},
		}, r.Count)
	}

	return b.String()
}

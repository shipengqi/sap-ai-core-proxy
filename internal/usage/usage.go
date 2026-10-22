// Package usage accounts for the tokens the proxy forwards to SAP AI Core.
//
// The collector is strictly observation-only: it is fed from the response paths
// after the bytes have already been handed to the client, so it can never change
// what the proxy sends upstream or downstream. Nothing in this package wraps
// http.ResponseWriter — the streaming helpers type-assert http.Flusher, and a
// wrapper that forgets Flush() would silently break every SSE response.
//
// Accounting model (this is the part that keeps the numbers honest):
//
//   - Token observation happens on a per-request Accumulator that lives in the
//     request context. It never touches the shared Collector.
//   - The HTTP middleware calls Collector.Add exactly once per request, after
//     the handler returned, and that single call is the only place request and
//     token counters are incremented.
//
// Because tokens are accumulated per request and committed once, a provider that
// repeats a usage block (Anthropic message_delta does) or a chunk that is parsed
// twice cannot inflate the totals. The alternative — letting Observe* increment
// shared counters directly and then also adding Record.Tokens in the middleware
// — would double count on every streaming request.
package usage

import (
	"sort"
	"sync"
	"time"

	"github.com/shipengqi/sap-ai-core-proxy/internal/catalogue"
)

// Wire-format surface names. They are also the values of the "surface" label,
// so they must stay stable: dashboards and the one-week measurement window
// depend on them.
const (
	SurfaceAnthropic = "anthropic"
	SurfaceOpenAI    = "openai"
	SurfaceGemini    = "gemini"
	SurfaceLiteLLM   = "litellm"
)

const (
	// maxModelLabels caps the number of distinct "model" label values. Model
	// names are caller-controlled, so an unbounded set would let a client grow
	// the collector (and the /metrics payload) without limit. Overflow folds
	// into otherModelLabel.
	maxModelLabels = 200
	// otherModelLabel is the overflow bucket for the cardinality cap.
	otherModelLabel = "__other__"
	// unknownModelLabel is used when a handler never set a model (e.g. a
	// malformed request that was rejected before routing).
	unknownModelLabel = "unknown"
	// maxModelBreakdowns caps the /stats by_model list. The untruncated data
	// stays available through /metrics.
	maxModelBreakdowns = 50
)

// Tokens is the token accounting for one request, in the vocabulary shared by
// all four wire formats.
//
// CacheRead and CacheWrite are kept separate from Input on purpose: prompt-cache
// effectiveness cannot be measured if they are folded into the prompt count.
type Tokens struct {
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	CacheRead  int64 `json:"cache_read_input_tokens"`
	CacheWrite int64 `json:"cache_creation_input_tokens"`
}

// Add returns the element-wise sum of t and o.
func (t Tokens) Add(o Tokens) Tokens {
	return Tokens{
		Input:      t.Input + o.Input,
		Output:     t.Output + o.Output,
		CacheRead:  t.CacheRead + o.CacheRead,
		CacheWrite: t.CacheWrite + o.CacheWrite,
	}
}

// Total returns the billable token count reported by the upstream.
func (t Tokens) Total() int64 { return t.Input + t.Output }

// Record is one finished HTTP request as seen by the middleware.
type Record struct {
	Surface string
	Model   string
	Stream  bool
	Status  int
	Latency time.Duration
	Tokens  Tokens
	// HasUsage reports whether the upstream actually reported usage for this
	// request. False means the proxy had nothing to count, which is itself a
	// measurement we care about (see Aggregate.UsageMissing).
	HasUsage bool
	// Observed reports that a response path actually tried to read usage. It is
	// what separates "the upstream reported nothing" (UsageMissing) from "nobody
	// instrumented this endpoint" (Uninstrumented).
	Observed bool
	// NoUsageExpected reports that the endpoint cannot produce token counts at
	// all, so neither counter above applies.
	NoUsageExpected bool
	// CaptureDropped reports that usage existed but was lost by the proxy, not by
	// the upstream (the Gemini stream exceeded the capture cap).
	CaptureDropped bool
}

// Aggregate is a set of counters that can be rendered either as JSON (/stats)
// or as Prometheus series (/metrics).
type Aggregate struct {
	Requests      int64   `json:"requests"`
	Failures      int64   `json:"failures"`
	UsageMissing  int64   `json:"usage_missing"`
	Tokens        Tokens  `json:"tokens"`
	CacheHitRatio float64 `json:"cache_hit_ratio"`
	// Uninstrumented counts successful requests that no observation path looked
	// at. It exists so a forgotten Observe call shows up as its own number
	// instead of hiding inside UsageMissing.
	Uninstrumented int64 `json:"uninstrumented"`
	// UsageCaptureDropped counts requests whose usage the proxy discarded while
	// buffering (currently only oversized Gemini streams). Such a request is also
	// counted in UsageMissing: the number really is unknown.
	UsageCaptureDropped int64 `json:"usage_capture_dropped"`
}

// Breakdown is an Aggregate attributed to a surface and/or a model.
type Breakdown struct {
	Surface string `json:"surface,omitempty"`
	Model   string `json:"model,omitempty"`
	Aggregate
}

// RequestSeries is one (surface, model, status, stream) counter.
type RequestSeries struct {
	Surface string
	Model   string
	Status  int
	Stream  bool
	Count   int64
}

// DurationSeries is the per-surface request latency sum and count, which is what
// Prometheus needs to compute an average without a histogram.
type DurationSeries struct {
	Surface    string
	SumSeconds float64
	Count      int64
}

// Snapshot is an immutable view of the collector. It backs both endpoints so
// that /metrics and /stats can never disagree about the same request.
type Snapshot struct {
	Since         time.Time
	UptimeSeconds float64
	Totals        Aggregate
	BySurface     []Breakdown
	ByModel       []Breakdown
	Requests      []RequestSeries
	Durations     []DurationSeries
}

// aggregate is the mutable form of Aggregate.
type aggregate struct {
	requests     int64
	failures     int64
	usageMissing int64
	uninstr      int64
	captureDrops int64
	tokens       Tokens
}

// add folds other into a.
func (a *aggregate) add(other aggregate) {
	a.requests += other.requests
	a.failures += other.failures
	a.usageMissing += other.usageMissing
	a.uninstr += other.uninstr
	a.captureDrops += other.captureDrops
	a.tokens = a.tokens.Add(other.tokens)
}

// export converts a to its immutable form, computing the cache hit ratio with
// the denominator that is correct for the surface.
func (a *aggregate) export(surface string) Aggregate {
	return Aggregate{
		Requests:            a.requests,
		Failures:            a.failures,
		UsageMissing:        a.usageMissing,
		Tokens:              a.tokens,
		CacheHitRatio:       ratio(a.tokens.CacheRead, promptDenominator(surface, a.tokens)),
		Uninstrumented:      a.uninstr,
		UsageCaptureDropped: a.captureDrops,
	}
}

type requestKey struct {
	surface string
	model   string
	status  int
	stream  bool
}

type modelKey struct {
	surface string
	model   string
}

type durationAggregate struct {
	sumSeconds float64
	count      int64
}

// Collector aggregates request and token counts for the lifetime of the process.
// All methods are safe for concurrent use.
type Collector struct {
	mu        sync.Mutex
	since     time.Time
	requests  map[requestKey]int64
	models    map[string]struct{}
	byModel   map[modelKey]*aggregate
	durations map[string]*durationAggregate
}

// NewCollector returns an empty collector whose measurement window starts now.
func NewCollector() *Collector {
	return &Collector{
		since:     time.Now(),
		requests:  make(map[requestKey]int64),
		models:    make(map[string]struct{}),
		byModel:   make(map[modelKey]*aggregate),
		durations: make(map[string]*durationAggregate),
	}
}

// Add records one finished request. It is called exactly once per request by the
// middleware, which is what makes double counting impossible.
func (c *Collector) Add(r Record) {
	if r.Surface == "" {
		// Not an inference surface: health probes, /metrics, /stats, /debug.
		// Counting them would pollute both the token totals and the request
		// counters that are supposed to mirror the SAP bill.
		return
	}

	model := r.Model
	if model == "" {
		model = unknownModelLabel
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	model = c.labelLocked(model)

	c.requests[requestKey{surface: r.Surface, model: model, status: r.Status, stream: r.Stream}]++

	key := modelKey{surface: r.Surface, model: model}
	agg := c.byModel[key]
	if agg == nil {
		agg = &aggregate{}
		c.byModel[key] = agg
	}
	agg.requests++
	agg.tokens = agg.tokens.Add(r.Tokens)
	if r.Status >= 400 {
		agg.failures++
	} else {
		// The three outcomes below are mutually exclusive, which is the point:
		// a request is either unmeasured because nobody instrumented it, measured
		// but empty because the upstream said nothing, or correctly accounted for.
		switch {
		case r.NoUsageExpected:
			// Endpoints with no token concept: nothing to count, nothing missing.
		case !r.Observed:
			agg.uninstr++
		case !r.HasUsage:
			agg.usageMissing++
		}
	}
	if r.CaptureDropped {
		// Counted even on a failed request: the drop is a fact about our capture,
		// not about the upstream.
		agg.captureDrops++
	}

	d := c.durations[r.Surface]
	if d == nil {
		d = &durationAggregate{}
		c.durations[r.Surface] = d
	}
	d.sumSeconds += r.Latency.Seconds()
	d.count++
}

// labelLocked normalises a model name for use as a label value and enforces the
// cardinality cap. The caller must hold c.mu.
//
// Normalisation goes through catalogue so the label matches the SAP AI Core
// deployment name ("anthropic--claude-3.5-sonnet"), which is the name that
// appears on the bill.
func (c *Collector) labelLocked(model string) string {
	model = catalogue.Normalize(model)
	if model == "" {
		return unknownModelLabel
	}
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

// Snapshot returns a consistent copy of all counters.
func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	snap := Snapshot{
		Since:         c.since,
		UptimeSeconds: time.Since(c.since).Seconds(),
	}

	surfaces := make(map[string]*aggregate, len(c.durations))
	for key, agg := range c.byModel {
		snap.ByModel = append(snap.ByModel, Breakdown{
			Surface:   key.surface,
			Model:     key.model,
			Aggregate: agg.export(key.surface),
		})

		s := surfaces[key.surface]
		if s == nil {
			s = &aggregate{}
			surfaces[key.surface] = s
		}
		s.add(*agg)
	}

	// The blended cache hit ratio has to be summed as a fraction of prompt
	// tokens, not averaged over ratios, and each surface contributes its own
	// prompt-token denominator. See promptDenominator.
	var cacheRead, promptTokens int64
	for surface, agg := range surfaces {
		snap.BySurface = append(snap.BySurface, Breakdown{
			Surface:   surface,
			Aggregate: agg.export(surface),
		})
		cacheRead += agg.tokens.CacheRead
		promptTokens += promptDenominator(surface, agg.tokens)

		snap.Totals.Requests += agg.requests
		snap.Totals.Failures += agg.failures
		snap.Totals.UsageMissing += agg.usageMissing
		snap.Totals.Uninstrumented += agg.uninstr
		snap.Totals.UsageCaptureDropped += agg.captureDrops
		snap.Totals.Tokens = snap.Totals.Tokens.Add(agg.tokens)
	}
	snap.Totals.CacheHitRatio = ratio(cacheRead, promptTokens)

	for key, count := range c.requests {
		snap.Requests = append(snap.Requests, RequestSeries{
			Surface: key.surface,
			Model:   key.model,
			Status:  key.status,
			Stream:  key.stream,
			Count:   count,
		})
	}
	for surface, d := range c.durations {
		snap.Durations = append(snap.Durations, DurationSeries{
			Surface:    surface,
			SumSeconds: d.sumSeconds,
			Count:      d.count,
		})
	}

	// Sort everything so both endpoints are deterministic; tests and humans read
	// these by eye.
	sort.Slice(snap.ByModel, func(i, j int) bool {
		a, b := snap.ByModel[i], snap.ByModel[j]
		if at, bt := a.Tokens.Total(), b.Tokens.Total(); at != bt {
			return at > bt
		}
		if a.Requests != b.Requests {
			return a.Requests > b.Requests
		}
		if a.Surface != b.Surface {
			return a.Surface < b.Surface
		}
		return a.Model < b.Model
	})
	sort.Slice(snap.BySurface, func(i, j int) bool {
		a, b := snap.BySurface[i], snap.BySurface[j]
		if a.Requests != b.Requests {
			return a.Requests > b.Requests
		}
		return a.Surface < b.Surface
	})
	sort.Slice(snap.Requests, func(i, j int) bool {
		a, b := snap.Requests[i], snap.Requests[j]
		if a.Surface != b.Surface {
			return a.Surface < b.Surface
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		if a.Status != b.Status {
			return a.Status < b.Status
		}
		return !a.Stream && b.Stream
	})
	sort.Slice(snap.Durations, func(i, j int) bool {
		return snap.Durations[i].Surface < snap.Durations[j].Surface
	})

	return snap
}

// promptDenominator returns the total prompt-token count for a surface, which is
// the denominator of the cache hit ratio.
//
// The providers disagree about whether cached tokens are part of the prompt
// count, so a single formula would be wrong for half of them:
//
//   - Anthropic reports input_tokens *excluding* cache reads and cache writes,
//     so all three counters must be added to get the real prompt size.
//   - OpenAI reports prompt_tokens and Gemini reports promptTokenCount *including*
//     the cached tokens, so adding cache reads again would count them twice and
//     understate the hit rate.
func promptDenominator(surface string, t Tokens) int64 {
	if surface == SurfaceAnthropic {
		return t.Input + t.CacheRead + t.CacheWrite
	}
	return t.Input
}

// ratio guards against a zero denominator, which must read as "no data" rather
// than NaN (NaN would poison the JSON response and break dashboards).
func ratio(numerator, denominator int64) float64 {
	if numerator <= 0 || denominator <= 0 {
		return 0
	}
	r := float64(numerator) / float64(denominator)
	if r > 1 {
		// Only reachable when an upstream reports a cache read larger than its
		// own prompt count; clamp instead of emitting a nonsensical >100%.
		return 1
	}
	return r
}

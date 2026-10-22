package diagnostics

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/shipengqi/sap-ai-core-proxy/internal/catalogue"
)

// autoMarkerMetricName is the family this file owns. It answers one question
// only: did the automatic Claude cache marker actually reach the request?
const autoMarkerMetricName = "sap_proxy_cache_auto_marker_total"

// autoMarkerHelp describes the family. It spells out that a disabled feature
// publishes nothing, because an absent series and a zero series would otherwise
// be read as the same thing.
const autoMarkerHelp = "Requests the proxy considered for an automatic ephemeral cache_control marker on the final Claude tool declaration, by outcome. added means the marker was written into the request; skipped_client_marker means the client had already marked something and was left untouched; skipped_no_tools means there was no final tool declaration to mark. Nothing is published while the feature is disabled."

// AutoMarkerSeries is one (surface, model, result) auto-marker counter.
type AutoMarkerSeries struct {
	Surface string `json:"surface"`
	Model   string `json:"model"`
	Result  string `json:"result"`
	Count   int64  `json:"count"`
}

type autoMarkerKey struct {
	surface string
	model   string
	result  string
}

// autoMarkerCollector aggregates auto-marker outcomes for the process lifetime.
// It is deliberately tiny: one counter family, no window, no session state.
type autoMarkerCollector struct {
	mu     sync.Mutex
	series map[autoMarkerKey]int64
	// models bounds the model label set exactly like the cache collector does.
	// The label is a deployment model name, but it still comes from a list this
	// process did not author, so it gets the same cap.
	models map[string]struct{}
}

// defaultAutoMarker is the process-wide collector the handlers report into and
// the /metrics and /stats/cache endpoints read.
var defaultAutoMarker = newAutoMarkerCollector()

// newAutoMarkerCollector returns an empty collector.
func newAutoMarkerCollector() *autoMarkerCollector {
	return &autoMarkerCollector{
		series: make(map[autoMarkerKey]int64),
		models: make(map[string]struct{}),
	}
}

// ObserveAutoMarker records one request's auto-marker outcome.
//
// An empty surface or result is ignored: there is no meaningful series for
// either, and a disabled feature reports an empty result so that it stays
// invisible in /metrics exactly as it was before this counter existed.
func ObserveAutoMarker(surface, model, result string) {
	defaultAutoMarker.observe(surface, model, result)
}

// AutoMarkerSnapshot returns the counters sorted by surface, model, then result.
func AutoMarkerSnapshot() []AutoMarkerSeries {
	return defaultAutoMarker.snapshot()
}

// AutoMarkerMetricsText renders the family this file owns. The router appends
// it to the other payloads so /metrics stays one endpoint.
func AutoMarkerMetricsText() string {
	return defaultAutoMarker.metricsText()
}

// observe increments one counter.
func (c *autoMarkerCollector) observe(surface, model, result string) {
	if surface == "" || result == "" {
		return
	}
	label := c.modelLabel(model)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.series[autoMarkerKey{surface: surface, model: label, result: result}]++
}

// modelLabel normalises a model name and enforces the same cardinality cap as
// the cache collector, so this family and /stats/cache by_model share one model
// vocabulary and neither can grow without bound. It takes the lock itself so
// callers can use it before the main critical section.
func (c *autoMarkerCollector) modelLabel(model string) string {
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

// snapshot returns a consistent, sorted copy of the counters.
func (c *autoMarkerCollector) snapshot() []AutoMarkerSeries {
	c.mu.Lock()
	defer c.mu.Unlock()

	series := make([]AutoMarkerSeries, 0, len(c.series))
	for k, n := range c.series {
		series = append(series, AutoMarkerSeries{
			Surface: k.surface, Model: k.model, Result: k.result, Count: n,
		})
	}
	sort.Slice(series, func(i, j int) bool {
		return lessSeries(series[i].Surface, series[i].Model, series[i].Result,
			series[j].Surface, series[j].Model, series[j].Result)
	})
	return series
}

// metricsText renders the counters in the Prometheus text exposition format.
//
// With no observations it returns the empty string rather than a header with no
// series: a proxy that never enabled the feature keeps its /metrics payload
// byte-identical to what it was before the family existed.
func (c *autoMarkerCollector) metricsText() string {
	series := c.snapshot()
	if len(series) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# HELP %s %s\n", autoMarkerMetricName, autoMarkerHelp)
	fmt.Fprintf(&b, "# TYPE %s counter\n", autoMarkerMetricName)
	for _, s := range series {
		b.WriteString(autoMarkerMetricName)
		b.WriteString(`{surface="`)
		b.WriteString(labelEscaper.Replace(s.Surface))
		b.WriteString(`",model="`)
		b.WriteString(labelEscaper.Replace(s.Model))
		b.WriteString(`",result="`)
		b.WriteString(labelEscaper.Replace(s.Result))
		b.WriteString(`"} `)
		b.WriteString(strconv.FormatInt(s.Count, 10))
		b.WriteString("\n")
	}
	return b.String()
}

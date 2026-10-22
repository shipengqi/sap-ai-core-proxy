package diagnostics

import (
	"strings"
	"testing"
)

func TestAutoMarkerMetricsTextFormat(t *testing.T) {
	// An isolated collector keeps the exact-text assertion independent of what
	// other tests reported into the package-level one.
	c := newAutoMarkerCollector()
	c.observe("anthropic", "anthropic--claude-4.6-sonnet", "added")
	c.observe("anthropic", "anthropic--claude-4.6-sonnet", "added")
	c.observe("openai", "anthropic--claude-3.5-sonnet", "skipped_no_tools")

	got := c.metricsText()
	want := `# HELP sap_proxy_cache_auto_marker_total ` + autoMarkerHelp + "\n" +
		"# TYPE sap_proxy_cache_auto_marker_total counter\n" +
		`sap_proxy_cache_auto_marker_total{surface="anthropic",model="anthropic--claude-4.6-sonnet",result="added"} 2` + "\n" +
		`sap_proxy_cache_auto_marker_total{surface="openai",model="anthropic--claude-3.5-sonnet",result="skipped_no_tools"} 1` + "\n"
	if got != want {
		t.Errorf("metricsText() =\n%s\nwant:\n%s", got, want)
	}
}

func TestAutoMarkerMetricsTextEmptyWhenNothingObserved(t *testing.T) {
	c := newAutoMarkerCollector()
	if got := c.metricsText(); got != "" {
		t.Errorf("metricsText() = %q, want empty string", got)
	}

	// Observations that carry no surface or no result must not create a series:
	// a disabled feature reports an empty result and has to stay invisible.
	c.observe("", "anthropic--claude-4.6-sonnet", "added")
	c.observe("anthropic", "anthropic--claude-4.6-sonnet", "")
	if got := c.metricsText(); got != "" {
		t.Errorf("metricsText() = %q after ignored observations, want empty string", got)
	}
	if series := c.snapshot(); len(series) != 0 {
		t.Errorf("snapshot() = %v, want empty", series)
	}
}

func TestAutoMarkerSnapshotIsSortedBySurfaceModelResult(t *testing.T) {
	c := newAutoMarkerCollector()
	c.observe("openai", "anthropic--claude-4.6-sonnet", "added")
	c.observe("anthropic", "model-b", "added")
	c.observe("anthropic", "model-a", "skipped_no_tools")
	c.observe("anthropic", "model-a", "added")
	c.observe("anthropic", "model-a", "skipped_client_marker")

	got := c.snapshot()
	want := []AutoMarkerSeries{
		{Surface: "anthropic", Model: "model-a", Result: "added", Count: 1},
		{Surface: "anthropic", Model: "model-a", Result: "skipped_client_marker", Count: 1},
		{Surface: "anthropic", Model: "model-a", Result: "skipped_no_tools", Count: 1},
		{Surface: "anthropic", Model: "model-b", Result: "added", Count: 1},
		{Surface: "openai", Model: "anthropic--claude-4.6-sonnet", Result: "added", Count: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("snapshot() has %d series, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("snapshot()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	// The rendered text must follow the same order.
	text := c.metricsText()
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	var seriesLines []string
	for _, line := range lines {
		if strings.HasPrefix(line, autoMarkerMetricName+"{") {
			seriesLines = append(seriesLines, line)
		}
	}
	if len(seriesLines) != len(want) {
		t.Fatalf("rendered %d series lines, want %d:\n%s", len(seriesLines), len(want), text)
	}
	for i, series := range want {
		prefix := `{surface="` + series.Surface + `",model="` + series.Model + `",result="` + series.Result + `"}`
		if !strings.HasPrefix(seriesLines[i], autoMarkerMetricName+prefix) {
			t.Errorf("series line %d = %q, want prefix %q", i, seriesLines[i], autoMarkerMetricName+prefix)
		}
	}
}

func TestAutoMarkerMetricsTextEscapesLabels(t *testing.T) {
	c := newAutoMarkerCollector()
	c.observe("sur\"face\\x", "mo\ndel", "added")

	got := c.metricsText()
	want := `sap_proxy_cache_auto_marker_total{surface="sur\"face\\x",model="mo\ndel",result="added"} 1`
	if !strings.Contains(got, want) {
		t.Errorf("metricsText() =\n%s\nwant it to contain:\n%s", got, want)
	}
}

func TestObserveAutoMarkerUsesNormalisedModelLabel(t *testing.T) {
	// The package-level entry point must produce the same model vocabulary as
	// /stats/cache by_model, so a user-facing alias and its SAP-style deployment
	// name collapse into one series.
	const surface = "test-surface-normalisation"
	ObserveAutoMarker(surface, "claude-3-5-sonnet-latest", "added")
	ObserveAutoMarker(surface, "anthropic--claude-3.5-sonnet", "added")

	total := int64(0)
	for _, series := range AutoMarkerSnapshot() {
		if series.Surface != surface {
			continue
		}
		if series.Model != "anthropic--claude-3.5-sonnet" {
			t.Errorf("model label = %q, want the normalised deployment name", series.Model)
		}
		total += series.Count
	}
	if total != 2 {
		t.Errorf("total count = %d, want 2", total)
	}

	// An unknown model must still get a stable label rather than an empty one.
	ObserveAutoMarker(surface, "", "added")
	found := false
	for _, series := range AutoMarkerSnapshot() {
		if series.Surface == surface && series.Model == unknownModelLabel {
			found = true
		}
	}
	if !found {
		t.Errorf("no series with the %q fallback label:\n%v", unknownModelLabel, AutoMarkerSnapshot())
	}
}

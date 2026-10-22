package usage

import (
	"fmt"
	"math"
	"testing"
	"time"
)

func TestCollectorAggregates(t *testing.T) {
	c := NewCollector()

	c.Add(Record{
		Surface:  SurfaceAnthropic,
		Model:    "claude-3-5-sonnet-latest",
		Status:   200,
		Tokens:   Tokens{Input: 10, Output: 5, CacheRead: 5},
		HasUsage: true,
		Observed: true,
		Latency:  100 * time.Millisecond,
	})
	c.Add(Record{
		Surface:  SurfaceAnthropic,
		Model:    "claude-3-5-sonnet-latest",
		Status:   200,
		Stream:   true,
		Tokens:   Tokens{Input: 20, Output: 5},
		HasUsage: true,
		Observed: true,
		Latency:  300 * time.Millisecond,
	})
	// A successful request whose upstream reported no usage.
	c.Add(Record{Surface: SurfaceOpenAI, Model: "gpt-4o", Status: 200, Observed: true})
	// An upstream failure: counted as a failure, not as missing usage.
	c.Add(Record{Surface: SurfaceOpenAI, Model: "gpt-4o", Status: 500})
	// Not an inference surface: must be ignored entirely.
	c.Add(Record{Surface: "", Model: "ignored", Status: 200})

	snap := c.Snapshot()

	if snap.Totals.Requests != 4 {
		t.Errorf("total requests = %d, want 4", snap.Totals.Requests)
	}
	if snap.Totals.Failures != 1 {
		t.Errorf("total failures = %d, want 1", snap.Totals.Failures)
	}
	if snap.Totals.UsageMissing != 1 {
		t.Errorf("total usage_missing = %d, want 1 (only successful requests count)", snap.Totals.UsageMissing)
	}
	if snap.Totals.Uninstrumented != 0 {
		t.Errorf("total uninstrumented = %d, want 0: every request was observed", snap.Totals.Uninstrumented)
	}
	assertTokens(t, "totals", snap.Totals.Tokens, Tokens{Input: 30, Output: 10, CacheRead: 5})

	if len(snap.BySurface) != 2 {
		t.Fatalf("by_surface has %d entries, want 2: %+v", len(snap.BySurface), snap.BySurface)
	}
	anthropicSurface := snap.BySurface[0]
	if anthropicSurface.Surface != SurfaceAnthropic || anthropicSurface.Requests != 2 {
		t.Errorf("unexpected first surface entry: %+v", anthropicSurface)
	}
	// Anthropic input_tokens exclude cache reads, so the denominator is 30+5.
	if want := 5.0 / 35.0; math.Abs(anthropicSurface.CacheHitRatio-want) > 1e-9 {
		t.Errorf("anthropic cache_hit_ratio = %v, want %v", anthropicSurface.CacheHitRatio, want)
	}

	if len(snap.ByModel) != 2 {
		t.Fatalf("by_model has %d entries, want 2: %+v", len(snap.ByModel), snap.ByModel)
	}
	// Sorted by total tokens descending, and the label is the SAP deployment name.
	first := snap.ByModel[0]
	if first.Model != "anthropic--claude-3.5-sonnet" {
		t.Errorf("first model = %q, want the normalised SAP name", first.Model)
	}
	if first.Requests != 2 {
		t.Errorf("first model requests = %d, want 2", first.Requests)
	}
	assertTokens(t, "first model", first.Tokens, Tokens{Input: 30, Output: 10, CacheRead: 5})

	if len(snap.Requests) != 4 {
		t.Fatalf("request series = %d, want 4: %+v", len(snap.Requests), snap.Requests)
	}
	var streaming, nonStreaming int64
	for _, series := range snap.Requests {
		if series.Surface != SurfaceAnthropic || series.Status != 200 {
			continue
		}
		if series.Stream {
			streaming = series.Count
		} else {
			nonStreaming = series.Count
		}
	}
	if streaming != 1 || nonStreaming != 1 {
		t.Errorf("stream split = (%d, %d), want (1, 1)", streaming, nonStreaming)
	}

	if len(snap.Durations) != 2 {
		t.Fatalf("duration series = %d, want 2", len(snap.Durations))
	}
	for _, d := range snap.Durations {
		if d.Surface != SurfaceAnthropic {
			continue
		}
		if d.Count != 2 {
			t.Errorf("anthropic duration count = %d, want 2", d.Count)
		}
		if math.Abs(d.SumSeconds-0.4) > 1e-9 {
			t.Errorf("anthropic duration sum = %v, want 0.4", d.SumSeconds)
		}
	}
}

// TestCollectorModelCardinalityCap verifies that a client cannot grow the label
// set without bound: everything past the cap folds into "__other__".
func TestCollectorModelCardinalityCap(t *testing.T) {
	c := NewCollector()
	for i := 0; i < maxModelLabels+5; i++ {
		c.Add(Record{Surface: SurfaceOpenAI, Model: fmt.Sprintf("model-%03d", i), Status: 200, HasUsage: true})
	}

	snap := c.Snapshot()
	if len(snap.ByModel) != maxModelLabels+1 {
		t.Fatalf("by_model has %d entries, want %d distinct labels plus the overflow bucket",
			len(snap.ByModel), maxModelLabels)
	}

	var other Breakdown
	for _, entry := range snap.ByModel {
		if entry.Model == otherModelLabel {
			other = entry
		}
	}
	if other.Model != otherModelLabel {
		t.Fatal("expected an __other__ bucket")
	}
	if other.Requests != 5 {
		t.Errorf("__other__ requests = %d, want 5", other.Requests)
	}
	if snap.Totals.Requests != int64(maxModelLabels+5) {
		t.Errorf("total requests = %d, want %d", snap.Totals.Requests, maxModelLabels+5)
	}
}

// TestCacheHitRatio covers the two provider conventions and the zero-denominator
// case, which must read as 0 rather than NaN.
func TestCacheHitRatio(t *testing.T) {
	tests := []struct {
		name    string
		surface string
		tokens  Tokens
		want    float64
	}{
		{
			// Anthropic: input excludes cache reads, so all three counters sum.
			name:    "anthropic additive denominator",
			surface: SurfaceAnthropic,
			tokens:  Tokens{Input: 50, CacheRead: 50},
			want:    0.5,
		},
		{
			// OpenAI: prompt_tokens already include the cached ones.
			name:    "openai inclusive denominator",
			surface: SurfaceOpenAI,
			tokens:  Tokens{Input: 100, CacheRead: 50},
			want:    0.5,
		},
		{
			// Gemini: promptTokenCount already includes cachedContentTokenCount.
			name:    "gemini inclusive denominator",
			surface: SurfaceGemini,
			tokens:  Tokens{Input: 40, CacheRead: 10},
			want:    0.25,
		},
		{
			name:    "zero denominator",
			surface: SurfaceOpenAI,
			tokens:  Tokens{},
			want:    0,
		},
		{
			name:    "cache read without prompt",
			surface: SurfaceOpenAI,
			tokens:  Tokens{CacheRead: 5},
			want:    0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ratio(tc.tokens.CacheRead, promptDenominator(tc.surface, tc.tokens))
			if math.IsNaN(got) || math.IsInf(got, 0) {
				t.Fatalf("ratio is not a finite number: %v", got)
			}
			if math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("ratio = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCollectorUnknownModel makes sure a request that never reached a handler's
// model assignment still produces a labelled series instead of an empty one.
func TestCollectorUnknownModel(t *testing.T) {
	c := NewCollector()
	c.Add(Record{Surface: SurfaceLiteLLM, Status: 400})

	snap := c.Snapshot()
	if len(snap.ByModel) != 1 || snap.ByModel[0].Model != unknownModelLabel {
		t.Fatalf("expected a single %q series, got %+v", unknownModelLabel, snap.ByModel)
	}
	if snap.Totals.Failures != 1 {
		t.Errorf("failures = %d, want 1", snap.Totals.Failures)
	}
}

// TestCollectorInstrumentationBuckets pins the three mutually exclusive outcomes
// for a successful request, which is what keeps a forgotten Observe call visible
// instead of letting it hide inside usage_missing.
func TestCollectorInstrumentationBuckets(t *testing.T) {
	c := NewCollector()

	// Instrumented, upstream reported nothing.
	c.Add(Record{Surface: SurfaceOpenAI, Model: "gpt-4o", Status: 200, Observed: true})
	// Nobody looked at this endpoint.
	c.Add(Record{Surface: SurfaceOpenAI, Model: "gpt-4o", Status: 200})
	// Endpoint with no token concept at all.
	c.Add(Record{Surface: SurfaceOpenAI, Model: "dall-e-3", Status: 200, NoUsageExpected: true})
	// Instrumented and complete.
	c.Add(Record{
		Surface: SurfaceOpenAI, Model: "gpt-4o", Status: 200,
		Observed: true, HasUsage: true, Tokens: Tokens{Input: 4, Output: 1},
	})
	// Instrumented, but the capture was dropped: missing *and* attributable.
	c.Add(Record{Surface: SurfaceGemini, Model: "gemini-1.5-pro", Status: 200, Observed: true, CaptureDropped: true})
	// A failure is never counted in either bucket.
	c.Add(Record{Surface: SurfaceOpenAI, Model: "gpt-4o", Status: 500})

	snap := c.Snapshot()

	if snap.Totals.Requests != 6 {
		t.Fatalf("requests = %d, want 6", snap.Totals.Requests)
	}
	if snap.Totals.UsageMissing != 2 {
		t.Errorf("usage_missing = %d, want 2 (one silent upstream, one dropped capture)", snap.Totals.UsageMissing)
	}
	if snap.Totals.Uninstrumented != 1 {
		t.Errorf("uninstrumented = %d, want 1", snap.Totals.Uninstrumented)
	}
	if snap.Totals.UsageCaptureDropped != 1 {
		t.Errorf("usage_capture_dropped = %d, want 1", snap.Totals.UsageCaptureDropped)
	}
	if snap.Totals.Failures != 1 {
		t.Errorf("failures = %d, want 1", snap.Totals.Failures)
	}

	// The per-model breakdown must add up to the same numbers.
	var missing, uninstrumented int64
	for _, entry := range snap.ByModel {
		missing += entry.UsageMissing
		uninstrumented += entry.Uninstrumented
	}
	if missing != snap.Totals.UsageMissing || uninstrumented != snap.Totals.Uninstrumented {
		t.Errorf("by_model (%d/%d) disagrees with totals (%d/%d)",
			missing, uninstrumented, snap.Totals.UsageMissing, snap.Totals.Uninstrumented)
	}
}

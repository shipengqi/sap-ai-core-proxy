package usage

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestQA_AnthropicCumulativeOutput pins the rule that message_delta carries a
// cumulative output count: the last value wins, the values are never summed.
func TestQA_AnthropicCumulativeOutput(t *testing.T) {
	acc := &Accumulator{}
	acc.observeAnthropicEvent([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":4,"cache_creation_input_tokens":2,"output_tokens":1}}}`))
	acc.observeAnthropicEvent([]byte(`{"type":"message_delta","usage":{"output_tokens":5}}`))
	acc.observeAnthropicEvent([]byte(`{"type":"message_delta","usage":{"output_tokens":12}}`))

	tokens, hasUsage := acc.Tokens()
	want := Tokens{Input: 10, Output: 12, CacheRead: 4, CacheWrite: 2}
	if tokens != want {
		t.Errorf("tokens = %+v, want %+v (output 12, not 1+5+12)", tokens, want)
	}
	if !hasUsage {
		t.Error("hasUsage = false, want true")
	}
}

// TestQA_AnthropicDeltaCannotRewriteInput covers a message_delta that repeats —
// or worse, changes — the input-side counters after message_start. The
// authoritative message_start values must survive.
func TestQA_AnthropicDeltaCannotRewriteInput(t *testing.T) {
	acc := &Accumulator{}
	acc.observeAnthropicEvent([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":4,"cache_creation_input_tokens":2}}}`))
	// Same values again: must not double.
	acc.observeAnthropicEvent([]byte(`{"type":"message_delta","usage":{"input_tokens":10,"cache_read_input_tokens":4,"cache_creation_input_tokens":2,"output_tokens":12}}`))
	// Different values: must not overwrite the authoritative block either.
	acc.observeAnthropicEvent([]byte(`{"type":"message_delta","usage":{"input_tokens":999,"cache_read_input_tokens":999,"output_tokens":13}}`))

	tokens, _ := acc.Tokens()
	want := Tokens{Input: 10, Output: 13, CacheRead: 4, CacheWrite: 2}
	if tokens != want {
		t.Errorf("tokens = %+v, want %+v (input side must come from message_start only)", tokens, want)
	}
}

// TestQA_MiddlewareCommitsExactlyOnce is the root-cause test for double
// counting: a handler that observes the same usage several times must still
// produce one request and one set of tokens.
func TestQA_MiddlewareCommitsExactlyOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)

	col := NewCollector()
	r := gin.New()
	r.Use(Middleware(col))
	r.POST("/anthropic/v1/messages", func(c *gin.Context) {
		c.Set("model", "claude-3-5-sonnet-latest")
		MarkStream(c)
		// The same usage observed three times, as a re-sent or duplicated chunk
		// would produce.
		for i := 0; i < 3; i++ {
			ObserveAnthropicEvent(c, `{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":4}}}`)
			ObserveAnthropicEvent(c, `{"type":"message_delta","usage":{"output_tokens":7}}`)
		}
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	snap := col.Snapshot()
	if snap.Totals.Requests != 1 {
		t.Errorf("requests = %d, want 1", snap.Totals.Requests)
	}
	want := Tokens{Input: 10, Output: 7, CacheRead: 4}
	if snap.Totals.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", snap.Totals.Tokens, want)
	}
	if len(snap.Requests) != 1 {
		t.Errorf("request series = %d, want 1: %+v", len(snap.Requests), snap.Requests)
	}
	if len(snap.Requests) == 1 && !snap.Requests[0].Stream {
		t.Error("the request series must be marked as streaming")
	}
	if len(snap.Durations) != 1 || snap.Durations[0].Count != 1 {
		t.Errorf("duration series = %+v, want one surface counted once", snap.Durations)
	}
}

// TestQA_MiddlewareIgnoresNonPost asserts that a POST to a non-inference path and
// a GET to an inference path are both dropped.
func TestQA_MiddlewareIgnoresNonPost(t *testing.T) {
	gin.SetMode(gin.TestMode)

	col := NewCollector()
	r := gin.New()
	r.Use(Middleware(col))
	r.GET("/anthropic/v1/messages", func(c *gin.Context) {
		c.Set("model", "claude-3-5-sonnet-latest")
		ObserveJSONBody(c, []byte(`{"usage":{"input_tokens":10}}`))
		c.String(http.StatusOK, "ok")
	})
	r.POST("/health", func(c *gin.Context) {
		ObserveJSONBody(c, []byte(`{"usage":{"input_tokens":10}}`))
		c.String(http.StatusOK, "ok")
	})

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/anthropic/v1/messages"},
		{http.MethodPost, "/health"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}

	snap := col.Snapshot()
	if snap.Totals.Requests != 0 || snap.Totals.Tokens != (Tokens{}) || len(snap.ByModel) != 0 {
		t.Errorf("non-inference traffic was recorded: %+v", snap.Totals)
	}
}

// TestQA_ModelCardinalityCapWithTotals is the 210-model cardinality attack: the
// label set must stay bounded while the token totals stay exact.
func TestQA_ModelCardinalityCapWithTotals(t *testing.T) {
	const models = 210

	c := NewCollector()
	for i := 0; i < models; i++ {
		c.Add(Record{
			Surface:  SurfaceOpenAI,
			Model:    fmt.Sprintf("hostile-model-%03d", i),
			Status:   http.StatusOK,
			Tokens:   Tokens{Input: 3, Output: 2},
			HasUsage: true,
		})
	}

	snap := c.Snapshot()

	if len(snap.ByModel) > maxModelLabels+1 {
		t.Fatalf("by_model has %d entries, want at most %d", len(snap.ByModel), maxModelLabels+1)
	}
	if len(snap.ByModel) != maxModelLabels+1 {
		t.Errorf("by_model has %d entries, want %d distinct labels plus the overflow bucket", len(snap.ByModel), maxModelLabels)
	}

	var other *Breakdown
	for i := range snap.ByModel {
		if snap.ByModel[i].Model == otherModelLabel {
			other = &snap.ByModel[i]
		}
	}
	if other == nil {
		t.Fatal("no __other__ overflow bucket")
	}
	if want := int64(models - maxModelLabels); other.Requests != want {
		t.Errorf("__other__ requests = %d, want %d", other.Requests, want)
	}

	// Bounded labels must not lose tokens.
	wantTotals := Tokens{Input: 3 * models, Output: 2 * models}
	if snap.Totals.Tokens != wantTotals {
		t.Errorf("totals = %+v, want %+v", snap.Totals.Tokens, wantTotals)
	}
	if snap.Totals.Requests != models {
		t.Errorf("total requests = %d, want %d", snap.Totals.Requests, models)
	}

	// The Prometheus payload must stay bounded too: one series per label.
	if got, limit := len(snap.Requests), maxModelLabels+1; got > limit {
		t.Errorf("request series = %d, want at most %d", got, limit)
	}
}

// TestQA_RepeatedModelDoesNotGrowLabels guards the benign side of the cap: a hot
// model must keep exactly one label.
func TestQA_RepeatedModelDoesNotGrowLabels(t *testing.T) {
	c := NewCollector()
	for i := 0; i < 1000; i++ {
		c.Add(Record{Surface: SurfaceAnthropic, Model: "claude-3-5-sonnet-latest", Status: 200, HasUsage: true})
	}

	snap := c.Snapshot()
	if len(snap.ByModel) != 1 {
		t.Errorf("by_model has %d entries, want 1", len(snap.ByModel))
	}
	if snap.Totals.Requests != 1000 {
		t.Errorf("requests = %d, want 1000", snap.Totals.Requests)
	}
	if len(snap.Requests) != 1 {
		t.Errorf("request series = %d, want 1", len(snap.Requests))
	}
}

// TestQA_ObservationHelpersTolerateMissingContext pins the "observability can
// never fail a request" contract at the API boundary, including a nil context.
func TestQA_ObservationHelpersTolerateMissingContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)

	// No accumulator in the context: every helper must no-op.
	MarkStream(c)
	ObserveAnthropicEvent(c, `{"usage":{"input_tokens":1}}`)
	ObserveOpenAIChunk(c, `{"usage":{"prompt_tokens":1}}`)
	ObserveGeminiChunk(c, []byte(`{"usageMetadata":{"promptTokenCount":1}}`))
	ObserveGeminiStream(c, []byte(`[{"usageMetadata":{"promptTokenCount":1}}]`))
	ObserveJSONBody(c, []byte(`{"usage":{"input_tokens":1}}`))

	// A nil context must not panic either.
	MarkStream(nil)
	ObserveJSONBody(nil, []byte(`{"usage":{"input_tokens":1}}`))
	ObserveAnthropicEvent(nil, `{"usage":{"input_tokens":1}}`)

	if got := accumulator(c); got != nil {
		t.Errorf("accumulator() = %+v, want nil when the middleware is absent", got)
	}
}

// TestQA_InstrumentationBucketsAreMutuallyExclusive pins the three-way split at
// the Collector level. Every successful request must land in exactly one bucket,
// and a failure must land in none of them.
func TestQA_InstrumentationBucketsAreMutuallyExclusive(t *testing.T) {
	tests := []struct {
		name      string
		record    Record
		wantMiss  int64
		wantUnin  int64
		wantFail  int64
		wantDrops int64
	}{
		{
			name:     "nobody observed it",
			record:   Record{Surface: SurfaceOpenAI, Model: "m", Status: 200},
			wantUnin: 1,
		},
		{
			name:     "observed but the upstream said nothing",
			record:   Record{Surface: SurfaceOpenAI, Model: "m", Status: 200, Observed: true},
			wantMiss: 1,
		},
		{
			name:   "observed with usage",
			record: Record{Surface: SurfaceOpenAI, Model: "m", Status: 200, Observed: true, HasUsage: true, Tokens: Tokens{Input: 3}},
		},
		{
			name:   "no token concept at all",
			record: Record{Surface: SurfaceOpenAI, Model: "m", Status: 200, NoUsageExpected: true},
		},
		{
			name:   "no token concept wins over observed",
			record: Record{Surface: SurfaceOpenAI, Model: "m", Status: 200, NoUsageExpected: true, Observed: true},
		},
		{
			name:      "dropped capture is missing and attributed",
			record:    Record{Surface: SurfaceGemini, Model: "m", Status: 200, Observed: true, CaptureDropped: true},
			wantMiss:  1,
			wantDrops: 1,
		},
		{
			name:     "a failure is in no bucket",
			record:   Record{Surface: SurfaceOpenAI, Model: "m", Status: 500},
			wantFail: 1,
		},
		{
			name:      "a failed request still records a dropped capture",
			record:    Record{Surface: SurfaceGemini, Model: "m", Status: 502, Observed: true, CaptureDropped: true},
			wantFail:  1,
			wantDrops: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewCollector()
			c.Add(tc.record)
			snap := c.Snapshot()

			if snap.Totals.Requests != 1 {
				t.Errorf("requests = %d, want 1", snap.Totals.Requests)
			}
			if snap.Totals.UsageMissing != tc.wantMiss {
				t.Errorf("usage_missing = %d, want %d", snap.Totals.UsageMissing, tc.wantMiss)
			}
			if snap.Totals.Uninstrumented != tc.wantUnin {
				t.Errorf("uninstrumented = %d, want %d", snap.Totals.Uninstrumented, tc.wantUnin)
			}
			if snap.Totals.Failures != tc.wantFail {
				t.Errorf("failures = %d, want %d", snap.Totals.Failures, tc.wantFail)
			}
			if snap.Totals.UsageCaptureDropped != tc.wantDrops {
				t.Errorf("usage_capture_dropped = %d, want %d", snap.Totals.UsageCaptureDropped, tc.wantDrops)
			}
			// Mutual exclusivity: a successful request contributes to at most one of
			// the three outcome counters.
			buckets := snap.Totals.UsageMissing + snap.Totals.Uninstrumented
			if tc.wantFail == 0 && buckets > 1 {
				t.Errorf("a single success landed in %d outcome buckets", buckets)
			}
			if tc.wantFail == 1 && buckets != 0 {
				t.Errorf("a failure also landed in an outcome bucket (%d)", buckets)
			}
		})
	}
}

// TestQA_ReconciliationIdentity pins the arithmetic invariant the two endpoints
// rely on: requests == failures + successes, and every success is accounted for.
func TestQA_ReconciliationIdentity(t *testing.T) {
	c := NewCollector()

	// 10 successes: 4 with usage, 3 silently missing, 2 uninstrumented, 1 no-usage.
	for i := 0; i < 4; i++ {
		c.Add(Record{Surface: SurfaceOpenAI, Model: "m", Status: 200, Observed: true, HasUsage: true, Tokens: Tokens{Input: 1}})
	}
	for i := 0; i < 3; i++ {
		c.Add(Record{Surface: SurfaceOpenAI, Model: "m", Status: 200, Observed: true})
	}
	for i := 0; i < 2; i++ {
		c.Add(Record{Surface: SurfaceOpenAI, Model: "m", Status: 200})
	}
	c.Add(Record{Surface: SurfaceOpenAI, Model: "m", Status: 200, NoUsageExpected: true})
	// 2 failures.
	c.Add(Record{Surface: SurfaceOpenAI, Model: "m", Status: 429})
	c.Add(Record{Surface: SurfaceAnthropic, Model: "claude-3-5-sonnet-latest", Status: 502})

	snap := c.Snapshot()

	const wantRequests, wantFailures = 12, 2
	if snap.Totals.Requests != wantRequests {
		t.Fatalf("requests = %d, want %d", snap.Totals.Requests, wantRequests)
	}
	if snap.Totals.Failures != wantFailures {
		t.Fatalf("failures = %d, want %d", snap.Totals.Failures, wantFailures)
	}

	successes := snap.Totals.Requests - snap.Totals.Failures
	if successes != 10 {
		t.Fatalf("successes = %d, want 10", successes)
	}

	const noUsageExpected = 1
	withUsage := successes - snap.Totals.UsageMissing - snap.Totals.Uninstrumented - noUsageExpected
	if withUsage != 4 {
		t.Errorf("with-usage successes = %d, want 4 (successes %d - missing %d - uninstrumented %d - noUsageExpected %d)",
			withUsage, successes, snap.Totals.UsageMissing, snap.Totals.Uninstrumented, noUsageExpected)
	}
	if snap.Totals.UsageMissing != 3 || snap.Totals.Uninstrumented != 2 {
		t.Errorf("usage_missing=%d uninstrumented=%d, want 3/2", snap.Totals.UsageMissing, snap.Totals.Uninstrumented)
	}
	// The four with-usage requests each carried one input token.
	if snap.Totals.Tokens.Input != 4 {
		t.Errorf("input tokens = %d, want 4", snap.Totals.Tokens.Input)
	}

	// The per-surface split must reconcile to the same totals.
	var requests, failures, missing, uninstrumented int64
	for _, s := range snap.BySurface {
		requests += s.Requests
		failures += s.Failures
		missing += s.UsageMissing
		uninstrumented += s.Uninstrumented
	}
	if requests != snap.Totals.Requests || failures != snap.Totals.Failures ||
		missing != snap.Totals.UsageMissing || uninstrumented != snap.Totals.Uninstrumented {
		t.Errorf("by_surface (%d/%d/%d/%d) disagrees with totals (%d/%d/%d/%d)",
			requests, failures, missing, uninstrumented,
			snap.Totals.Requests, snap.Totals.Failures, snap.Totals.UsageMissing, snap.Totals.Uninstrumented)
	}
}

package usage

import (
	"net/http"
	"testing"
)

func assertTokens(t *testing.T, label string, got, want Tokens) {
	t.Helper()
	if got != want {
		t.Errorf("%s: tokens = %+v, want %+v", label, got, want)
	}
}

// TestAccumulatorAnthropicStream covers the two-event Anthropic usage flow:
// message_start carries the prompt side, message_delta the completion side.
func TestAccumulatorAnthropicStream(t *testing.T) {
	acc := &Accumulator{}
	acc.observeAnthropicEvent([]byte(`{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":100,"cache_read_input_tokens":40,"cache_creation_input_tokens":10,"output_tokens":1}}}`))
	acc.observeAnthropicEvent([]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`))
	acc.observeAnthropicEvent([]byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":25}}`))

	tokens, hasUsage := acc.Tokens()
	assertTokens(t, "stream", tokens, Tokens{Input: 100, Output: 25, CacheRead: 40, CacheWrite: 10})
	if !hasUsage {
		t.Error("expected hasUsage to be true")
	}
}

// TestAccumulatorAnthropicRepeatedDeltaIsIdempotent guards the double-counting
// hazard: some upstreams repeat the input counters on message_delta, and a naive
// implementation would add them to the prompt a second time.
func TestAccumulatorAnthropicRepeatedDeltaIsIdempotent(t *testing.T) {
	acc := &Accumulator{}
	acc.observeAnthropicEvent([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":100,"cache_read_input_tokens":40,"cache_creation_input_tokens":10}}}`))
	for i := 0; i < 3; i++ {
		acc.observeAnthropicEvent([]byte(`{"type":"message_delta","usage":{"input_tokens":100,"cache_read_input_tokens":40,"output_tokens":25}}`))
	}

	tokens, _ := acc.Tokens()
	assertTokens(t, "repeated delta", tokens, Tokens{Input: 100, Output: 25, CacheRead: 40, CacheWrite: 10})
}

// TestAccumulatorAnthropicDeltaWithoutStart covers upstreams that skip
// message_start: the input counters must still be picked up from message_delta.
func TestAccumulatorAnthropicDeltaWithoutStart(t *testing.T) {
	acc := &Accumulator{}
	acc.observeAnthropicEvent([]byte(`{"type":"message_delta","usage":{"input_tokens":7,"output_tokens":3}}`))

	tokens, hasUsage := acc.Tokens()
	assertTokens(t, "delta only", tokens, Tokens{Input: 7, Output: 3})
	if !hasUsage {
		t.Error("expected hasUsage to be true")
	}
}

// TestAccumulatorAnthropicTolerantDecoding covers the field shapes that must not
// break the parse: null, floats, quoted numbers and absent fields.
func TestAccumulatorAnthropicTolerantDecoding(t *testing.T) {
	acc := &Accumulator{}
	acc.observeAnthropicEvent([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":null,"output_tokens":1.0,"cache_read_input_tokens":0,"cache_creation_input_tokens":null}}}`))
	acc.observeAnthropicEvent([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":"42"}}}`))

	tokens, hasUsage := acc.Tokens()
	assertTokens(t, "tolerant", tokens, Tokens{Input: 42, Output: 1})
	if !hasUsage {
		t.Error("expected hasUsage to be true")
	}
}

// TestAccumulatorAnthropicNoUsage verifies that an event without a usage block
// leaves the accumulator untouched, so usage_missing stays accurate.
func TestAccumulatorAnthropicNoUsage(t *testing.T) {
	acc := &Accumulator{}
	acc.observeAnthropicEvent([]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
	acc.observeAnthropicEvent([]byte(`not json at all`))

	tokens, hasUsage := acc.Tokens()
	assertTokens(t, "no usage", tokens, Tokens{})
	if hasUsage {
		t.Error("expected hasUsage to be false")
	}
}

// TestAccumulatorOpenAIChunks covers the final-chunk usage block, including the
// "usage": null that OpenAI sends on every intermediate chunk.
func TestAccumulatorOpenAIChunks(t *testing.T) {
	acc := &Accumulator{}
	acc.observeOpenAIChunk([]byte(`{"id":"chatcmpl-1","choices":[{"delta":{"content":"hi"}}],"usage":null}`))

	if _, hasUsage := acc.Tokens(); hasUsage {
		t.Fatal(`"usage": null must not count as reported usage`)
	}

	acc.observeOpenAIChunk([]byte(`{"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25,"prompt_tokens_details":{"cached_tokens":12}}}`))

	tokens, hasUsage := acc.Tokens()
	assertTokens(t, "openai", tokens, Tokens{Input: 20, Output: 5, CacheRead: 12})
	if !hasUsage {
		t.Error("expected hasUsage to be true")
	}
}

// TestAccumulatorGeminiMetadata covers usageMetadata, including the thinking
// tokens that Gemini reports outside candidatesTokenCount but bills as output.
func TestAccumulatorGeminiMetadata(t *testing.T) {
	acc := &Accumulator{}
	acc.observeGeminiChunk([]byte(`{"candidates":[],"usageMetadata":{"promptTokenCount":30,"candidatesTokenCount":7,"cachedContentTokenCount":8,"thoughtsTokenCount":3}}`))

	tokens, hasUsage := acc.Tokens()
	assertTokens(t, "gemini", tokens, Tokens{Input: 30, Output: 10, CacheRead: 8})
	if !hasUsage {
		t.Error("expected hasUsage to be true")
	}
}

// TestAccumulatorGeminiStreamArray covers the buffered streamGenerateContent
// body, whose chunks each carry a cumulative usageMetadata block.
func TestAccumulatorGeminiStreamArray(t *testing.T) {
	acc := &Accumulator{}
	acc.observeGeminiStream([]byte(`[{"candidates":[{"content":{"parts":[{"text":"a"}]}}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":2}},{"candidates":[{"content":{"parts":[{"text":"b"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":9,"cachedContentTokenCount":4}}]`))

	tokens, _ := acc.Tokens()
	assertTokens(t, "gemini stream", tokens, Tokens{Input: 11, Output: 9, CacheRead: 4})
}

// TestAccumulatorGeminiStreamTruncated verifies that an unparseable capture is
// ignored instead of producing a partial number.
func TestAccumulatorGeminiStreamTruncated(t *testing.T) {
	acc := &Accumulator{}
	acc.observeGeminiStream([]byte(`[{"usageMetadata":{"promptTokenCount":11`))

	tokens, hasUsage := acc.Tokens()
	assertTokens(t, "truncated", tokens, Tokens{})
	if hasUsage {
		t.Error("expected hasUsage to be false for an unparseable capture")
	}
}

// TestAccumulatorJSONBodySniffing covers the non-streaming bodies, which arrive
// in whichever wire format the surface returned (possibly converted).
func TestAccumulatorJSONBodySniffing(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		want     Tokens
		wantSeen bool
	}{
		{
			name:     "anthropic",
			body:     `{"type":"message","usage":{"input_tokens":10,"output_tokens":8,"cache_read_input_tokens":4,"cache_creation_input_tokens":2}}`,
			want:     Tokens{Input: 10, Output: 8, CacheRead: 4, CacheWrite: 2},
			wantSeen: true,
		},
		{
			name:     "openai",
			body:     `{"object":"chat.completion","usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":3}}}`,
			want:     Tokens{Input: 10, Output: 5, CacheRead: 3},
			wantSeen: true,
		},
		{
			name:     "gemini",
			body:     `{"candidates":[],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":6,"totalTokenCount":18}}`,
			want:     Tokens{Input: 12, Output: 6},
			wantSeen: true,
		},
		{
			name:     "no usage",
			body:     `{"candidates":[],"error":{"message":"boom"}}`,
			want:     Tokens{},
			wantSeen: false,
		},
		{
			name:     "not json",
			body:     `<html>502</html>`,
			want:     Tokens{},
			wantSeen: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			acc := &Accumulator{}
			acc.observeJSON([]byte(tc.body))
			tokens, hasUsage := acc.Tokens()
			assertTokens(t, tc.name, tokens, tc.want)
			if hasUsage != tc.wantSeen {
				t.Errorf("hasUsage = %v, want %v", hasUsage, tc.wantSeen)
			}
		})
	}
}

// TestAccumulatorNilSafety pins the contract that observation can never panic,
// even when no middleware installed an accumulator.
func TestAccumulatorNilSafety(t *testing.T) {
	var acc *Accumulator
	acc.MarkStream()
	acc.MarkUpstreamStatus(http.StatusBadRequest)
	acc.MarkNoUsageExpected()
	acc.MarkCaptureDropped()
	acc.observeAnthropicEvent([]byte(`{"usage":{"input_tokens":1}}`))
	acc.observeOpenAIChunk([]byte(`{"usage":{"prompt_tokens":1}}`))
	acc.observeGeminiChunk([]byte(`{"usageMetadata":{"promptTokenCount":1}}`))
	acc.observeGeminiStream([]byte(`[]`))
	acc.observeJSON([]byte(`{"usage":{"input_tokens":1}}`))
	if tokens, hasUsage := acc.Tokens(); tokens != (Tokens{}) || hasUsage {
		t.Error("a nil accumulator must report zero usage")
	}
	if acc.Stream() {
		t.Error("a nil accumulator must not report a stream")
	}
	if acc.UpstreamStatus() != 0 {
		t.Error("a nil accumulator must not report an upstream status")
	}
	if got := acc.Report(http.StatusTeapot).Status; got != http.StatusTeapot {
		t.Errorf("Report on a nil accumulator = %d, want the writer status", got)
	}
}

// TestAccumulatorReportEffectiveStatus pins the precedence the counters use to
// classify a request.
//
// This is the F1 fix: a streaming handler writes c.Status(200) before any frame
// is known, so the writer status alone cannot distinguish a failed stream from a
// successful one.
func TestAccumulatorReportEffectiveStatus(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*Accumulator)
		writer int
		want   int
	}{
		{
			name:   "no upstream information falls back to the writer status",
			setup:  func(*Accumulator) {},
			writer: http.StatusOK,
			want:   http.StatusOK,
		},
		{
			name:   "writer status is preserved when it is an error",
			setup:  func(*Accumulator) {},
			writer: http.StatusNotFound,
			want:   http.StatusNotFound,
		},
		{
			name:   "upstream 200 keeps the request successful",
			setup:  func(a *Accumulator) { a.MarkUpstreamStatus(http.StatusOK) },
			writer: http.StatusOK,
			want:   http.StatusOK,
		},
		{
			name:   "upstream 400 wins over the 200 the client saw",
			setup:  func(a *Accumulator) { a.MarkUpstreamStatus(http.StatusBadRequest) },
			writer: http.StatusOK,
			want:   http.StatusBadRequest,
		},
		{
			name: "a real upstream code beats a stream error event",
			setup: func(a *Accumulator) {
				a.MarkUpstreamStatus(http.StatusBadRequest)
				a.observeAnthropicEvent([]byte(`{"type":"error","error":{"message":"x"}}`))
			},
			writer: http.StatusOK,
			want:   http.StatusBadRequest,
		},
		{
			name: "an error event inside a 200 stream becomes 502",
			setup: func(a *Accumulator) {
				a.MarkUpstreamStatus(http.StatusOK)
				a.observeAnthropicEvent([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"overloaded"}}`))
			},
			writer: http.StatusOK,
			want:   http.StatusBadGateway,
		},
		{
			name: "an OpenAI error chunk inside a 200 stream becomes 502",
			setup: func(a *Accumulator) {
				a.observeOpenAIChunk([]byte(`{"error":{"message":"boom","type":"server_error"}}`))
			},
			writer: http.StatusOK,
			want:   http.StatusBadGateway,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			acc := &Accumulator{}
			tc.setup(acc)
			if got := acc.Report(tc.writer).Status; got != tc.want {
				t.Errorf("Report(%d).Status = %d, want %d", tc.writer, got, tc.want)
			}
		})
	}
}

// TestAccumulatorStreamErrorDoesNotMatchModelText guards the byte-level
// pre-filter in noteStreamError: a model that literally prints {"error": ...} in
// its answer must not be classified as a failed request.
func TestAccumulatorStreamErrorDoesNotMatchModelText(t *testing.T) {
	acc := &Accumulator{}
	acc.observeAnthropicEvent([]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"{\"error\":\"boom\"}"}}`))

	if got := acc.Report(http.StatusOK).Status; got != http.StatusOK {
		t.Errorf("Report().Status = %d, want 200: quoted error text is not an error event", got)
	}
}

// TestAccumulatorReportInstrumentationFlags covers the flags that let the
// collector tell "nobody looked" from "the upstream said nothing".
func TestAccumulatorReportInstrumentationFlags(t *testing.T) {
	acc := &Accumulator{}
	if r := acc.Report(http.StatusOK); r.Observed || r.NoUsageExpected || r.CaptureDropped {
		t.Fatalf("a fresh accumulator must report no flags: %+v", r)
	}

	acc.observeJSON([]byte(`{"object":"chat.completion","id":"x"}`))
	if r := acc.Report(http.StatusOK); !r.Observed {
		t.Error("an Observe call must mark the request as observed even without usage")
	}

	other := &Accumulator{}
	other.MarkNoUsageExpected()
	if r := other.Report(http.StatusOK); !r.NoUsageExpected || r.Observed {
		t.Errorf("MarkNoUsageExpected flags = %+v, want noUsageExpected only", r)
	}

	dropped := &Accumulator{}
	dropped.MarkCaptureDropped()
	if r := dropped.Report(http.StatusOK); !r.CaptureDropped || !r.Observed {
		t.Errorf("MarkCaptureDropped flags = %+v, want captureDropped and observed", r)
	}
}

// TestAccumulatorResponsesShape covers the OpenAI Responses API usage block, which
// reuses Anthropic's input_tokens/output_tokens names but reports the cached part
// under input_tokens_details.cached_tokens.
func TestAccumulatorResponsesShape(t *testing.T) {
	acc := &Accumulator{}
	acc.observeJSON([]byte(`{"id":"resp_1","object":"response","status":"completed","usage":{"input_tokens":30,"output_tokens":7,"total_tokens":37,"input_tokens_details":{"cached_tokens":5}}}`))

	tokens, hasUsage := acc.Tokens()
	assertTokens(t, "responses", tokens, Tokens{Input: 30, Output: 7, CacheRead: 5})
	if !hasUsage {
		t.Error("expected hasUsage to be true")
	}

	// The Anthropic spelling must keep working and must not be shadowed by the
	// details block when both are present.
	anthropicOnly := &Accumulator{}
	anthropicOnly.observeJSON([]byte(`{"usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":9}}`))
	tokens, _ = anthropicOnly.Tokens()
	assertTokens(t, "anthropic", tokens, Tokens{Input: 10, Output: 2, CacheRead: 9})
}

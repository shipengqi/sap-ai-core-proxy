package usage

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Accumulator collects the usage of a single in-flight request.
//
// It is created by Middleware and handed to the response paths through the
// request context. It is intentionally not shared: a request's numbers are only
// published to the Collector once, by Collector.Add.
type Accumulator struct {
	mu     sync.Mutex
	tokens Tokens
	// hasUsage records whether the upstream reported usage at all. It drives
	// Aggregate.UsageMissing, which is the input to the P1 decision about
	// forcing OpenAI's stream_options.include_usage.
	hasUsage bool
	// anthropicInputSet records that an authoritative Anthropic input-side usage
	// block has already been applied, so a later message_delta cannot overwrite
	// (or, worse, add to) it.
	anthropicInputSet bool
	stream            bool
	// upstreamStatus is the status the upstream actually returned. The streaming
	// handlers lock the client-visible status at 200 before any frame is known,
	// so without this the counters cannot tell a failed stream from a good one.
	upstreamStatus int
	// streamFailed records a failure the upstream reported *inside* an
	// already-200 stream (an error event / error chunk).
	streamFailed bool
	// observed records that some response path actually tried to read usage.
	// Without it "the upstream reported no usage" and "nobody looked" would be
	// indistinguishable, which is how the uninstrumented endpoints stayed
	// invisible.
	observed bool
	// noUsageExpected records that this endpoint cannot produce tokens at all
	// (image generation, audio transcription), so a zero is not a gap.
	noUsageExpected bool
	// captureDropped records that the usage existed but we threw it away — the
	// Gemini stream was larger than the capture cap.
	captureDropped bool
}

// Tokens returns the tokens observed so far and whether any usage was reported.
func (a *Accumulator) Tokens() (Tokens, bool) {
	if a == nil {
		return Tokens{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tokens, a.hasUsage
}

// Stream reports whether this request produced a streaming response.
func (a *Accumulator) Stream() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stream
}

// UpstreamStatus returns the status recorded by MarkUpstreamStatus, or 0 when
// the response path never recorded one.
func (a *Accumulator) UpstreamStatus() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.upstreamStatus
}

// MarkStream records that the response is a stream. It is called by the stream
// package rather than inferred from the response, because Gemini streams with a
// JSON content type and OpenAI streams carry no usage chunk at all.
func (a *Accumulator) MarkStream() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stream = true
}

// MarkUpstreamStatus records the HTTP status the upstream returned.
//
// This is the only way the counters can see a failed stream: the handlers call
// c.Status(200) before piping so that the client sees a stream (pre-existing
// behaviour that must not change), which makes c.Writer.Status() useless as a
// failure signal.
func (a *Accumulator) MarkUpstreamStatus(code int) {
	if a == nil || code == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.upstreamStatus = code
}

// MarkNoUsageExpected records that this endpoint produces no token counts, so
// the request must not be reported as missing usage or as uninstrumented.
func (a *Accumulator) MarkNoUsageExpected() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.noUsageExpected = true
}

// MarkCaptureDropped records that usage was known to exist but could not be
// recovered (the Gemini stream exceeded the capture cap). It also counts as
// observed: the request was instrumented, the data was lost by us.
func (a *Accumulator) MarkCaptureDropped() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.observed = true
	a.captureDropped = true
}

// markObserved records that a response path attempted to read usage.
func (a *Accumulator) markObserved() {
	a.mu.Lock()
	a.observed = true
	a.mu.Unlock()
}

// noteStreamError detects a failure the upstream reported *inside* a stream that
// already answered 200, which is how Anthropic and OpenAI signal overload and
// mid-generation errors.
//
// The byte-level pre-filter matters: escaped quotes inside a text delta are
// written as \" on the wire, so a model that literally prints {"error":...} in
// its answer cannot trip the substring test.
func (a *Accumulator) noteStreamError(payload []byte) {
	if !bytes.Contains(payload, []byte(`"error"`)) {
		return
	}
	var probe struct {
		Type  string          `json:"type"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return
	}
	if probe.Type != "error" && (len(probe.Error) == 0 || string(probe.Error) == "null") {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.streamFailed = true
}

// Report renders the request into a Record using the effective status.
//
// Precedence: a real upstream error code wins (it is the most specific fact), then
// a failure the upstream reported inside the stream (synthesised as 502), then a
// non-error upstream status, then whatever the client was actually sent.
func (a *Accumulator) Report(writerStatus int) Record {
	if a == nil {
		return Record{Status: writerStatus}
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	status := writerStatus
	switch {
	case a.upstreamStatus >= http.StatusBadRequest:
		status = a.upstreamStatus
	case a.streamFailed:
		status = http.StatusBadGateway
	case a.upstreamStatus != 0:
		status = a.upstreamStatus
	}

	return Record{
		Status:          status,
		Stream:          a.stream,
		Tokens:          a.tokens,
		HasUsage:        a.hasUsage,
		Observed:        a.observed,
		NoUsageExpected: a.noUsageExpected,
		CaptureDropped:  a.captureDropped,
	}
}

// flexInt decodes a token count that may arrive as a number, a float, a quoted
// number or null.
//
// UnmarshalJSON deliberately never returns an error: usage parsing runs on the
// response path, and a shape we did not anticipate must degrade to "field
// absent", never to a failed parse that loses the rest of the counters.
type flexInt struct {
	value int64
	ok    bool
}

// UnmarshalJSON implements json.Unmarshaler.
func (f *flexInt) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "" || s == "null" {
		return nil
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = strings.TrimSpace(s[1 : len(s)-1])
		if s == "" {
			return nil
		}
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		f.value, f.ok = v, true
		return nil
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		f.value, f.ok = int64(v), true
	}
	return nil
}

// anthropicUsage is the usage block of an Anthropic message (streaming or not).
type anthropicUsage struct {
	InputTokens  flexInt `json:"input_tokens"`
	OutputTokens flexInt `json:"output_tokens"`
	CacheRead    flexInt `json:"cache_read_input_tokens"`
	CacheWrite   flexInt `json:"cache_creation_input_tokens"`
}

// openAIUsage is the usage block of an OpenAI chat completion chunk or body.
//
// prompt_tokens is the whole prompt, cached and written parts included, which is
// the opposite of Anthropic's input_tokens — that is why CacheHitRatio divides by
// surface. cache_write_tokens appears from GPT-5.6 on, where writing the cache
// costs money; older models report only cached_tokens and charge nothing for a
// write.
type openAIUsage struct {
	PromptTokens        flexInt `json:"prompt_tokens"`
	CompletionTokens    flexInt `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens     flexInt `json:"cached_tokens"`
		CacheWriteTokens flexInt `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
}

// geminiUsage is Gemini's usageMetadata block.
type geminiUsage struct {
	PromptTokenCount        flexInt `json:"promptTokenCount"`
	CandidatesTokenCount    flexInt `json:"candidatesTokenCount"`
	CachedContentTokenCount flexInt `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      flexInt `json:"thoughtsTokenCount"`
}

// genericUsage covers every wire format at once, so a non-streaming body can be
// sniffed without knowing which surface produced it (converted responses do not
// necessarily match the surface's native format).
type genericUsage struct {
	InputTokens  flexInt `json:"input_tokens"`
	OutputTokens flexInt `json:"output_tokens"`
	CacheRead    flexInt `json:"cache_read_input_tokens"`
	CacheWrite   flexInt `json:"cache_creation_input_tokens"`
	// InputTokensDetails is the Responses API's spelling of the cached prompt
	// count. It is a distinct field from Anthropic's cache_read_input_tokens, so
	// the two never collide: Anthropic does not send input_tokens_details and the
	// Responses API does not send cache_read_input_tokens.
	InputTokensDetails *struct {
		CachedTokens     flexInt `json:"cached_tokens"`
		CacheWriteTokens flexInt `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	PromptTokens        flexInt `json:"prompt_tokens"`
	CompletionTokens    flexInt `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens     flexInt `json:"cached_tokens"`
		CacheWriteTokens flexInt `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
}

func (u *anthropicUsage) any() bool {
	return u != nil && (u.InputTokens.ok || u.OutputTokens.ok || u.CacheRead.ok || u.CacheWrite.ok)
}

func (u *openAIUsage) any() bool {
	if u == nil {
		return false
	}
	if u.PromptTokens.ok || u.CompletionTokens.ok {
		return true
	}
	return u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens.ok
}

func (u *geminiUsage) any() bool {
	return u != nil && (u.PromptTokenCount.ok || u.CandidatesTokenCount.ok ||
		u.CachedContentTokenCount.ok || u.ThoughtsTokenCount.ok)
}

// observeAnthropicEvent folds one Anthropic SSE payload (the part after
// "data: ") into the accumulator.
func (a *Accumulator) observeAnthropicEvent(payload []byte) {
	if a == nil || len(payload) == 0 {
		return
	}
	a.markObserved()
	a.noteStreamError(payload)

	// Cheap pre-filter: the vast majority of SSE events (content_block_delta,
	// ping, ...) carry no usage, and a streaming response can have thousands of
	// them. Skipping the JSON decode there keeps the proxy off the hot path.
	if !bytes.Contains(payload, []byte(`"usage"`)) {
		return
	}

	var event struct {
		Type    string `json:"type"`
		Message *struct {
			Usage *anthropicUsage `json:"usage"`
		} `json:"message"`
		Usage *anthropicUsage `json:"usage"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return
	}

	switch {
	case event.Message != nil && event.Message.Usage != nil:
		// message_start: the authoritative input-side numbers.
		a.applyAnthropicUsage(event.Message.Usage, true)
	case event.Usage != nil:
		// message_delta carries usage at the top level. Some upstreams repeat the
		// input counters there with a running total, so only a non-delta event
		// (a plain non-streaming message body) is allowed to overwrite them.
		a.applyAnthropicUsage(event.Usage, event.Type != "message_delta")
	}
}

// applyAnthropicUsage folds u into the accumulator.
//
// overwriteInput must be true only for the authoritative input-side block. The
// input, cache-read and cache-write counters are absolute per-request values,
// which is why they are assigned rather than added — adding them again from a
// repeated message_delta would double count the prompt.
func (a *Accumulator) applyAnthropicUsage(u *anthropicUsage, overwriteInput bool) {
	if !u.any() {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if overwriteInput || !a.anthropicInputSet {
		present := false
		if u.InputTokens.ok {
			a.tokens.Input = u.InputTokens.value
			present = true
		}
		if u.CacheRead.ok {
			a.tokens.CacheRead = u.CacheRead.value
			present = true
		}
		if u.CacheWrite.ok {
			a.tokens.CacheWrite = u.CacheWrite.value
			present = true
		}
		if present {
			a.anthropicInputSet = true
		}
	}

	// Output tokens are cumulative across message_delta events (and message_start
	// reports a placeholder 1), so the maximum is the final answer.
	if u.OutputTokens.ok && u.OutputTokens.value > a.tokens.Output {
		a.tokens.Output = u.OutputTokens.value
	}

	a.hasUsage = true
}

// observeOpenAIChunk folds one OpenAI SSE payload into the accumulator. OpenAI
// sends `"usage": null` on every chunk until the final one, which is why the
// null case has to be a no-op rather than a "usage missing" signal.
func (a *Accumulator) observeOpenAIChunk(payload []byte) {
	if a == nil || len(payload) == 0 {
		return
	}
	a.markObserved()
	a.noteStreamError(payload)
	if !bytes.Contains(payload, []byte(`"usage"`)) {
		return
	}
	var chunk struct {
		Usage *openAIUsage `json:"usage"`
	}
	if err := json.Unmarshal(payload, &chunk); err != nil || chunk.Usage == nil {
		return
	}
	a.applyOpenAIUsage(chunk.Usage)
}

func (a *Accumulator) applyOpenAIUsage(u *openAIUsage) {
	if !u.any() {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// Usage only appears on the final chunk and is cumulative, but taking the max
	// also makes a repeated or re-sent final chunk idempotent.
	if u.PromptTokens.ok && u.PromptTokens.value > a.tokens.Input {
		a.tokens.Input = u.PromptTokens.value
	}
	if u.CompletionTokens.ok && u.CompletionTokens.value > a.tokens.Output {
		a.tokens.Output = u.CompletionTokens.value
	}
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens.ok &&
		u.PromptTokensDetails.CachedTokens.value > a.tokens.CacheRead {
		a.tokens.CacheRead = u.PromptTokensDetails.CachedTokens.value
	}
	// cache_write_tokens is reported from GPT-5.6 on, where a write is billed.
	// Older models never send it, so this stays zero for them and no cost is
	// invented.
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CacheWriteTokens.ok &&
		u.PromptTokensDetails.CacheWriteTokens.value > a.tokens.CacheWrite {
		a.tokens.CacheWrite = u.PromptTokensDetails.CacheWriteTokens.value
	}

	a.hasUsage = true
}

// observeGeminiChunk folds one Gemini response object (a generateContent body or
// one element of a streamGenerateContent array) into the accumulator.
func (a *Accumulator) observeGeminiChunk(payload []byte) {
	if a == nil || len(payload) == 0 {
		return
	}
	a.markObserved()
	a.noteStreamError(payload)
	if !bytes.Contains(payload, []byte(`"usageMetadata"`)) {
		return
	}
	var chunk struct {
		UsageMetadata *geminiUsage `json:"usageMetadata"`
	}
	if err := json.Unmarshal(payload, &chunk); err != nil || chunk.UsageMetadata == nil {
		return
	}
	a.applyGeminiUsage(chunk.UsageMetadata)
}

func (a *Accumulator) applyGeminiUsage(u *geminiUsage) {
	if !u.any() {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if u.PromptTokenCount.ok && u.PromptTokenCount.value > a.tokens.Input {
		a.tokens.Input = u.PromptTokenCount.value
	}

	// Gemini bills thinking tokens as output but reports them outside
	// candidatesTokenCount, so both have to be added to match the bill.
	var output int64
	outputSet := false
	if u.CandidatesTokenCount.ok {
		output += u.CandidatesTokenCount.value
		outputSet = true
	}
	if u.ThoughtsTokenCount.ok {
		output += u.ThoughtsTokenCount.value
		outputSet = true
	}
	if outputSet && output > a.tokens.Output {
		a.tokens.Output = output
	}

	if u.CachedContentTokenCount.ok && u.CachedContentTokenCount.value > a.tokens.CacheRead {
		a.tokens.CacheRead = u.CachedContentTokenCount.value
	}

	a.hasUsage = true
}

// observeGeminiStream folds a complete buffered streamGenerateContent array.
// Anything we cannot parse (truncated capture, unexpected framing) is ignored,
// which surfaces as usage_missing for that request.
func (a *Accumulator) observeGeminiStream(body []byte) {
	if a == nil || len(body) == 0 {
		return
	}
	a.markObserved()
	if !bytes.Contains(body, []byte(`"usageMetadata"`)) {
		return
	}
	var chunks []struct {
		UsageMetadata *geminiUsage `json:"usageMetadata"`
	}
	if err := json.Unmarshal(body, &chunks); err != nil {
		return
	}
	for _, chunk := range chunks {
		if chunk.UsageMetadata != nil {
			a.applyGeminiUsage(chunk.UsageMetadata)
		}
	}
}

// observeJSON folds a complete non-streaming response body into the accumulator.
//
// The body is sniffed rather than tagged with its wire format because the proxy
// rewrites responses across formats: a Bedrock /invoke answer is handed back to
// the client as OpenAI, and an OpenAI answer as Anthropic. The converted body is
// what the client sees and what carries the numbers we want to reconcile.
func (a *Accumulator) observeJSON(body []byte) {
	if a == nil || len(body) == 0 {
		return
	}
	a.markObserved()

	if bytes.Contains(body, []byte(`"usageMetadata"`)) {
		var v struct {
			UsageMetadata *geminiUsage `json:"usageMetadata"`
		}
		if err := json.Unmarshal(body, &v); err == nil && v.UsageMetadata != nil {
			a.applyGeminiUsage(v.UsageMetadata)
		}
		return
	}

	if !bytes.Contains(body, []byte(`"usage"`)) {
		return
	}

	var v struct {
		Usage *genericUsage `json:"usage"`
	}
	if err := json.Unmarshal(body, &v); err != nil || v.Usage == nil {
		return
	}
	u := v.Usage

	switch {
	case u.InputTokens.ok || u.CacheRead.ok || u.CacheWrite.ok || u.InputTokensDetails != nil:
		// Anthropic Messages, and also the OpenAI Responses API, which reuses the
		// input_tokens/output_tokens names and reports the cached part under
		// input_tokens_details.cached_tokens.
		cacheRead := u.CacheRead
		if !cacheRead.ok && u.InputTokensDetails != nil {
			cacheRead = u.InputTokensDetails.CachedTokens
		}
		// Same split for the write side: Anthropic reports it as
		// cache_creation_input_tokens, the Responses API as
		// input_tokens_details.cache_write_tokens.
		cacheWrite := u.CacheWrite
		if !cacheWrite.ok && u.InputTokensDetails != nil {
			cacheWrite = u.InputTokensDetails.CacheWriteTokens
		}
		a.applyAnthropicUsage(&anthropicUsage{
			InputTokens:  u.InputTokens,
			OutputTokens: u.OutputTokens,
			CacheRead:    cacheRead,
			CacheWrite:   cacheWrite,
		}, true)
	case u.PromptTokens.ok || u.CompletionTokens.ok || u.PromptTokensDetails != nil:
		a.applyOpenAIUsage(&openAIUsage{
			PromptTokens:        u.PromptTokens,
			CompletionTokens:    u.CompletionTokens,
			PromptTokensDetails: u.PromptTokensDetails,
		})
	}
}

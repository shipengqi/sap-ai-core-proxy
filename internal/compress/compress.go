// Package compress shrinks the tool_result logs of an outbound inference
// request.
//
// It applies one deterministic rule to *every* tool_result block in the
// request — the block in the live zone and the blocks already sitting in the
// history — and it applies that rule to the same text in the same way every
// time. That consistency is the whole design, and it is worth spelling out
// because the obvious alternative is a trap:
//
//   - Compressing only the live zone breaks the prompt cache. Turn N sends a
//     compressed X'; the provider caches X'. Turn N+1 arrives with the client's
//     original X in the history, and forwarding that X unchanged means the
//     prefix no longer matches the cached one, so the whole conversation is
//     billed as a fresh cache write. Rewriting the history on every turn keeps
//     the prefix byte-identical, which is what makes the cache keep hitting.
//   - Because the transform is a pure function of the input text, the history
//     is rewritten into exactly the bytes the provider already cached. The
//     model also keeps seeing the compressed form of a result it first saw
//     compressed, so its view of the conversation stays self-consistent.
//   - A block carrying cache_control is compressed like any other. It is
//     already part of the prefix and its compressed form is stable, so the
//     cache write happens once and every later turn reads it.
//
// Two properties the package is built around:
//
//   - Determinism. The same input must produce the same bytes every time,
//     because a prefix that changes every turn gets no cache read at all. There
//     is no sampling, no timestamp, no map iteration in any output path. The
//     LRU in cache.go only stores what the pure function already produced, so a
//     hit and a miss are indistinguishable in the output.
//   - Never fail a request. Every branch falls back to the original text, the
//     exported entry recovers from a panic, and the caller only runs this
//     package when the operator turned it on.
package compress

import (
	"encoding/json"
	"strings"
)

// MinTextBytes is the smallest tool_result text worth looking at. Below this a
// compression pass cannot pay for its own distortion risk: the omitted-line
// markers alone are tens of bytes.
const MinTextBytes = 512

// StrategyLog is the name of the only strategy this build ships. The Report and
// the metrics carry it so a second strategy can be added without changing the
// wire shapes.
const StrategyLog = "log"

// Skip reasons. They end up in sap_proxy_compress_skipped_total, which is the
// only way to tell "there was nothing to compress" from "the compressor was
// too scared to touch it".
const (
	skipNoMessages   = "no_messages"
	skipBadMessages  = "bad_messages"
	skipNoContent    = "no_content"
	skipBadContent   = "bad_content"
	skipPlainContent = "plain_content"
	skipNoToolResult = "no_tool_result"
	skipTooSmall     = "too_small"
	skipNotLog       = "not_log"
	skipNoGain       = "no_gain"
	skipStrategyOff  = "strategy_disabled"
	skipPanic        = "panic"
)

// Report describes what one request did. It feeds the metrics and the log line;
// it is never used to change the request.
type Report struct {
	// BlocksConsidered counts tool_result text blocks that were eligible: big
	// enough and of a type this package understands. Blocks are counted in
	// every message of the request, history included.
	BlocksConsidered int
	// BlocksCompressed counts the eligible blocks that actually shrank.
	BlocksCompressed int
	// OriginalBytes is the size of the compressed blocks before compression.
	// Blocks that were skipped are not counted, so the metric is a true before
	// and after for the bytes that moved.
	OriginalBytes int
	// CompressedBytes is the size of those same blocks after compression.
	CompressedBytes int
	// Strategy names the arm that ran, e.g. "log".
	Strategy string
	// Skipped is the reason nothing was compressed. Empty means at least one
	// block was compressed.
	Skipped string
}

// merge adds a per-message report into the request total.
func (r *Report) merge(other Report) {
	r.BlocksConsidered += other.BlocksConsidered
	r.BlocksCompressed += other.BlocksCompressed
	r.OriginalBytes += other.OriginalBytes
	r.CompressedBytes += other.CompressedBytes
}

// CompressRequest compresses every tool_result log in an Anthropic-shaped
// request.
//
// It walks all of messages, not just the last user turn, and considers the
// tool_result blocks of every message that carries an array content. Both
// tool_result content shapes are supported: a plain JSON string, and an array
// of {type:"text", text:...} parts.
//
// A message whose content is a plain string is left alone: that is text the
// caller wrote, and silently dropping lines from it would take exactly the line
// the caller was pointing at without telling anyone. Only machine-generated
// tool output is eligible.
//
// The returned map is either the argument itself (nothing changed) or a shallow
// copy whose only difference is the "messages" key. Nothing outside "messages"
// is touched — system and tools are part of the cache prefix and are never
// rewritten — and no json.RawMessage is mutated in place.
func CompressRequest(m map[string]json.RawMessage) (out map[string]json.RawMessage, rep Report) {
	out = m
	if m == nil {
		return m, Report{Skipped: skipNoMessages}
	}
	// A compressor must never be able to fail a request. Every branch below is
	// written to fall back, but a malformed block we did not anticipate would
	// otherwise turn into a 500 on somebody's coding session.
	defer func() {
		if r := recover(); r != nil {
			out = m
			rep = Report{Skipped: skipPanic}
		}
	}()

	msgsRaw, ok := m["messages"]
	if !ok {
		return m, Report{Skipped: skipNoMessages}
	}
	var msgs []json.RawMessage
	if err := json.Unmarshal(msgsRaw, &msgs); err != nil || len(msgs) == 0 {
		return m, Report{Skipped: skipBadMessages}
	}

	rep.Strategy = StrategyLog
	reason := ""
	sawToolResult := false
	changed := false

	for i := range msgs {
		res := compressMessage(msgs[i])
		rep.merge(res.report)
		if res.sawToolResult {
			sawToolResult = true
		}
		if res.message == nil {
			// Unchanged: the original bytes stay in the slice, so an untouched
			// message is forwarded exactly as the client sent it.
			if reason == "" {
				reason = res.reason
			}
			continue
		}
		msgs[i] = res.message
		changed = true
	}

	if !changed {
		switch {
		case reason != "":
			rep.Skipped = reason
		case !sawToolResult:
			rep.Skipped = skipNoToolResult
		}
		return m, rep
	}

	encoded, err := json.Marshal(msgs)
	if err != nil {
		return m, Report{BlocksConsidered: rep.BlocksConsidered, Skipped: skipBadMessages}
	}

	// Shallow copy: every other value is shared with the caller, and only the
	// messages key is replaced.
	clone := make(map[string]json.RawMessage, len(m)+1)
	for k, v := range m {
		clone[k] = v
	}
	clone["messages"] = encoded
	return clone, rep
}

// messageResult is the outcome of compressing one message.
type messageResult struct {
	// message is the rewritten message, or nil when the message is unchanged
	// and the caller must keep the original bytes.
	message json.RawMessage
	// sawToolResult reports whether the message carried at least one
	// tool_result block, whatever happened to it.
	sawToolResult bool
	// reason is why the message was left alone. Empty when it changed, or when
	// there was simply nothing to do in it.
	reason string
	// report carries the per-message counters.
	report Report
}

// compressMessage compresses the tool_result blocks of one message.
func compressMessage(raw json.RawMessage) messageResult {
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &msg); err != nil || msg == nil {
		return messageResult{reason: skipBadMessages}
	}
	contentRaw, ok := msg["content"]
	if !ok {
		return messageResult{reason: skipNoContent}
	}
	// Only an array can carry a tool_result. A plain-string content is the
	// caller's own words rather than machine-generated output, and is not
	// eligible at any position in the conversation.
	if isJSONString(contentRaw) {
		return messageResult{reason: skipPlainContent}
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(contentRaw, &blocks); err != nil {
		return messageResult{reason: skipBadContent}
	}

	res := messageResult{}
	changed := false
	for i := range blocks {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(blocks[i], &obj); err != nil || obj == nil {
			continue
		}
		if blockType(obj) != blockToolResult {
			continue
		}
		res.sawToolResult = true

		rebuilt, blockChanged, why := compressToolResult(obj, &res.report)
		if !blockChanged {
			if res.reason == "" {
				res.reason = why
			}
			continue
		}
		blocks[i] = rebuilt
		changed = true
	}
	if !changed {
		return res
	}

	encoded, err := json.Marshal(blocks)
	if err != nil {
		// Nothing ships, so nothing may be counted either.
		return messageResult{sawToolResult: res.sawToolResult, reason: skipBadContent}
	}
	msg["content"] = encoded
	newMsg, err := json.Marshal(msg)
	if err != nil {
		return messageResult{sawToolResult: res.sawToolResult, reason: skipBadMessages}
	}
	res.message = newMsg
	res.reason = ""
	return res
}

// compressToolResult compresses one tool_result block.
//
// It returns the rebuilt block bytes, whether anything changed, and the reason
// the block was left alone. The block map is modified in place, which is safe
// because it was decoded from the request bytes and never shared.
func compressToolResult(obj map[string]json.RawMessage, rep *Report) (json.RawMessage, bool, string) {
	bodyRaw, ok := obj["content"]
	if !ok {
		return nil, false, skipNoContent
	}

	if isJSONString(bodyRaw) {
		var text string
		if err := json.Unmarshal(bodyRaw, &text); err != nil {
			return nil, false, skipBadContent
		}
		rep.BlocksConsidered++
		compressed, why := compressText(text)
		if compressed == "" {
			return nil, false, why
		}
		encoded, err := json.Marshal(compressed)
		if err != nil {
			return nil, false, skipBadContent
		}
		obj["content"] = encoded
		rebuilt, err := json.Marshal(obj)
		if err != nil {
			return nil, false, skipBadContent
		}
		rep.BlocksCompressed++
		rep.OriginalBytes += len(text)
		rep.CompressedBytes += len(compressed)
		return rebuilt, true, ""
	}

	var parts []json.RawMessage
	if err := json.Unmarshal(bodyRaw, &parts); err != nil {
		return nil, false, skipBadContent
	}
	partChanged := false
	reason := ""
	for i := range parts {
		var part map[string]json.RawMessage
		if err := json.Unmarshal(parts[i], &part); err != nil || part == nil {
			continue
		}
		textRaw, ok := part["text"]
		if !ok || !isJSONString(textRaw) {
			continue
		}
		var text string
		if err := json.Unmarshal(textRaw, &text); err != nil {
			continue
		}
		rep.BlocksConsidered++
		compressed, why := compressText(text)
		if compressed == "" {
			if reason == "" {
				reason = why
			}
			continue
		}
		encoded, err := json.Marshal(compressed)
		if err != nil {
			continue
		}
		part["text"] = encoded
		rebuilt, err := json.Marshal(part)
		if err != nil {
			continue
		}
		parts[i] = rebuilt
		partChanged = true
		rep.BlocksCompressed++
		rep.OriginalBytes += len(text)
		rep.CompressedBytes += len(compressed)
	}
	if !partChanged {
		if reason == "" {
			// The block carried a content array with no text part in it: there
			// was nothing this package understands, which is not an error.
			reason = skipNoContent
		}
		return nil, false, reason
	}

	encoded, err := json.Marshal(parts)
	if err != nil {
		return nil, false, skipBadContent
	}
	obj["content"] = encoded
	rebuilt, err := json.Marshal(obj)
	if err != nil {
		return nil, false, skipBadContent
	}
	return rebuilt, true, ""
}

const blockToolResult = "tool_result"

// compressText returns the compressed text, or "" with the reason it left the
// text alone. The caller treats "" as "use the original".
//
// The escape valve is consulted here rather than inside the cache, so a
// strategy switched off at runtime takes effect on the next block instead of
// being masked by an entry cached while it was on.
func compressText(text string) (string, string) {
	if Skip(StrategyLog) {
		return "", skipStrategyOff
	}
	return compressTextCached(text)
}

// compressTextUncached is the pure transform: the same text in always yields
// the same bytes out. Everything cache.go stores comes from here, which is why
// a cache hit can never change the output.
func compressTextUncached(text string) (string, string) {
	if len(text) < MinTextBytes {
		return "", skipTooSmall
	}
	if !LooksLikeLog(text) {
		return "", skipNotLog
	}
	compressed := CompressLog(text)
	// Strictly smaller or nothing. A compression that grows the payload is a
	// bug, and shipping it would cost tokens instead of saving them.
	if len(compressed) >= len(text) {
		return "", skipNoGain
	}
	return compressed, ""
}

// blockType returns the type of a content block, "" when it has none.
func blockType(block map[string]json.RawMessage) string {
	raw, ok := block["type"]
	if !ok {
		return ""
	}
	var t string
	if err := json.Unmarshal(raw, &t); err != nil {
		return ""
	}
	return t
}

// isJSONString reports whether raw is a JSON string. It looks at the first byte
// rather than unmarshalling, because the caller needs the distinction before it
// decides how to decode.
func isJSONString(raw json.RawMessage) bool {
	return strings.HasPrefix(strings.TrimSpace(string(raw)), `"`)
}

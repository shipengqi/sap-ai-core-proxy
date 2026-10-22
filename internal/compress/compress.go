// Package compress shrinks the live zone of an outbound inference request.
//
// The live zone is the last user message, and only that: everything before it
// (system, tools, message history) is the prompt-cache prefix, which SAP AI
// Core bills at 1.25x to write and 0.1x to read. Touching a byte of it costs
// more than the tokens it could ever save, so this package never does.
//
// Two properties the whole package is built around:
//
//   - Determinism. The same input must produce the same bytes every time,
//     because a prefix that changes every turn gets no cache read at all. There
//     is no sampling, no timestamp, no map iteration in any output path.
//   - Never fail a request. Every branch falls back to the original text, the
//     exported entry recovers from a panic, and the caller only runs this
//     package when the operator turned it on.
package compress

import (
	"encoding/json"
	"strings"
)

// MinTextBytes is the smallest live-zone text worth looking at. Below this a
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
	skipNoUserMsg    = "no_user_message"
	skipBadUserMsg   = "bad_user_message"
	skipNoContent    = "no_content"
	skipBadContent   = "bad_content"
	skipPlainContent = "plain_content"
	skipNoToolResult = "no_tool_result"
	skipCacheControl = "cache_control"
	skipTooSmall     = "too_small"
	skipNotLog       = "not_log"
	skipNoGain       = "no_gain"
	skipStrategyOff  = "strategy_disabled"
	skipPanic        = "panic"
)

// Report describes what one request did. It feeds the metrics and the log line;
// it is never used to change the request.
type Report struct {
	// BlocksConsidered counts live-zone text blocks that were eligible: big
	// enough, no cache_control, and of a type this package understands.
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

// CompressLiveZone compresses the live zone of an Anthropic-shaped request.
//
// The live zone is the content of the last message with role "user". Inside it
// only tool_result blocks are considered, and a block carrying cache_control is
// left byte for byte alone: rewriting a cached block is charged as a cache
// write, which costs more than the read it would save.
//
// The returned map is either the argument itself (nothing changed) or a shallow
// copy whose only difference is the "messages" key. Nothing outside the live
// zone is touched, and no json.RawMessage is mutated in place.
func CompressLiveZone(m map[string]json.RawMessage) (out map[string]json.RawMessage, rep Report) {
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

	last := -1
	for i := range msgs {
		if messageRole(msgs[i]) == roleUser {
			last = i
		}
	}
	if last < 0 {
		return m, Report{Skipped: skipNoUserMsg}
	}

	var msg map[string]json.RawMessage
	if err := json.Unmarshal(msgs[last], &msg); err != nil || msg == nil {
		return m, Report{Skipped: skipBadUserMsg}
	}
	contentRaw, ok := msg["content"]
	if !ok {
		return m, Report{Skipped: skipNoContent}
	}

	rep.Strategy = StrategyLog
	reason := ""

	var (
		newContent    json.RawMessage
		changed       bool
		sawToolResult bool
	)

	// Only an array can carry a tool_result. A plain-string content is the
	// caller's own words rather than machine-generated output: dropping 80% of
	// it silently would take exactly the line the caller pointed at, and the
	// caller would never learn the text was rewritten. Not worth it.
	if isJSONString(contentRaw) {
		return m, Report{Skipped: skipPlainContent}
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(contentRaw, &blocks); err != nil {
		return m, Report{Skipped: skipBadContent}
	}
	for i := range blocks {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(blocks[i], &obj); err != nil || obj == nil {
			continue
		}
		if blockType(obj) != blockToolResult {
			continue
		}
		sawToolResult = true
		// A cached block is billed at 1.25x per byte rewritten. Leave it.
		if _, cached := obj["cache_control"]; cached {
			if reason == "" {
				reason = skipCacheControl
			}
			continue
		}
		bodyRaw, ok := obj["content"]
		if !ok {
			continue
		}

		if isJSONString(bodyRaw) {
			var text string
			if err := json.Unmarshal(bodyRaw, &text); err != nil {
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
			obj["content"] = encoded
			rebuilt, err := json.Marshal(obj)
			if err != nil {
				continue
			}
			blocks[i] = rebuilt
			changed = true
			rep.BlocksCompressed++
			rep.OriginalBytes += len(text)
			rep.CompressedBytes += len(compressed)
			continue
		}

		var parts []json.RawMessage
		if err := json.Unmarshal(bodyRaw, &parts); err != nil {
			continue
		}
		partChanged := false
		for j := range parts {
			var part map[string]json.RawMessage
			if err := json.Unmarshal(parts[j], &part); err != nil || part == nil {
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
			parts[j] = rebuilt
			partChanged = true
			rep.BlocksCompressed++
			rep.OriginalBytes += len(text)
			rep.CompressedBytes += len(compressed)
		}
		if partChanged {
			encoded, err := json.Marshal(parts)
			if err != nil {
				continue
			}
			obj["content"] = encoded
			rebuilt, err := json.Marshal(obj)
			if err != nil {
				continue
			}
			blocks[i] = rebuilt
			changed = true
		}
	}
	if changed {
		encoded, err := json.Marshal(blocks)
		if err != nil {
			return m, Report{BlocksConsidered: rep.BlocksConsidered, Skipped: skipBadContent}
		}
		newContent = encoded
	}

	if !changed {
		if reason != "" {
			rep.Skipped = reason
		} else if !sawToolResult {
			rep.Skipped = skipNoToolResult
		}
		return m, rep
	}

	msg["content"] = newContent
	newMsg, err := json.Marshal(msg)
	if err != nil {
		return m, Report{BlocksConsidered: rep.BlocksConsidered, Skipped: skipBadUserMsg}
	}
	msgs[last] = newMsg
	newMsgs, err := json.Marshal(msgs)
	if err != nil {
		return m, Report{BlocksConsidered: rep.BlocksConsidered, Skipped: skipBadMessages}
	}

	// Shallow copy: every other value is shared with the caller, and only the
	// messages key is replaced.
	clone := make(map[string]json.RawMessage, len(m)+1)
	for k, v := range m {
		clone[k] = v
	}
	clone["messages"] = newMsgs
	return clone, rep
}

const (
	roleUser        = "user"
	blockToolResult = "tool_result"
)

// compressText returns the compressed text, or "" with the reason it left the
// text alone. The caller treats "" as "use the original".
func compressText(text string) (string, string) {
	if Skip(StrategyLog) {
		return "", skipStrategyOff
	}
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

// messageRole returns the role of a message element, "" when it has none.
func messageRole(raw json.RawMessage) string {
	var m struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	return m.Role
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

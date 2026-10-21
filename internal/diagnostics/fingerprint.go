// Package diagnostics attributes prompt-cache hits and misses to a cause.
//
// The usage package answers "how many tokens were served from cache". It cannot
// answer "why was this request not served from cache", and the difference
// matters: a cache miss caused by the client rewriting its system prompt is
// actionable, while a miss caused by the five-minute TTL expiring is not.
//
// The attribution works by fingerprinting the cache hot zone of every request
// (system, tools, the message prefix) and comparing it with the previous
// request of the same session. The comparison is then crossed with the cache
// counters the upstream actually reported, which separates "we busted the
// cache" from "the cache was simply gone".
//
// Everything here is observation-only. No request byte is modified, no
// ResponseWriter is wrapped, and no prompt or message content is ever stored or
// logged — only hashes and counts. This is a corporate gateway; content must
// not leak into metrics.
package diagnostics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// maxBodyBytes caps the request body this package will parse. Beyond it the
// request is counted as skipped instead of buffered or parsed: a runaway body
// must not cost the proxy memory for a diagnostic that nobody asked for.
const maxBodyBytes = 8 << 20

// maxStoredMessageHashes bounds the per-session memory used to detect
// append-only conversation growth. See isAppendOnly for what the bound costs.
const maxStoredMessageHashes = 32

// fingerprints are the SHA-256 digests of the four regions that decide whether
// a provider prompt cache can still hit.
//
// The digests are taken over the raw JSON bytes rather than over a decoded and
// re-encoded value. Re-encoding would make the fingerprint depend on this
// package's marshalling choices, so an unrelated refactor could silently change
// every hash and make all traffic look like drift.
type fingerprints struct {
	System   [32]byte
	Tools    [32]byte
	Prefix   [32]byte
	LiveZone [32]byte
	First    [32]byte
}

// request is what ObserveRequest extracts from one inbound body.
type request struct {
	surface  string
	model    string
	session  string
	fp       fingerprints
	markers  int
	msgCount int
	msgHash  [][32]byte
	volatile map[string]int
	skipped  string
}

// bodyShape is the subset of the request body this package reads. Messages stay
// as json.RawMessage so each one can be hashed exactly as the client wrote it.
type bodyShape struct {
	System   json.RawMessage   `json:"system"`
	Tools    json.RawMessage   `json:"tools"`
	Messages []json.RawMessage `json:"messages"`
	Metadata struct {
		UserID string `json:"user_id"`
	} `json:"metadata"`
}

// parseRequest extracts fingerprints and diagnostics from an inbound body.
//
// A body that is not JSON, or is too large, yields a request whose skipped
// field is set. Skipping is not an error path: the request is still proxied
// normally, it just does not contribute to the cache attribution.
func parseRequest(body []byte, surface, model, clientIP string) request {
	req := request{surface: surface, model: model}

	if len(body) == 0 {
		req.skipped = skipEmptyBody
		return req
	}
	if len(body) > maxBodyBytes {
		req.skipped = skipBodyTooLarge
		return req
	}

	var shape bodyShape
	if err := json.Unmarshal(body, &shape); err != nil {
		req.skipped = skipUnparsable
		return req
	}

	req.fp.System = digest(shape.System)
	req.fp.Tools = digest(shape.Tools)
	req.markers = countCacheControl(shape.System) + countCacheControl(shape.Tools)

	req.msgCount = len(shape.Messages)
	req.msgHash = make([][32]byte, 0, min(len(shape.Messages), maxStoredMessageHashes))
	for i, msg := range shape.Messages {
		h := digest(msg)
		if i < maxStoredMessageHashes {
			req.msgHash = append(req.msgHash, h)
		}
		req.markers += countCacheControlInMessage(msg)
	}

	if n := len(shape.Messages); n > 0 {
		req.fp.First = req.msgHash[0]
		req.fp.LiveZone = digest(shape.Messages[n-1])
		if n > 1 {
			req.fp.Prefix = digestMessages(shape.Messages[:n-1])
		}
	}

	req.session = sessionKey(shape.Metadata.UserID, clientIP, req.fp.First)
	req.volatile = scanVolatile(shape.System, shape.Tools, prefixBytes(shape.Messages))
	return req
}

// digest returns the SHA-256 of raw, or the zero value when raw is empty. The
// zero value is what "this request has no system prompt" hashes to, so it must
// stay distinct from the digest of an empty-but-present value.
func digest(raw json.RawMessage) [32]byte {
	if len(raw) == 0 {
		return [32]byte{}
	}
	return sha256.Sum256(raw)
}

// digestMessages hashes a run of messages by chaining their individual digests,
// so a change to any message changes the result without concatenating the
// messages themselves.
func digestMessages(msgs []json.RawMessage) [32]byte {
	h := sha256.New()
	var sum [32]byte
	for _, m := range msgs {
		sum = sha256.Sum256(m)
		h.Write(sum[:])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// prefixBytes concatenates the messages that form the cache hot zone, for the
// volatile scan. Only those messages are scanned: the live zone is expected to
// change every turn, so a timestamp there is harmless.
func prefixBytes(msgs []json.RawMessage) []byte {
	if len(msgs) <= 1 {
		return nil
	}
	var out []byte
	for _, m := range msgs[:len(msgs)-1] {
		out = append(out, m...)
	}
	return out
}

// sessionKey identifies a conversation.
//
// Anthropic clients may send metadata.user_id, but it identifies a user rather
// than a conversation, so it is combined with the digest of the first message.
// Without either signal the client address is the only thing left; that
// over-merges concurrent conversations from one host, which shows up as
// spurious messages_rewritten rather than as a silent wrong answer.
func sessionKey(userID, clientIP string, first [32]byte) string {
	who := userID
	if who == "" {
		who = clientIP
	}
	h := sha256.New()
	h.Write([]byte(who))
	h.Write([]byte{0})
	h.Write(first[:])
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:16])
}

// countCacheControl counts explicit cache_control markers in a system prompt or
// tools array. Both arrive either as an array of blocks or, for a single tool,
// as one object.
func countCacheControl(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}

	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err == nil {
		n := 0
		for _, b := range blocks {
			if _, ok := b["cache_control"]; ok {
				n++
			}
		}
		return n
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		if _, ok := obj["cache_control"]; ok {
			return 1
		}
	}
	return 0
}

// countCacheControlInMessage counts markers on a message's content blocks and,
// defensively, on the message object itself (which is where the OpenAI-shaped
// surfaces put it).
func countCacheControlInMessage(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return 0
	}
	n := 0
	if _, ok := msg["cache_control"]; ok {
		n++
	}
	if content, ok := msg["content"]; ok {
		n += countCacheControl(content)
	}
	return n
}

// surfaceFromPath mirrors the usage package's mapping. Duplicating four string
// prefixes is cheaper than exporting a helper that only two callers need.
func surfaceFromPath(path string) string {
	switch {
	case hasPrefix(path, "/anthropic/"):
		return "anthropic"
	case hasPrefix(path, "/openai/"):
		return "openai"
	case hasPrefix(path, "/gemini/"):
		return "gemini"
	case hasPrefix(path, "/litellm/"):
		return "litellm"
	}
	return ""
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// skipped reasons. Each is a label value, so they must stay stable.
const (
	skipEmptyBody      = "empty_body"
	skipBodyTooLarge   = "body_too_large"
	skipUnparsable     = "unparsable_body"
	skipNotInference   = "not_inference_surface"
	skipNoRequestState = "no_request_state"
	skipFailedRequest  = "failed_request"
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

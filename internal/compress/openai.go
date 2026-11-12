package compress

import "encoding/json"

// CompressOpenAIRequest compresses tool output in an OpenAI-shaped request.
//
// Two wire shapes are covered by the same deterministic transform:
//
//   - Chat Completions: messages[] items with role "tool" (or the legacy
//     "function") and their "content".
//   - Responses: input[] items with type "function_call_output" and their
//     "output".
//
// Unlike the Anthropic form, a plain JSON string IS eligible here. On the
// OpenAI wire the role/type is an explicit, trustworthy marker that the text
// came from a tool rather than from the person talking to the model. A user,
// system, developer or assistant message is never touched, whatever its
// content looks like — that is the false-positive guard this shape gets for
// free, and the reason this transform cannot be shared with CompressRequest.
//
// The same rules as the Anthropic path apply: every tool output in the request
// is considered, history included, so the compressed prefix stays byte-identical
// across turns and the provider's implicit prefix cache keeps hitting. The
// transform is a pure function of the text, never fails a request, and returns
// either the argument itself or a shallow copy where only "messages" and/or
// "input" differ.
func CompressOpenAIRequest(m map[string]json.RawMessage) (out map[string]json.RawMessage, rep Report) {
	out = m
	if m == nil {
		return m, Report{Skipped: skipNoMessages}
	}
	// A compressor must never be able to fail a request. Every branch falls
	// back, and this catches the malformed shape nobody anticipated.
	defer func() {
		if r := recover(); r != nil {
			out = m
			rep = Report{Skipped: skipPanic}
		}
	}()

	rep.Strategy = StrategyLog
	arrays := []struct {
		key      string
		fieldFor func(map[string]json.RawMessage) string
	}{
		{"messages", chatToolOutputField},
		{"input", responsesToolOutputField},
	}

	replacements := make(map[string]json.RawMessage, len(arrays))
	reason := ""
	sawToolOutput := false
	for _, a := range arrays {
		raw, ok := m[a.key]
		if !ok {
			continue
		}
		encoded, saw, why, changed := compressOpenAIArray(raw, a.fieldFor, &rep)
		if saw {
			sawToolOutput = true
		}
		if why != "" && reason == "" {
			reason = why
		}
		if changed {
			replacements[a.key] = encoded
		}
	}

	if len(replacements) == 0 {
		switch {
		case reason != "":
			rep.Skipped = reason
		case !sawToolOutput:
			rep.Skipped = skipNoToolOutput
		}
		return m, rep
	}

	// Shallow copy: every other value is shared with the caller, and only the
	// arrays that actually changed are replaced.
	clone := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		clone[k] = v
	}
	for k, v := range replacements {
		clone[k] = v
	}
	return clone, rep
}

// compressOpenAIArray walks one messages/input array. fieldFor returns the name
// of the field holding tool output, or "" when the item is not tool output.
//
// The per-array report is merged only once the rewritten array has been
// marshalled successfully, so a failed marshal can never leave the metrics
// claiming bytes that never shipped.
func compressOpenAIArray(raw json.RawMessage, fieldFor func(map[string]json.RawMessage) string, rep *Report) (json.RawMessage, bool, string, bool) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false, skipBadMessages, false
	}
	out := make([]json.RawMessage, len(items))
	copy(out, items)

	local := Report{}
	reason := ""
	saw := false
	changed := false
	for i := range items {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(items[i], &obj); err != nil || obj == nil {
			continue
		}
		field := fieldFor(obj)
		if field == "" {
			continue
		}
		saw = true
		value, ok := obj[field]
		if !ok {
			if reason == "" {
				reason = skipNoContent
			}
			continue
		}
		newValue, delta, why := compressOpenAITextField(value)
		if why != "" && reason == "" {
			reason = why
		}
		if newValue == nil {
			local.merge(delta)
			continue
		}
		obj[field] = newValue
		rebuilt, err := json.Marshal(obj)
		if err != nil {
			continue
		}
		out[i] = rebuilt
		local.merge(delta)
		changed = true
	}

	if !changed {
		rep.merge(local)
		return nil, saw, reason, false
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, saw, skipBadMessages, false
	}
	rep.merge(local)
	return encoded, saw, "", true
}

// compressOpenAITextField compresses one tool-output value, which is either a
// JSON string or an array of text parts. It returns nil when nothing shipped, a
// Report with the deltas only for blocks that did, and the first skip reason.
//
// A part with a non-text type (image, file, ...) is left byte-for-byte alone;
// only parts carrying a "text" field are eligible.
func compressOpenAITextField(value json.RawMessage) (json.RawMessage, Report, string) {
	if isJSONString(value) {
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			return nil, Report{}, skipBadContent
		}
		compressed, why := compressText(text)
		if compressed == "" {
			return nil, Report{BlocksConsidered: 1}, why
		}
		encoded, err := json.Marshal(compressed)
		if err != nil {
			return nil, Report{BlocksConsidered: 1}, skipBadContent
		}
		return encoded, Report{
			BlocksConsidered: 1,
			BlocksCompressed: 1,
			OriginalBytes:    len(text),
			CompressedBytes:  len(compressed),
		}, ""
	}

	var parts []json.RawMessage
	if err := json.Unmarshal(value, &parts); err != nil {
		return nil, Report{}, skipBadContent
	}
	out := make([]json.RawMessage, len(parts))
	copy(out, parts)
	rep := Report{}
	reason := ""
	changed := false
	for i := range parts {
		var part map[string]json.RawMessage
		if err := json.Unmarshal(parts[i], &part); err != nil || part == nil {
			continue
		}
		if t := blockType(part); t != "" && t != "text" && t != "input_text" && t != "output_text" {
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
			if reason == "" && why != "" {
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
		out[i] = rebuilt
		rep.BlocksCompressed++
		rep.OriginalBytes += len(text)
		rep.CompressedBytes += len(compressed)
		changed = true
	}
	if !changed {
		return nil, rep, reason
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, Report{}, skipBadContent
	}
	return encoded, rep, ""
}

// chatToolOutputField identifies a Chat Completions tool message. The role is
// the trust boundary: a user message carrying the same log is never eligible.
func chatToolOutputField(obj map[string]json.RawMessage) string {
	raw, ok := obj["role"]
	if !ok {
		return ""
	}
	var role string
	if err := json.Unmarshal(raw, &role); err != nil {
		return ""
	}
	if role != "tool" && role != "function" {
		return ""
	}
	return "content"
}

// responsesToolOutputField identifies a Responses API function_call_output
// item, which is what Codex sends for the result of a shell/tool call.
func responsesToolOutputField(obj map[string]json.RawMessage) string {
	if blockType(obj) != "function_call_output" {
		return ""
	}
	return "output"
}

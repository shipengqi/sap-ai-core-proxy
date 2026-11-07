package agentconfig

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// The OpenCode config is JSONC: comments and trailing commas are legal, so a
// full decode/encode would silently drop them. This file implements a small
// structural scanner that locates top-level members and provider entries by
// byte range and splices only those ranges. Everything else — comments,
// formatting, unrelated settings — stays byte-for-byte intact.
type jsoncEditor struct {
	src []byte
}

type jsoncMember struct {
	key      string
	valStart int
	valEnd   int
	hasComma bool
	commaEnd int
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

func skipSpaceComments(src []byte, i int) int {
	for i < len(src) {
		switch {
		case isSpace(src[i]):
			i++
		case i+1 < len(src) && src[i] == '/' && src[i+1] == '/':
			i += 2
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case i+1 < len(src) && src[i] == '/' && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && (src[i] != '*' || src[i+1] != '/') {
				i++
			}
			if i+1 < len(src) {
				i += 2
			} else {
				i = len(src)
			}
		default:
			return i
		}
	}
	return i
}

func scanString(src []byte, i int) (int, error) {
	i++ // opening quote
	for i < len(src) {
		if src[i] == '\\' {
			i += 2
			continue
		}
		if src[i] == '"' {
			return i + 1, nil
		}
		i++
	}
	return 0, io.ErrUnexpectedEOF
}

func scanValue(src []byte, i int) (int, error) {
	i = skipSpaceComments(src, i)
	if i >= len(src) {
		return 0, io.ErrUnexpectedEOF
	}
	switch src[i] {
	case '"':
		return scanString(src, i)
	case '{', '[':
		depth := 0
		for i < len(src) {
			c := src[i]
			if c == '"' {
				j, err := scanString(src, i)
				if err != nil {
					return 0, err
				}
				i = j
				continue
			}
			if c == '/' && i+1 < len(src) && (src[i+1] == '/' || src[i+1] == '*') {
				i = skipSpaceComments(src, i)
				continue
			}
			if c == '{' || c == '[' {
				depth++
			}
			if c == '}' || c == ']' {
				depth--
				if depth == 0 {
					return i + 1, nil
				}
			}
			i++
		}
		return 0, io.ErrUnexpectedEOF
	default:
		j := i
		for j < len(src) && !isSpace(src[j]) && src[j] != ',' && src[j] != '}' && src[j] != ']' {
			j++
		}
		if j == i {
			return 0, fmt.Errorf("unexpected byte %q at %d", src[i], i)
		}
		return j, nil
	}
}

func objectMembers(src []byte, objStart int) ([]jsoncMember, error) {
	if objStart >= len(src) || src[objStart] != '{' {
		return nil, fmt.Errorf("expected object at byte %d", objStart)
	}
	i := objStart + 1
	var members []jsoncMember
	for {
		i = skipSpaceComments(src, i)
		if i >= len(src) {
			return nil, io.ErrUnexpectedEOF
		}
		if src[i] == '}' {
			return members, nil
		}
		if src[i] == ',' {
			i++
			continue
		}
		if src[i] != '"' {
			return nil, fmt.Errorf("expected object key at byte %d", i)
		}
		keyEnd, err := scanString(src, i)
		if err != nil {
			return nil, err
		}
		key := string(src[i+1 : keyEnd-1])
		i = skipSpaceComments(src, keyEnd)
		if i >= len(src) || src[i] != ':' {
			return nil, fmt.Errorf("expected ':' after %q", key)
		}
		i = skipSpaceComments(src, i+1)
		valStart := i
		valEnd, err := scanValue(src, i)
		if err != nil {
			return nil, fmt.Errorf("value of %q: %w", key, err)
		}
		m := jsoncMember{key: key, valStart: valStart, valEnd: valEnd}
		j := skipSpaceComments(src, valEnd)
		if j < len(src) && src[j] == ',' {
			m.hasComma = true
			m.commaEnd = j + 1
		}
		members = append(members, m)
		i = j
		if m.hasComma {
			continue
		}
		next := skipSpaceComments(src, i)
		if next < len(src) && src[next] == '}' {
			return members, nil
		}
		return nil, fmt.Errorf("expected ',' or '}' after %q", key)
	}
}

func (e *jsoncEditor) rootStart() (int, error) {
	i := skipSpaceComments(e.src, 0)
	if i >= len(e.src) || e.src[i] != '{' {
		return 0, fmt.Errorf("config root is not a JSON object")
	}
	return i, nil
}

func (e *jsoncEditor) members(objStart int) ([]jsoncMember, error) {
	return objectMembers(e.src, objStart)
}

func (e *jsoncEditor) splice(start, end int, text string) {
	out := make([]byte, 0, len(e.src)+len(text))
	out = append(out, e.src[:start]...)
	out = append(out, text...)
	out = append(out, e.src[end:]...)
	e.src = out
}

// setMember sets a key inside the object starting at objStart. Edits are
// applied immediately so subsequent lookups see the updated document.
func (e *jsoncEditor) setMember(objStart int, key, value, indent string) error {
	members, err := e.members(objStart)
	if err != nil {
		return err
	}
	for _, m := range members {
		if m.key == key {
			e.splice(m.valStart, m.valEnd, indentBlock(value, indent))
			return nil
		}
	}
	var pos int
	var text string
	if len(members) == 0 {
		pos = objStart + 1
		text = "\n" + indent + `"` + key + `": ` + indentBlock(value, indent) + "\n"
	} else {
		last := members[len(members)-1]
		if last.hasComma {
			pos = last.commaEnd
			text = "\n" + indent + `"` + key + `": ` + indentBlock(value, indent) + ","
		} else {
			pos = last.valEnd
			text = ",\n" + indent + `"` + key + `": ` + indentBlock(value, indent)
		}
	}
	e.splice(pos, pos, text)
	return nil
}

func (e *jsoncEditor) setRoot(key, value string) error {
	start, err := e.rootStart()
	if err != nil {
		return err
	}
	return e.setMember(start, key, value, "  ")
}

func (e *jsoncEditor) setProvider(id, value string) error {
	start, err := e.rootStart()
	if err != nil {
		return err
	}
	members, err := e.members(start)
	if err != nil {
		return err
	}
	for _, m := range members {
		if m.key != "providers" {
			continue
		}
		if m.valStart >= len(e.src) || e.src[m.valStart] != '{' {
			return fmt.Errorf("providers is not a JSON object")
		}
		return e.setMember(m.valStart, id, value, "    ")
	}
	// No providers object yet: create it with this first provider. Build the
	// inner object at relative indent 0 — setRoot applies its own indent to
	// every line, and the result must match what setProvider writes on later
	// runs or the plan would never be idempotent.
	obj := "{\n  \"" + id + "\": " + indentBlock(value, "  ") + "\n}"
	return e.setRoot("providers", obj)
}

// addPolicyAllows appends allow rules to an existing deny-by-default policies
// array. It only runs when the array actually contains a provider.use deny.
func (e *jsoncEditor) addPolicyAllows(ids []string) error {
	start, err := e.rootStart()
	if err != nil {
		return err
	}
	members, err := e.members(start)
	if err != nil {
		return err
	}
	for _, m := range members {
		if m.key != "policies" {
			continue
		}
		inner := string(e.src[m.valStart:m.valEnd])
		if !strings.Contains(inner, "provider.use") || !strings.Contains(inner, "deny") {
			return nil
		}
		var rules []string
		for _, id := range ids {
			if strings.Contains(inner, `"`+id+`"`) && strings.Contains(inner, "allow") {
				continue
			}
			rules = append(rules, fmt.Sprintf(`    { "action": "provider.use", "resource": %q, "effect": "allow" }`, id))
		}
		if len(rules) == 0 {
			return nil
		}
		content := strings.TrimSpace(inner)
		content = strings.TrimPrefix(content, "[")
		content = strings.TrimSuffix(content, "]")
		content = strings.TrimSpace(content)
		content = strings.TrimSuffix(strings.TrimSpace(content), ",")
		if content != "" {
			content += ","
		}
		replacement := "[\n" + content + "\n" + strings.Join(rules, ",\n") + "\n  ]"
		e.splice(m.valStart, m.valEnd, replacement)
		return nil
	}
	return nil
}

func indentBlock(value, indent string) string {
	lines := strings.Split(value, "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = indent + lines[i]
	}
	return strings.Join(lines, "\n")
}

func mustJSON(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		// All callers pass plain maps/strings; this cannot fail in practice.
		return "null"
	}
	return string(data)
}

// patchJSONC applies root values and provider objects to an existing JSONC
// document, creating a minimal document when the file is empty.
func patchJSONC(old []byte, root map[string]string, providers map[string]string, allowIDs []string) ([]byte, error) {
	editor := &jsoncEditor{src: append([]byte{}, old...)}
	if len(editor.src) == 0 || string(skipSpaceBytes(editor.src)) == "{}" {
		editor.src = []byte("{}")
	}
	// Stable ordering: schema first, then model, then providers.
	for _, key := range []string{"$schema", "model"} {
		if value, ok := root[key]; ok {
			if err := editor.setRoot(key, value); err != nil {
				return nil, err
			}
		}
	}
	for _, key := range sortedKeys(root) {
		if key == "$schema" || key == "model" {
			continue
		}
		if err := editor.setRoot(key, root[key]); err != nil {
			return nil, err
		}
	}
	for _, id := range []string{OpenCodeOpenAIID, OpenCodeAnthropicID} {
		if value, ok := providers[id]; ok {
			if err := editor.setProvider(id, value); err != nil {
				return nil, err
			}
		}
	}
	if err := editor.addPolicyAllows(allowIDs); err != nil {
		return nil, err
	}
	return editor.src, nil
}

func skipSpaceBytes(src []byte) []byte {
	i := skipSpaceComments(src, 0)
	if i >= len(src) {
		return nil
	}
	return src[i:]
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

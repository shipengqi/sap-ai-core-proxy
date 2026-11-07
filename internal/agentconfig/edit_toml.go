package agentconfig

import (
	"fmt"
	"strings"
)

// patchTOML edits config.toml at the line level. A full TOML parse+encode is
// deliberately avoided: Codex config files contain single-quoted Windows UNC
// paths, comments and ordering that a round trip would rewrite.
//
// Top-level keys are inserted before the first [table] header — Codex's own
// config reference states "root keys must be listed before tables". A matching
// section is replaced in place; otherwise it is appended.
func patchTOML(old []byte, top map[string]string, sectionHeader, sectionBody string) ([]byte, error) {
	src := string(old)
	ending := "\n"
	if strings.Contains(src, "\r\n") {
		ending = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	// 1. Replace or append the managed provider section.
	lines = patchSection(lines, sectionHeader, strings.Split(strings.TrimSuffix(sectionBody, "\n"), "\n"))

	// 2. Replace or insert top-level keys before the first table header.
	firstTable := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			firstTable = i
			break
		}
	}
	insertAt := firstTable
	if insertAt < 0 {
		insertAt = len(lines)
	}
	pending := make([]string, 0, len(top))
	for key, value := range top {
		replacement := fmt.Sprintf("%s = %s", key, value)
		found := false
		for i := 0; i < insertAt; i++ {
			trimmed := strings.TrimSpace(lines[i])
			name, _, ok := strings.Cut(trimmed, "=")
			if ok && strings.TrimSpace(name) == key {
				lines[i] = replacement
				found = true
				break
			}
		}
		if !found {
			pending = append(pending, replacement)
		}
	}
	if len(pending) > 0 {
		// Keep a stable order regardless of map iteration, and leave a blank
		// line between top-level keys and the first table header.
		sortStrings(pending)
		if insertAt < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[insertAt]), "[") {
			pending = append(pending, "")
		}
		updated := make([]string, 0, len(lines)+len(pending))
		updated = append(updated, lines[:insertAt]...)
		updated = append(updated, pending...)
		updated = append(updated, lines[insertAt:]...)
		lines = updated
	}
	if len(lines) == 0 {
		return nil, nil
	}
	return []byte(strings.Join(lines, ending) + ending), nil
}

func patchSection(lines []string, header string, body []string) []string {
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == header {
			start = i
			break
		}
	}
	if start < 0 {
		result := append([]string{}, lines...)
		if len(result) > 0 && strings.TrimSpace(result[len(result)-1]) != "" {
			result = append(result, "")
		}
		return append(result, body...)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			end = i
			break
		}
	}
	result := make([]string, 0, len(lines)-(end-start)+len(body))
	result = append(result, lines[:start]...)
	result = append(result, body...)
	result = append(result, lines[end:]...)
	return result
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

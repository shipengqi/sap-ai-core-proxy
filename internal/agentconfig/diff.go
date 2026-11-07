package agentconfig

import (
	"fmt"
	"strings"
)

// Diff renders a compact, readable preview. It trims the common line prefix
// and suffix and prints the changed middle, which is enough to review a small
// config file without pulling in a diff dependency.
func Diff(path string, old, new []byte) string {
	var b strings.Builder
	if len(old) == 0 {
		fmt.Fprintf(&b, "--- %s (new file)\n", path)
		for _, line := range splitLines(string(new)) {
			fmt.Fprintf(&b, "+ %s\n", line)
		}
		return b.String()
	}
	oldLines := splitLines(string(old))
	newLines := splitLines(string(new))
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	fmt.Fprintf(&b, "--- %s\n+++ %s\n", path, path)
	start := prefix - 2
	if start < 0 {
		start = 0
	}
	for i := start; i < prefix; i++ {
		fmt.Fprintf(&b, "  %s\n", oldLines[i])
	}
	for i := prefix; i < len(oldLines)-suffix; i++ {
		fmt.Fprintf(&b, "- %s\n", oldLines[i])
	}
	for i := prefix; i < len(newLines)-suffix; i++ {
		fmt.Fprintf(&b, "+ %s\n", newLines[i])
	}
	end := len(newLines)
	if len(newLines)-suffix+2 < end {
		end = len(newLines) - suffix + 2
	}
	for i := len(newLines) - suffix; i < end; i++ {
		fmt.Fprintf(&b, "  %s\n", newLines[i])
	}
	return b.String()
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

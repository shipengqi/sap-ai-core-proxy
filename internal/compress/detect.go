package compress

import (
	"regexp"
	"strings"
)

// Detection thresholds.
const (
	// minLogLines is the shortest text that can be a log. A handful of lines is
	// a snippet, and dropping lines from a snippet loses more than it saves.
	minLogLines = 8
	// signalRatio is the fraction of non-blank lines that must look like log
	// output. High on purpose: the cost of compressing something that is not a
	// log is a silently corrupted request.
	signalRatio = 0.30
)

// Level / framework patterns.
//
// Every one of them is anchored at the start of the line (after an optional
// bracket, timestamp or logger name). That anchoring is what keeps the two
// false-positive classes out: a Go source file that declares `const ERROR = ...`
// and a JSON document with an `"error"` field both never match, because neither
// has the level token at the head of the line.
// levelFollower is what may follow the level token: a colon, a bracket, a bang,
// or whitespace followed by a word character (the common `TIMESTAMP LEVEL
// message` and `ERROR in ./src/x.js` shapes).
//
// The last alternative is why `const ERROR = "error"` does not match: after the
// level token there are only spaces and then `=`, which is not in the class, so
// no path through the regex matches. RE2 explores every path, so one bad
// alternative is not enough to sneak through.
const levelFollower = `(?:[:|\]!]|\s+[A-Za-z0-9_./-])`

var (
	// reLevel matches `2024-01-01T00:00:00Z ERROR: ...`, `[ERROR] ...`,
	// `app ERROR: ...`, `ERROR worker-1 upstream timeout` and `npm ERR! ...`.
	reLevel = regexp.MustCompile(`^(?:\[[^\]]{0,64}\]\s*)?` +
		`(?:\d{4}-\d{2}-\d{2}(?:[T ][0-9:.]{4,15})?\s*)?` +
		`(?:[A-Za-z0-9_.$-]{1,64}\s+)?` +
		`(ERROR|ERR|FATAL|CRIT|CRITICAL|SEVERE|EMERG|ALERT|PANIC)\b` + levelFollower)

	reWarnLevel = regexp.MustCompile(`^(?:\[[^\]]{0,64}\]\s*)?` +
		`(?:\d{4}-\d{2}-\d{2}(?:[T ][0-9:.]{4,15})?\s*)?` +
		`(?:[A-Za-z0-9_.$-]{1,64}\s+)?` +
		`(WARN|WARNING)\b` + levelFollower)

	reInfoLevel = regexp.MustCompile(`^(?:\[[^\]]{0,64}\]\s*)?` +
		`(?:\d{4}-\d{2}-\d{2}(?:[T ][0-9:.]{4,15})?\s*)?` +
		`(?:[A-Za-z0-9_.$-]{1,64}\s+)?` +
		`(INFO|DEBUG|TRACE|NOTICE|VERBOSE)\b` + levelFollower)

	// Lowercase compiler and toolchain diagnostics: `error: ...`,
	// `error[E0308]: ...`, `warning: ...`, `fatal: ...`.
	reLowerError = regexp.MustCompile(`^(?:error|fatal)(?:\[[A-Za-z]?[0-9]+\])?\s*:`)
	reLowerWarn  = regexp.MustCompile(`^warning\s*:`)
	rePanic      = regexp.MustCompile(`^(?:panic|fatal error)\s*:`)

	// An exception line: `ValueError:`, `AssertionError:`, and pytest's
	// `E       AssertionError: ...` (the optional leading token covers the
	// pytest `E` gutter).
	reException = regexp.MustCompile(`^(?:[A-Za-z_][A-Za-z0-9_.]*\s+)?` +
		`[A-Za-z_][A-Za-z0-9_.]*(?:Error|Exception|Exit|Interrupt|Failure)\b\s*:`)

	// Stack frames. `File "x.py", line 12` (Python), `at foo (a.js:1:2)`
	// (JS/Java), `pkg/file.go:12:0` (Go), `--> src/main.rs:4:5` (rustc).
	rePyFile  = regexp.MustCompile(`^File "[^"]*", line [0-9]+`)
	reGoFrame = regexp.MustCompile(`^[A-Za-z0-9_./-]+\.(?:go|rs|java|ts|js|tsx|jsx|py|c|cpp|rb):[0-9]+`)

	// Summary lines: `2 failed, 126 passed`, `1 failed`, `collected 128 items`,
	// `test result: FAILED`, `BUILD FAILED`.
	reSummary = regexp.MustCompile(`^(?:[0-9]+\s+(?:passed|failed|error|errors|warning|warnings|skipped|test|tests|assertions|examples|failures)\b` +
		`|test result:` +
		`|collected [0-9]+ items` +
		`|BUILD (?:SUCCESSFUL|FAILED)` +
		`|[0-9]+ of [0-9]+ (?:tests?|assertions?))`)
)

// LooksLikeLog reports whether text is log, build or test output.
//
// It is deliberately conservative: a false positive silently rewrites content
// the model needed. Two independent tests have to agree:
//
//  1. the ratio test — at least signalRatio (30%) of the non-blank lines carry a
//     recognisable log marker; or
//  2. a structural test — real tool output is *mostly* signal-free (pytest
//     progress dots, echoed source lines, blank separators), so a pure ratio
//     test rejects it. In that case two independent structural markers must
//     corroborate: errors plus a stack, errors plus a summary, or a test-result
//     line plus a summary. A single marker is never enough.
//
// Both false-positive classes the package pins in its tests — a Go source file
// that declares an ERROR constant, and a JSON document with "error" fields —
// fail every one of these tests, because their markers are never at the head of
// a line.
func LooksLikeLog(text string) bool {
	lines := splitLines(text)
	if len(lines) < minLogLines {
		return false
	}

	regions := findStackRegions(lines)

	var nonBlank, signals, errors, tests, summaries int
	for _, l := range lines {
		t := strings.TrimSpace(l.text)
		if t == "" {
			continue
		}
		nonBlank++
		if isLogSignal(t) {
			signals++
		}
		switch {
		case isErrorLine(t):
			errors++
		case isFailLine(t):
			tests++
		}
		if isSummaryLine(t) {
			summaries++
		}
	}
	if nonBlank == 0 {
		return false
	}

	if float64(signals)/float64(nonBlank) >= signalRatio {
		return true
	}

	// Structural corroboration. See the doc comment: a single marker is never
	// enough. A stack counts either as a real multi-line region or as a single
	// recognisable frame line (rustc and pytest emit lone location lines).
	hasStack := false
	for _, r := range regions {
		if r.end-r.start >= 1 {
			hasStack = true
			break
		}
	}
	stackLines := 0
	for _, l := range lines {
		if t := strings.TrimSpace(l.text); t != "" && isStackSignal(t) {
			stackLines++
		}
	}
	corroborated := summaries >= 1 || hasStack || stackLines >= 1

	switch {
	case errors >= 3:
		// Three error lines in one text is not something source code does.
		return true
	case errors >= 2 && corroborated:
		return true
	case tests >= 1 && corroborated:
		return true
	}
	return false
}

// isLogSignal reports whether one non-blank line carries any log marker. It is
// the numerator of the ratio test only; classification for compression is done
// separately by classifyLines.
func isLogSignal(t string) bool {
	return isErrorLine(t) || isFailLine(t) || isWarnLine(t) ||
		isInfoLine(t) || isSummaryLine(t) || isStackSignal(t)
}

// isErrorLine reports whether the line is an error, fatal or exception line.
func isErrorLine(t string) bool {
	return reLevel.MatchString(t) || reLowerError.MatchString(t) ||
		rePanic.MatchString(t) || reException.MatchString(t)
}

// isWarnLine reports whether the line is a warning.
func isWarnLine(t string) bool {
	return reWarnLevel.MatchString(t) || reLowerWarn.MatchString(t)
}

// isInfoLine reports whether the line is an informational log line.
func isInfoLine(t string) bool {
	return reInfoLevel.MatchString(t)
}

// isSummaryLine reports whether the line is a counts/totals line.
func isSummaryLine(t string) bool {
	return reSummary.MatchString(t)
}

// isFailLine reports whether the line is a test-framework result line: pytest,
// go test, jest, mocha, cargo test.
func isFailLine(t string) bool {
	switch {
	case strings.HasPrefix(t, "--- FAIL"), strings.HasPrefix(t, "--- PASS"), strings.HasPrefix(t, "--- SKIP"):
		return true
	case strings.HasPrefix(t, "=== RUN"), strings.HasPrefix(t, "=== PAUSE"), strings.HasPrefix(t, "=== CONT"):
		return true
	case strings.HasPrefix(t, "FAILED "), strings.HasPrefix(t, "PASSED "), strings.HasPrefix(t, "SKIPPED "):
		return true
	case strings.HasPrefix(t, "FAIL\t"), strings.HasPrefix(t, "FAIL "), strings.HasPrefix(t, "PASS "):
		return true
	case strings.HasPrefix(t, "ok  "), strings.HasPrefix(t, "ok\t"):
		return true
	// jest / mocha glyphs.
	case strings.HasPrefix(t, "✕"), strings.HasPrefix(t, "✓"), strings.HasPrefix(t, "×"),
		strings.HasPrefix(t, "✔"), strings.HasPrefix(t, "✗"), strings.HasPrefix(t, "●"):
		return true
	// jest / mocha / phpunit block headers.
	case strings.HasPrefix(t, "Tests:"), strings.HasPrefix(t, "Test Suites:"),
		strings.HasPrefix(t, "Snapshots:"), strings.HasPrefix(t, "Time:"):
		return true
	case strings.HasPrefix(t, "test result:"), strings.HasPrefix(t, "running "):
		return true
	}
	return false
}

// isStackSignal reports whether the line is part of a stack trace.
func isStackSignal(t string) bool {
	return isFrameLine(t) || isTracebackHeader(t) ||
		strings.HasPrefix(t, "Caused by:") || strings.HasPrefix(t, "... ") && strings.HasSuffix(t, "more")
}

// isFrameLine reports whether the line is a single stack frame.
func isFrameLine(t string) bool {
	return strings.HasPrefix(t, "at ") || rePyFile.MatchString(t) ||
		reGoFrame.MatchString(t) || strings.HasPrefix(t, "--> ")
}

// isTracebackHeader reports whether the line starts a Python traceback.
func isTracebackHeader(t string) bool {
	return strings.HasPrefix(t, "Traceback (most recent call last)")
}

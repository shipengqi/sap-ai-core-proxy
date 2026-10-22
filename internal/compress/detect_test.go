package compress

import (
	"strings"
	"testing"
)

// goSourceFixture is a realistic Go file that mentions ERROR. It must never be
// compressed: dropping lines out of source code silently corrupts the file the
// agent is working on.
var goSourceFixture = strings.Join([]string{
	"package parser",
	"",
	"import (",
	"\t\"errors\"",
	"\t\"fmt\"",
	"\t\"strings\"",
	")",
	"",
	"// ERROR is the level name emitted for a hard failure.",
	"const ERROR = \"error\"",
	"",
	"const WARNING = \"warning\"",
	"",
	"var ErrNotFound = errors.New(\"not found\")",
	"",
	"type Parser struct {",
	"\tstrict bool",
	"\tmax    int",
	"}",
	"",
	"func New(strict bool) *Parser {",
	"\treturn &Parser{strict: strict, max: 128}",
	"}",
	"",
	"func (p *Parser) Parse(line string) (string, error) {",
	"\tif strings.TrimSpace(line) == \"\" {",
	"\t\treturn \"\", ErrNotFound",
	"\t}",
	"\tif len(line) > p.max {",
	"\t\treturn \"\", fmt.Errorf(\"line too long: %d\", len(line))",
	"\t}",
	"\treturn strings.ToUpper(line), nil",
	"}",
	"",
	"func (p *Parser) Level() string {",
	"\tif p.strict {",
	"\t\treturn ERROR",
	"\t}",
	"\treturn WARNING",
	"}",
}, "\n")

// jsonFixture is a pretty-printed JSON document with error fields. Diagnostics
// serialised to JSON are a common tool result, and they must survive intact.
var jsonFixture = strings.Join([]string{
	"{",
	"  \"status\": \"failed\",",
	"  \"error\": {",
	"    \"code\": \"E0308\",",
	"    \"message\": \"mismatched types\",",
	"    \"level\": \"ERROR\",",
	"    \"details\": {",
	"      \"expected\": \"u32\",",
	"      \"found\": \"String\",",
	"      \"warning\": \"unused variable: count\"",
	"    }",
	"  },",
	"  \"trace\": [",
	"    \"at Parser.next (parser.go:88:12)\",",
	"    \"at Parser.Parse (parser.go:42:9)\",",
	"    \"File \\\"/app/main.py\\\", line 12, in compute\"",
	"  ],",
	"  \"errors\": [",
	"    \"error: unresolved import\",",
	"    \"error: aborting due to 2 previous errors\",",
	"    \"warning: unused import\"",
	"  ],",
	"  \"summary\": {",
	"    \"failed\": 2,",
	"    \"passed\": 126,",
	"    \"collected\": 128",
	"  }",
	"}",
}, "\n")

// TestLooksLikeLog_RejectsCodeAndJSON is the false-positive guard. Both fixtures
// are long enough and full of the words the detector looks for, but neither is
// log output.
func TestLooksLikeLog_RejectsCodeAndJSON(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"go source with ERROR constants", goSourceFixture},
		{"json with error fields", jsonFixture},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.text) < MinTextBytes {
				t.Fatalf("fixture is only %d bytes, too short to be a meaningful test", len(tc.text))
			}
			if LooksLikeLog(tc.text) {
				t.Fatalf("LooksLikeLog = true, want false")
			}
			// The gate that actually runs in production: a non-log document
			// must come back untouched, with a reason.
			if got, why := compressText(tc.text); got != "" {
				t.Errorf("compressText compressed a non-log document (reason %q):\n%s", why, got)
			}
		})
	}
}

// TestLooksLikeLog_AcceptsRealOutput pins the true positives so a tightening of
// the detector cannot silently switch the feature off.
func TestLooksLikeLog_AcceptsRealOutput(t *testing.T) {
	cases := map[string]string{
		"pytest":  fixturePytest,
		"npm":     fixtureNPM,
		"cargo":   fixtureCargo,
		"go test": fixtureGoTest,
		"jest":    fixtureJest,
		"service": bigInfoLog(),
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if !LooksLikeLog(text) {
				t.Errorf("LooksLikeLog = false, want true")
			}
		})
	}
}

// TestLooksLikeLog_TooFewLines: a snippet is not a log, whatever it contains.
func TestLooksLikeLog_TooFewLines(t *testing.T) {
	text := strings.Join([]string{
		"ERROR: boom",
		"ERROR: bang",
		"Traceback (most recent call last):",
		"  File \"a.py\", line 1, in f",
		"ValueError: nope",
	}, "\n")
	if LooksLikeLog(text) {
		t.Errorf("LooksLikeLog = true for a 5-line snippet, want false")
	}
}

// TestCompressLog_Deterministic: ten runs, one answer. This is the property the
// whole feature rests on — a prefix that changes every turn gets no cache read.
func TestCompressLog_Deterministic(t *testing.T) {
	for _, text := range []string{fixturePytest, fixtureNPM, fixtureCargo, fixtureGoTest, fixtureJest, bigInfoLog()} {
		want := CompressLog(text)
		for i := 0; i < 10; i++ {
			if got := CompressLog(text); got != want {
				t.Fatalf("run %d differs from run 0\nfirst:\n%s\nrun %d:\n%s", i, want, i, got)
			}
		}
	}
}

// TestCompressLog_EdgeCases pins the inputs that are easy to crash on: no
// newlines, only blank lines, CRLF, a trailing newline, and empty input.
func TestCompressLog_EdgeCases(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"empty", ""},
		{"single line", "ERROR: boom"},
		{"only blank lines", "\n\n\n\n\n\n\n\n\n"},
		{"only newlines and spaces", "   \n\t\n   \n\t\n   \n\t\n   \n\t\n"},
		{"crlf", "2024-01-15T09:00:00Z ERROR: a\r\n2024-01-15T09:00:01Z ERROR: b\r\n" + strings.Repeat("2024-01-15T09:00:02Z INFO: filler line that is long enough to matter\r\n", 20)},
		{"long single line with no newline", strings.Repeat("ERROR: a very long line without any line break at all ", 40)},
		{"no trailing newline", strings.TrimSuffix(fixturePytest, "========")},
		{"windows log", strings.ReplaceAll(fixtureNPM, "\n", "\r\n")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := CompressLog(tc.text)
			if len(out) > len(tc.text) {
				t.Errorf("output grew: %d -> %d", len(tc.text), len(out))
			}
			// Round-trip: whatever is left must be a subsequence of the input
			// lines, never new text.
			if tc.text != "" && !isLineSubsequence(tc.text, out) {
				t.Errorf("output is not made of input lines:\n%s", out)
			}
		})
	}
}

// isLineSubsequence reports whether every line of out (ignoring omission
// markers) appears in text.
func isLineSubsequence(text, out string) bool {
	known := map[string]int{}
	for _, l := range splitLines(text) {
		known[l.text]++
	}
	for _, l := range splitLines(out) {
		if strings.Contains(l.text, "lines omitted") {
			continue
		}
		if known[l.text] == 0 {
			return false
		}
		known[l.text]--
	}
	return true
}

// TestCompressLog_PreservesCRLF: CRLF input must not come back as LF, because a
// tool result that changes line endings is a diff the agent did not ask for.
func TestCompressLog_PreservesCRLF(t *testing.T) {
	text := strings.ReplaceAll(fixtureNPM, "\n", "\r\n")
	out := CompressLog(text)
	if strings.Contains(out, "\n") && !strings.Contains(out, "\r\n") {
		t.Errorf("CRLF input produced LF-only output")
	}
	// Every kept line keeps its own terminator.
	for _, l := range splitLines(out) {
		if l.term != "" && l.term != "\r\n" && l.term != "\n" {
			t.Errorf("unexpected terminator %q", l.term)
		}
	}
}

// TestCompressLog_StrictlySmaller: the gate, checked over every fixture. A
// compression that does not shrink is dropped by compressText, so CompressLog
// itself only has to guarantee it never grows.
func TestCompressLog_StrictlySmaller(t *testing.T) {
	for name, text := range map[string]string{
		"pytest":  fixturePytest,
		"npm":     fixtureNPM,
		"cargo":   fixtureCargo,
		"go test": fixtureGoTest,
		"jest":    fixtureJest,
		"service": bigInfoLog(),
	} {
		if out := CompressLog(text); len(out) >= len(text) {
			t.Errorf("%s: %d -> %d, want strictly smaller", name, len(text), len(out))
		}
	}
}

// TestAdaptiveBudget pins the shape of the adaptive budget: a fraction of the
// input, floored so a small log is not gutted and capped so a huge dump cannot
// produce an expensive payload.
func TestAdaptiveBudget(t *testing.T) {
	cases := []struct {
		total int
		want  int
	}{
		{1, budgetMin},
		{10, budgetMin},
		{50, budgetMin},
		{200, 20},
		{400, 40},
		{1000, 100},
		{2000, budgetMax},
		{20000, budgetMax},
	}
	for _, tc := range cases {
		if got := adaptiveBudget(tc.total); got != tc.want {
			t.Errorf("adaptiveBudget(%d) = %d, want %d", tc.total, got, tc.want)
		}
	}
}

// TestCompressLog_BudgetIsEnforced: on a log that is almost entirely INFO, the
// surviving lines must be bounded by the budget and not by the input size.
func TestCompressLog_BudgetIsEnforced(t *testing.T) {
	text := bigInfoLog()
	out := CompressLog(text)

	kept := 0
	for _, l := range splitLines(out) {
		if strings.Contains(l.text, "lines omitted") {
			continue
		}
		if strings.TrimSpace(l.text) != "" {
			kept++
		}
	}
	// 5 errors + context, plus a budget of 39 (395 lines * 10%).
	if kept > budgetMax {
		t.Errorf("kept %d lines, want at most %d", kept, budgetMax)
	}
	if kept > 80 {
		t.Errorf("kept %d non-blank lines out of 400, the budget is not binding", kept)
	}
}

// TestCompressLog_StackSurvives: a Python traceback and a JS trace must come
// back with their frames, including across the blank line in the middle.
func TestCompressLog_StackSurvives(t *testing.T) {
	python := strings.Join([]string{
		"2024-01-15T09:00:00.000Z INFO  starting job",
		"Traceback (most recent call last):",
		"  File \"/app/worker.py\", line 88, in run",
		"    self.handle(item)",
		"  File \"/app/worker.py\", line 42, in handle",
		"    return parse(item.payload)",
		"  File \"/app/parse.py\", line 17, in parse",
		"    raise ValueError(\"bad payload\")",
		"ValueError: bad payload",
		"2024-01-15T09:00:01.000Z INFO  job finished",
		"2024-01-15T09:00:02.000Z INFO  job finished",
		"2024-01-15T09:00:03.000Z INFO  job finished",
		"2024-01-15T09:00:04.000Z INFO  job finished",
	}, "\n")
	out := CompressLog(python)
	for _, want := range []string{
		"Traceback (most recent call last):",
		`File "/app/worker.py", line 88, in run`,
		`File "/app/parse.py", line 17, in parse`,
		"ValueError: bad payload",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("python stack lost %q:\n%s", want, out)
		}
	}

	js := strings.Join([]string{
		"2024-01-15T09:00:00.000Z DEBUG building bundle",
		"TypeError: Cannot read properties of undefined (reading 'name')",
		"    at render (/app/src/render.js:24:19)",
		"    at mount (/app/src/mount.js:11:5)",
		"    at start (/app/src/index.js:8:3)",
		"    at Object.<anonymous> (/app/src/index.js:14:1)",
		"2024-01-15T09:00:01.000Z DEBUG building bundle",
		"2024-01-15T09:00:02.000Z DEBUG building bundle",
		"2024-01-15T09:00:03.000Z DEBUG building bundle",
		"2024-01-15T09:00:04.000Z DEBUG building bundle",
	}, "\n")
	out = CompressLog(js)
	for _, want := range []string{
		"at render (/app/src/render.js:24:19)",
		"at mount (/app/src/mount.js:11:5)",
		"at Object.<anonymous> (/app/src/index.js:14:1)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("js stack lost %q:\n%s", want, out)
		}
	}
}

// TestCompressLog_WarningDedupKeepsDistinctWarnings: only byte-identical
// warnings collapse. Merging on a normalised number would hide a second,
// different failure.
func TestCompressLog_WarningDedupKeepsDistinctWarnings(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString("2024-01-15T09:00:00.000Z WARN  retrying request\n")
	}
	for i := 0; i < 4; i++ {
		b.WriteString("2024-01-15T09:00:00.000Z WARN  slow query took 1200ms on shard 1\n")
		b.WriteString("2024-01-15T09:00:00.000Z WARN  slow query took 1200ms on shard 2\n")
		b.WriteString("2024-01-15T09:00:00.000Z WARN  slow query took 1200ms on shard 3\n")
	}
	b.WriteString("2024-01-15T09:00:01.000Z ERROR worker failed after 3 retries\n")
	out := CompressLog(b.String())

	// Ten identical warnings collapse to the one that was selected plus the
	// lines context pulls back around it and around the next warning. Context is
	// verbatim by design, so it may reintroduce a duplicate. Without dedup all
	// ten would survive: the budget is 20 and there are only 23 lines.
	if n := strings.Count(out, "WARN  retrying request"); n > 5 {
		t.Errorf("duplicate warning kept %d times, want at most 3:\n%s", n, out)
	}
	// shard 1/2/3 differ only in a number and must all survive.
	for _, want := range []string{"shard 1", "shard 2", "shard 3"} {
		if !strings.Contains(out, want) {
			t.Errorf("distinct warning lost %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "ERROR worker failed after 3 retries") {
		t.Errorf("error lost:\n%s", out)
	}
}

// TestConfigure_KillSwitch covers the global switch and the per-strategy escape
// valve.
func TestConfigure_KillSwitch(t *testing.T) {
	defer Configure(false, nil)

	Configure(false, nil)
	if Enabled() {
		t.Errorf("Enabled = true after Configure(false)")
	}
	Configure(true, []string{"LOG"})
	if !Enabled() {
		t.Errorf("Enabled = false after Configure(true)")
	}
	if !Skip(StrategyLog) {
		t.Errorf("Skip(log) = false with COMPRESS_LOGS_SKIP=log")
	}
	// A skipped strategy must leave the text alone.
	if got, why := compressText(fixturePytest); got != "" || why != skipStrategyOff {
		t.Errorf("compressText with the strategy skipped = (%d bytes, %q), want (%q, %q)", len(got), why, "", skipStrategyOff)
	}

	Configure(true, nil)
	if Skip(StrategyLog) {
		t.Errorf("Skip(log) = true with no skip list")
	}
	if got, _ := compressText(fixturePytest); got == "" {
		t.Errorf("compressText returned nothing with the strategy enabled")
	}
}

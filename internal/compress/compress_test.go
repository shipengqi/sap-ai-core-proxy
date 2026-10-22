package compress

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The fixtures below are real shapes, not toys: pytest -v, webpack via npm,
// cargo, go test and jest. Each one is used to assert three things a coding
// agent actually cares about — the output shrank, every error survived, and the
// stack and the summary survived.

var fixturePytest = strings.Join([]string{
	"============================= test session starts ==============================",
	"platform linux -- Python 3.11.4, pytest-7.4.0, pluggy-1.2.0",
	"rootdir: /app",
	"configfile: pyproject.toml",
	"plugins: anyio-3.7.1, cov-4.1.0, mock-3.11.1",
	"collected 128 items",
	"",
	pytestProgress(),
	"================================== FAILURES ===================================",
	"_______________________________ test_update_user ______________________________",
	"",
	"    def test_update_user(client):",
	"        resp = client.patch(\"/users/1\", json={\"name\": \"new\"})",
	">       assert resp.status_code == 200",
	"E       AssertionError: assert 404 == 200",
	"E        +  where 404 = Response(client.patch('/users/1')).status_code",
	"",
	"tests/test_api.py:42: AssertionError",
	"_______________________________ test_delete_user ______________________________",
	"",
	"    def test_delete_user(client):",
	"        resp = client.delete(\"/users/1\")",
	">       assert resp.status_code == 204",
	"E       AssertionError: assert 500 == 204",
	"E        +  where 500 = Response(client.delete('/users/1')).status_code",
	"",
	"tests/test_api.py:58: AssertionError",
	"=========================== short test summary info ============================",
	"FAILED tests/test_api.py::test_update_user - AssertionError: assert 404 == 200",
	"FAILED tests/test_api.py::test_delete_user - AssertionError: assert 500 == 204",
	"========================= 2 failed, 126 passed in 4.53s ========================",
}, "\n")

// pytestProgress is the verbose progress section of a 120-test run. It is the
// bulk of the bytes and carries no signal, which is exactly what makes it the
// part worth dropping.
func pytestProgress() string {
	var b strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "tests/test_api.py::test_case_%03d PASSED                                  [%3d%%]\n", i, i*100/120)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

var fixtureNPM = strings.Join([]string{
	"",
	"> app@1.0.0 build",
	"> webpack --mode production",
	"",
	"ERROR in ./src/app.js 5:0-42",
	"Module not found: Error: Can't resolve './components/Missing' in '/app/src'",
	"resolve './components/Missing' in '/app/src'",
	"  using description file: /app/package.json (relative path: ./src)",
	"    Field 'browser' doesn't contain a valid alias configuration",
	"    using description file: /app/package.json (relative path: ./src/components)",
	"      no extension",
	"        /app/src/components/Missing doesn't exist",
	"      .js",
	"        /app/src/components/Missing.js doesn't exist",
	"      .jsx",
	"        /app/src/components/Missing.jsx doesn't exist",
	" @ ./src/router.js 3:0-36",
	" @ ./src/index.js 2:0-14",
	" @ multi ./src/index.js",
	"",
	"ERROR in ./src/router.js 12:16-30",
	"Module not found: Error: Can't resolve 'react-router-dom' in '/app/src'",
	" @ ./src/router.js 12:16-30",
	" @ ./src/index.js 4:0-20",
	"",
	"webpack 5.89.0 compiled with 2 errors and 1 warning in 1234 ms",
	"npm ERR! code ELIFECYCLE",
	"npm ERR! errno 2",
	"npm ERR! app@1.0.0 build: `webpack --mode production`",
	"npm ERR! Exit status 2",
	"npm ERR! Failed at the app@1.0.0 build script.",
	"npm ERR! This is probably not a problem with npm. There is likely additional logging output above.",
	"npm ERR! A complete log of this run can be found in: /root/.npm/_logs/debug-0.log",
}, "\n")

var fixtureCargo = strings.Join([]string{
	"   Compiling app v0.1.0 (/app)",
	"warning: unused variable: `count`",
	"  --> src/main.rs:4:9",
	"   |",
	" 4 |     let count = compute();",
	"   |         ^^^^^ help: if this is intentional, prefix it with an underscore: `_count`",
	"   |",
	"   = note: `#[warn(unused_variables)]` on by default",
	"",
	"error[E0308]: mismatched types",
	" --> src/main.rs:12:5",
	"  |",
	"12 |     total",
	"  |     ^^^^^ expected `u32`, found `&str`",
	"  |",
	"  = note: expected type `u32`",
	"           found type `&str`",
	"",
	"error[E0308]: mismatched types",
	" --> src/lib.rs:8:20",
	"  |",
	"8  |     return format!(\"{}\", value);",
	"   |            ^^^^^^^^^^^^^^^^^^^^ expected `()`, found `String`",
	"   |",
	"   = note: expected type `()`",
	"              found struct `String`",
	"",
	"error: could not compile `app` (bin \"app\") due to 2 previous errors",
	"warning: `app` (bin \"app\") generated 1 warning",
	"error: build failed",
}, "\n")

var fixtureGoTest = strings.Join([]string{
	"=== RUN   TestParseConfig",
	"=== RUN   TestParseFlags",
	goTestCases(),
	"--- FAIL: TestParseConfig (0.01s)",
	"    parser_test.go:42: expected 3 entries, got 4",
	"    parser_test.go:43:   input: \"a,b,c,d\"",
	"--- FAIL: TestParseFlags (0.00s)",
	"    parser_test.go:58: unknown flag: -x",
	"panic: runtime error: index out of range [3] with length 2 [recovered]",
	"	goroutine 19 [running]:",
	"github.com/acme/app.(*Parser).next(0x1400010c000)",
	"	/Users/dev/app/parser.go:88 +0x1c4",
	"github.com/acme/app.TestParseConfig(0x1400019a000)",
	"	/Users/dev/app/parser_test.go:42 +0x98",
	"testing.tRunner(0x1400019a000, 0x102f31c40)",
	"	/usr/local/go/src/testing/testing.go:1576 +0x10c",
	"created by testing.(*T).Run",
	"	/usr/local/go/src/testing/testing.go:1629 +0x3b8",
	"FAIL	github.com/acme/app	0.123s",
	"ok  	github.com/acme/app/config	0.045s",
	"ok  	github.com/acme/app/util	(cached)",
	"FAIL",
}, "\n")

var fixtureJest = strings.Join([]string{
	" PASS  tests/sum.test.js",
	" FAIL  tests/api.test.js",
	jestNoise(),
	"  ● api › fetchUser › returns the user",
	"",
	"    expect(received).toEqual(expected)",
	"",
	"    Expected: 200",
	"    Received: 404",
	"",
	"      at Object.<anonymous> (tests/api.test.js:24:31)",
	"      at Promise.then.completed (node_modules/jest-circus/build/utils.js:298:28)",
	"      at new Promise (<anonymous>)",
	"      at callAsyncCircusFn (node_modules/jest-circus/build/run.js:91:15)",
	"",
	"  ● api › fetchUser › rejects on network error",
	"",
	"    TypeError: Cannot read properties of undefined (reading 'json')",
	"        at fetchUser (src/api.js:15:22)",
	"        at Object.<anonymous> (tests/api.test.js:31:9)",
	"",
	"Test Suites: 1 failed, 1 passed, 2 total",
	"Tests:       2 failed, 5 passed, 7 total",
	"Snapshots:   0 total",
	"Time:        3.412 s",
	"Ran all test suites.",
}, "\n")

// goTestCases is the per-case output of a table-driven Go test: 30 lines that
// carry no signal, which is the part worth dropping.
func goTestCases() string {
	var b strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&b, "    case %03d: input=\"a,b,c,d\" expected=3 got=3 elapsed=0.00ms\n", i)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// jestNoise is what jest prints for console.log calls: the bulk of a verbose
// test run and the part no agent needs to read twice.
func jestNoise() string {
	var b strings.Builder
	b.WriteString("  console.log\n")
	for i := 1; i <= 24; i++ {
		fmt.Fprintf(&b, "    loading fixture %03d for the api suite\n", i)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// bigInfoLog is a 400-line service log: a wall of INFO noise around five real
// errors. It is the shape that produces the large ratios.
func bigInfoLog() string {
	var b strings.Builder
	for i := 0; i < 395; i++ {
		fmt.Fprintf(&b, "2024-01-15T09:%02d:%02d.%03dZ INFO  worker-1 handled request id=%04d in %dms\n",
			i/60%60, i%60, i%1000, i, 3+i%17)
	}
	b.WriteString("2024-01-15T09:12:44.001Z ERROR worker-1 upstream timeout after 30000ms id=9001\n")
	b.WriteString("2024-01-15T09:12:44.002Z ERROR worker-1 dropping request id=9001\n")
	b.WriteString("2024-01-15T09:12:45.010Z ERROR worker-1 upstream timeout after 30000ms id=9002\n")
	b.WriteString("2024-01-15T09:12:45.011Z ERROR worker-1 dropping request id=9002\n")
	b.WriteString("2024-01-15T09:12:46.500Z ERROR worker-1 circuit breaker open after 5 failures\n")
	return b.String()
}

// TestCompressLog_Fixtures is the core fidelity test: for every real-world
// shape, the output must be smaller and must still carry every error, the stack
// and the summary.
func TestCompressLog_Fixtures(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		mustKeep []string
	}{
		{
			name: "pytest",
			text: fixturePytest,
			mustKeep: []string{
				"E       AssertionError: assert 404 == 200",
				"E       AssertionError: assert 500 == 204",
				"FAILED tests/test_api.py::test_update_user",
				"FAILED tests/test_api.py::test_delete_user",
				"2 failed, 126 passed in 4.53s",
				"tests/test_api.py:42: AssertionError",
			},
		},
		{
			name: "npm",
			text: fixtureNPM,
			mustKeep: []string{
				"ERROR in ./src/app.js 5:0-42",
				"ERROR in ./src/router.js 12:16-30",
				"Module not found: Error: Can't resolve './components/Missing'",
				"npm ERR! code ELIFECYCLE",
				"npm ERR! A complete log of this run",
			},
		},
		{
			name: "cargo",
			text: fixtureCargo,
			mustKeep: []string{
				"error[E0308]: mismatched types",
				"error: could not compile",
				"error: build failed",
				"warning: unused variable",
				"--> src/main.rs:12:5",
			},
		},
		{
			name: "go test",
			text: fixtureGoTest,
			mustKeep: []string{
				"--- FAIL: TestParseConfig (0.01s)",
				"--- FAIL: TestParseFlags (0.00s)",
				"panic: runtime error: index out of range [3] with length 2",
				"parser_test.go:42: expected 3 entries, got 4",
				"/Users/dev/app/parser.go:88 +0x1c4",
				"FAIL	github.com/acme/app	0.123s",
			},
		},
		{
			name: "jest",
			text: fixtureJest,
			mustKeep: []string{
				"● api › fetchUser › returns the user",
				"● api › fetchUser › rejects on network error",
				"TypeError: Cannot read properties of undefined (reading 'json')",
				"at Object.<anonymous> (tests/api.test.js:24:31)",
				"Test Suites: 1 failed, 1 passed, 2 total",
				"Tests:       2 failed, 5 passed, 7 total",
			},
		},
		{
			name: "big info log",
			text: bigInfoLog(),
			mustKeep: []string{
				"ERROR worker-1 upstream timeout after 30000ms id=9001",
				"ERROR worker-1 upstream timeout after 30000ms id=9002",
				"ERROR worker-1 circuit breaker open after 5 failures",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !LooksLikeLog(tc.text) {
				t.Fatalf("LooksLikeLog = false, want true for %s output", tc.name)
			}
			out := CompressLog(tc.text)
			if len(out) >= len(tc.text) {
				t.Fatalf("compressed size %d, want < %d", len(out), len(tc.text))
			}
			for _, want := range tc.mustKeep {
				if !strings.Contains(out, want) {
					t.Errorf("output lost %q\n--- output ---\n%s", want, out)
				}
			}
			// Every error and failure line survives, however tight the budget.
			if got, want := countErrorLines(out), countErrorLines(tc.text); got != want {
				t.Errorf("error+fail lines kept = %d, want %d\n--- output ---\n%s", got, want, out)
			}
			t.Logf("%s: %d -> %d bytes (%.1fx), %d/%d error+fail lines kept",
				tc.name, len(tc.text), len(out), float64(len(tc.text))/float64(len(out)),
				countErrorLines(out), countErrorLines(tc.text))
		})
	}
}

// countErrorLines counts the lines the compressor must never drop.
func countErrorLines(text string) int {
	n := 0
	for _, l := range splitLines(text) {
		t := strings.TrimSpace(l.text)
		if t == "" {
			continue
		}
		if isErrorLine(t) || isFailLine(t) {
			n++
		}
	}
	return n
}

// TestCompressLog_ErrorsNeverDropped counts error lines before and after: the
// compressor may drop noise, never a failure.
func TestCompressLog_ErrorsNeverDropped(t *testing.T) {
	text := bigInfoLog()
	out := CompressLog(text)
	if got, want := strings.Count(out, "ERROR worker-1"), strings.Count(text, "ERROR worker-1"); got != want {
		t.Errorf("error lines kept = %d, want %d", got, want)
	}
}

// ---------------------------------------------------------------------------
// CompressRequest
// ---------------------------------------------------------------------------

// prefixLogs are five distinct real logs. Distinct on purpose: if every turn
// carried the same text, a caching bug could hide behind a correct result.
var prefixLogs = []string{fixturePytest, fixtureNPM, fixtureCargo, fixtureGoTest, fixtureJest}

// conversationMessages builds the first `turns` user/assistant pairs of a
// scripted Claude Code session. Every user message carries one tool_result whose
// content is a real log, and every second one carries cache_control, which is
// what a real session looks like in the wild.
func conversationMessages(turns int) []any {
	msgs := make([]any, 0, turns*2)
	for i := 0; i < turns; i++ {
		block := map[string]any{
			"type":        "tool_result",
			"tool_use_id": fmt.Sprintf("toolu_%02d", i),
			"content":     prefixLogs[i%len(prefixLogs)],
		}
		if i%2 == 1 {
			block["cache_control"] = map[string]any{"type": "ephemeral"}
		}
		msgs = append(msgs,
			map[string]any{"role": "user", "content": []any{block}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{
					"type":  "tool_use",
					"id":    fmt.Sprintf("toolu_%02d", i),
					"name":  "Bash",
					"input": map[string]any{"command": "go test ./..."},
				},
			}},
		)
	}
	return msgs
}

// conversationRequest wraps a message prefix in a full Anthropic request.
func conversationRequest(msgs []any) map[string]any {
	return map[string]any{
		"model":      "claude-3-5-sonnet-latest",
		"max_tokens": 4096,
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude Code, an assistant for software engineering."},
		},
		"tools": []any{
			map[string]any{
				"name":        "Bash",
				"description": "Run a command",
				"input_schema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"command": map[string]any{"type": "string"}},
				},
			},
		},
		"messages": msgs,
	}
}

// TestCompressRequest_PrefixStability is the test the whole change stands or
// falls on.
//
// Turn N sends messages [m0..mk]; turn N+1 sends [m0..mk, assistant, user].
// Both go through CompressRequest, and the first k+1 messages of turn N+1's
// output must be byte-identical to turn N's output. That is exactly what the
// provider's prompt cache compares: if one byte of the prefix moves, the cache
// misses and the whole conversation is billed as a fresh write.
//
// The scripted conversation carries cache_control blocks in its history, which
// is the case that used to be skipped and is now compressed like everything
// else — and the case where a single-byte drift would be most expensive.
func TestCompressRequest_PrefixStability(t *testing.T) {
	const turns = 5
	full := conversationMessages(turns)

	var previous []json.RawMessage
	var previousRequest map[string]json.RawMessage

	for k := 1; k <= turns; k++ {
		in := mustRawMap(t, conversationRequest(full[:k*2]))
		out, rep := CompressRequest(in)

		if rep.BlocksCompressed != k {
			t.Fatalf("turn %d: BlocksCompressed = %d, want %d (%+v)", k, rep.BlocksCompressed, k, rep)
		}
		msgs := mustMessages(t, out)
		if len(msgs) != k*2 {
			t.Fatalf("turn %d: messages length %d, want %d", k, len(msgs), k*2)
		}
		// The prefix of this turn must be the prefix of the previous turn.
		for i := range previous {
			if !bytes.Equal(previous[i], msgs[i]) {
				t.Fatalf("turn %d: messages[%d] drifted from the previous turn\nprevious: %s\ncurrent:  %s",
					k, i, previous[i], msgs[i])
			}
		}
		// system and tools are the head of the cache prefix: never touched.
		if previousRequest != nil {
			for _, key := range []string{"system", "tools"} {
				if !bytes.Equal(previousRequest[key], out[key]) {
					t.Fatalf("turn %d: %s changed between turns", k, key)
				}
			}
		}
		for _, key := range []string{"system", "tools"} {
			if !bytes.Equal(in[key], out[key]) {
				t.Fatalf("turn %d: %s changed by compression", k, key)
			}
		}

		previous = msgs
		previousRequest = out
	}

	// The last turn's history must really be compressed: a marker in the
	// history block proves the test is not passing because nothing happened.
	last := mustObject(t, previous[len(previous)-2])
	blocks := mustArray(t, last["content"])
	var text string
	if err := json.Unmarshal(mustObject(t, blocks[0])["content"], &text); err != nil {
		t.Fatalf("decode last tool_result: %v", err)
	}
	if !strings.Contains(text, "lines omitted") {
		t.Fatalf("the last tool_result was not compressed, so prefix stability proves nothing:\n%s", text)
	}
	lastLog := prefixLogs[(turns-1)%len(prefixLogs)]
	if got, want := countErrorLines(text), countErrorLines(lastLog); got != want {
		t.Errorf("error+fail lines kept = %d, want %d\n%s", got, want, text)
	}
}

// TestCompressRequest_PrefixStabilityAcrossTurnsWithColdCache repeats the
// stability walk with the memo cleared between turns. Determinism must come from
// the transform, never from a warm cache.
func TestCompressRequest_PrefixStabilityAcrossTurnsWithColdCache(t *testing.T) {
	const turns = 4
	full := conversationMessages(turns)

	var cold, warm [][]json.RawMessage
	for k := 1; k <= turns; k++ {
		in := mustRawMap(t, conversationRequest(full[:k*2]))

		resetTextCache()
		coldOut, coldRep := CompressRequest(in)
		cold = append(cold, mustMessages(t, coldOut))

		warmOut, warmRep := CompressRequest(in) // warm
		warm = append(warm, mustMessages(t, warmOut))

		if coldRep != warmRep {
			t.Fatalf("turn %d: cold cache report %+v, warm %+v", k, coldRep, warmRep)
		}
		for i := range cold[k-1] {
			if !bytes.Equal(cold[k-1][i], warm[k-1][i]) {
				t.Fatalf("turn %d: messages[%d] differs between a cold and a warm cache\ncold: %s\nwarm: %s",
					k, i, cold[k-1][i], warm[k-1][i])
			}
		}
		if k == 1 {
			continue
		}
		// The history of this turn must be byte-identical to the whole of the
		// previous turn, which is the property the provider's cache checks.
		for i := range cold[k-2] {
			if !bytes.Equal(cold[k-2][i], cold[k-1][i]) {
				t.Fatalf("turn %d: messages[%d] drifted with a cold cache\nprevious: %s\ncurrent:  %s",
					k, i, cold[k-2][i], cold[k-1][i])
			}
		}
	}
}

// TestCompressRequest_CacheControlIsCompressed inverts the old rule on purpose.
//
// A cache_control block used to be skipped because rewriting it looked like a
// cache write. It is now compressed like any other block: the compressed form is
// stable, so the write happens once and every later turn reads it — whereas
// leaving it out of the transform is what broke the prefix in the first place.
func TestCompressRequest_CacheControlIsCompressed(t *testing.T) {
	in := mustRawMap(t, map[string]any{
		"model": "claude-3-5-sonnet-latest",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":          "tool_result",
						"tool_use_id":   "toolu_1",
						"content":       fixturePytest,
						"cache_control": map[string]any{"type": "ephemeral"},
					},
				},
			},
		},
	})
	out, rep := CompressRequest(in)

	if rep.BlocksCompressed != 1 {
		t.Fatalf("BlocksCompressed = %d, want 1 (%+v)", rep.BlocksCompressed, rep)
	}
	if rep.Skipped != "" {
		t.Fatalf("Skipped = %q, want empty", rep.Skipped)
	}

	before := mustObject(t, mustArray(t, mustObject(t, mustMessages(t, in)[0])["content"])[0])
	after := mustObject(t, mustArray(t, mustObject(t, mustMessages(t, out)[0])["content"])[0])

	if !bytes.Equal(before["cache_control"], after["cache_control"]) {
		t.Errorf("cache_control changed: %s -> %s", before["cache_control"], after["cache_control"])
	}
	if !bytes.Equal(before["tool_use_id"], after["tool_use_id"]) {
		t.Errorf("tool_use_id changed: %s -> %s", before["tool_use_id"], after["tool_use_id"])
	}

	var beforeText, afterText string
	if err := json.Unmarshal(before["content"], &beforeText); err != nil {
		t.Fatalf("decode original content: %v", err)
	}
	if err := json.Unmarshal(after["content"], &afterText); err != nil {
		t.Fatalf("decode compressed content: %v", err)
	}
	if len(afterText) >= len(beforeText) {
		t.Fatalf("tool_result text %d -> %d, want strictly smaller", len(beforeText), len(afterText))
	}
	if !strings.Contains(afterText, "lines omitted") {
		t.Errorf("no omission marker, so nothing was dropped:\n%s", afterText)
	}
	for _, want := range []string{
		"FAILED tests/test_api.py::test_update_user",
		"E       AssertionError: assert 404 == 200",
		"2 failed, 126 passed in 4.53s",
	} {
		if !strings.Contains(afterText, want) {
			t.Errorf("compressed tool_result lost %q", want)
		}
	}
}

// TestCompressRequest_EveryMessageIsCompressed pins the scope: history blocks,
// including the ones in assistant-authored turns and cache_control blocks, are
// all compressed by the same rule.
func TestCompressRequest_EveryMessageIsCompressed(t *testing.T) {
	in := mustRawMap(t, map[string]any{
		"model": "claude-3-5-sonnet-latest",
		"messages": []any{
			map[string]any{"role": "user", "content": "run the tests"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "running them now"},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": fixturePytest,
					"cache_control": map[string]any{"type": "ephemeral"}},
			}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "t2", "name": "Bash", "input": map[string]any{"command": "npm run build"}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t2", "content": fixtureNPM},
			}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "t3", "name": "Bash", "input": map[string]any{"command": "cargo build"}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t3", "content": []any{
					map[string]any{"type": "text", "text": fixtureCargo},
				}},
			}},
		},
	})
	out, rep := CompressRequest(in)

	if rep.BlocksCompressed != 3 {
		t.Fatalf("BlocksCompressed = %d, want 3 (%+v)", rep.BlocksCompressed, rep)
	}
	if rep.OriginalBytes <= rep.CompressedBytes {
		t.Fatalf("bytes %d -> %d, want strictly smaller", rep.OriginalBytes, rep.CompressedBytes)
	}
	if rep.Skipped != "" {
		t.Errorf("Skipped = %q, want empty", rep.Skipped)
	}

	// The plain-string user message and the text-only assistant message keep
	// their original bytes.
	inMsgs := mustMessages(t, in)
	outMsgs := mustMessages(t, out)
	for _, i := range []int{0, 1} {
		if !bytes.Equal(inMsgs[i], outMsgs[i]) {
			t.Errorf("messages[%d] changed although it carries no tool_result:\nbefore: %s\nafter:  %s",
				i, inMsgs[i], outMsgs[i])
		}
	}
	for _, i := range []int{2, 4, 6} {
		if bytes.Equal(inMsgs[i], outMsgs[i]) {
			t.Errorf("messages[%d] was not compressed", i)
		}
	}
}

// TestCompressRequest_RoleIsIrrelevant: the transform is not a live-zone
// transform any more, so a tool_result in an assistant-role message is still
// eligible. Roles do not select blocks; block type does.
func TestCompressRequest_RoleIsIrrelevant(t *testing.T) {
	in := mustRawMap(t, map[string]any{
		"messages": []any{
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": fixtureGoTest},
			}},
		},
	})
	_, rep := CompressRequest(in)
	if rep.BlocksCompressed != 1 {
		t.Fatalf("BlocksCompressed = %d, want 1 (%+v)", rep.BlocksCompressed, rep)
	}
}

// TestCompressRequest_SameTextSameBytes: the same log in two different messages
// must compress to the same bytes, which is what makes a history rewrite safe.
func TestCompressRequest_SameTextSameBytes(t *testing.T) {
	in := mustRawMap(t, map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": fixtureJest},
			}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "again"},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t2", "content": fixtureJest},
			}},
		},
	})
	out, rep := CompressRequest(in)
	if rep.BlocksCompressed != 2 {
		t.Fatalf("BlocksCompressed = %d, want 2 (%+v)", rep.BlocksCompressed, rep)
	}
	msgs := mustMessages(t, out)
	first := mustObject(t, mustArray(t, mustObject(t, msgs[0])["content"])[0])
	second := mustObject(t, mustArray(t, mustObject(t, msgs[2])["content"])[0])
	if !bytes.Equal(first["content"], second["content"]) {
		t.Errorf("the same input text produced two different outputs:\n%s\n%s",
			first["content"], second["content"])
	}
}

// TestCompressRequest_SkipReasons covers the paths that leave a request alone.
func TestCompressRequest_SkipReasons(t *testing.T) {
	small := "ERROR: boom\n" + strings.Repeat("x", 200) + "\n"

	cases := []struct {
		name string
		req  map[string]any
		want string
	}{
		{
			name: "no messages",
			req:  map[string]any{"model": "m"},
			want: skipNoMessages,
		},
		{
			name: "messages is not an array",
			req:  map[string]any{"messages": "nope"},
			want: skipBadMessages,
		},
		{
			name: "text below the size threshold",
			req: map[string]any{
				"messages": []any{map[string]any{"role": "user",
					"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t", "content": small}}}},
			},
			want: skipTooSmall,
		},
		{
			name: "not a log",
			req: map[string]any{
				"messages": []any{map[string]any{"role": "user",
					"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t",
						"content": strings.Repeat("the quick brown fox jumps over the lazy dog. ", 40)}}}},
			},
			want: skipNotLog,
		},
		{
			name: "plain string content is not eligible",
			req: map[string]any{
				"messages": []any{map[string]any{"role": "user", "content": fixturePytest}},
			},
			want: skipPlainContent,
		},
		{
			name: "no content field",
			req: map[string]any{
				"messages": []any{map[string]any{"role": "user"}},
			},
			want: skipNoContent,
		},
		{
			// Only tool_result blocks are eligible: a text block is the
			// caller's own words and is left alone, wherever it sits.
			name: "text block is not eligible",
			req: map[string]any{
				"messages": []any{map[string]any{"role": "user",
					"content": []any{map[string]any{"type": "text", "text": fixturePytest}}}},
			},
			want: skipNoToolResult,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := mustRawMap(t, tc.req)
			out, rep := CompressRequest(in)
			if rep.Skipped != tc.want {
				t.Fatalf("Skipped = %q, want %q (%+v)", rep.Skipped, tc.want, rep)
			}
			if rep.BlocksCompressed != 0 {
				t.Errorf("BlocksCompressed = %d, want 0", rep.BlocksCompressed)
			}
			if !bytes.Equal(in["messages"], out["messages"]) {
				t.Errorf("messages changed although nothing was compressed")
			}
		})
	}
}

// TestCompressRequest_NoGainFallsBackToOriginal pins the hard gate: when every
// line is an error there is nothing to drop, and a compression that grew the
// payload must never ship.
func TestCompressRequest_NoGainFallsBackToOriginal(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&b, "2024-01-15T09:12:%02d.000Z ERROR handler: request %04d failed with a very long message body\n", i, i)
	}
	allErrors := b.String()
	if !LooksLikeLog(allErrors) {
		t.Fatalf("LooksLikeLog = false for an all-error log")
	}

	in := mustRawMap(t, map[string]any{
		"messages": []any{map[string]any{"role": "user",
			"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t", "content": allErrors}}}},
	})
	out, rep := CompressRequest(in)
	if rep.Skipped != skipNoGain {
		t.Fatalf("Skipped = %q, want %q (%+v)", rep.Skipped, skipNoGain, rep)
	}
	if !bytes.Equal(in["messages"], out["messages"]) {
		t.Errorf("messages changed although compression would not have helped")
	}
}

// TestCompressRequest_Deterministic is the cache-prefix guarantee at the API
// level: the same input must produce the same bytes every time, or the prefix
// changes on every turn and the cache never hits.
func TestCompressRequest_Deterministic(t *testing.T) {
	in := mustRawMap(t, conversationRequest(conversationMessages(3)))

	var want []byte
	var wantRep Report
	for i := 0; i < 10; i++ {
		out, rep := CompressRequest(in)
		if i == 0 {
			want = append([]byte(nil), out["messages"]...)
			wantRep = rep
			continue
		}
		if !bytes.Equal(want, out["messages"]) {
			t.Fatalf("run %d produced different bytes:\nfirst: %s\nrun %d: %s", i, want, i, out["messages"])
		}
		if rep != wantRep {
			t.Fatalf("run %d produced a different report: %+v vs %+v", i, rep, wantRep)
		}
	}
	if len(want) == 0 {
		t.Fatal("nothing was produced")
	}
}

// TestCompressRequest_CacheDoesNotChangeOutput proves the LRU is an accelerator
// and nothing more: the bytes and the report are identical with a cold cache, a
// warm cache, and a cache that was cleared in between.
func TestCompressRequest_CacheDoesNotChangeOutput(t *testing.T) {
	in := mustRawMap(t, conversationRequest(conversationMessages(5)))

	resetTextCache()
	cold, coldRep := CompressRequest(in)
	warm, warmRep := CompressRequest(in)
	resetTextCache()
	cleared, clearedRep := CompressRequest(in)

	if !bytes.Equal(cold["messages"], warm["messages"]) {
		t.Fatalf("warm cache changed the output:\ncold: %s\nwarm: %s", cold["messages"], warm["messages"])
	}
	if !bytes.Equal(cold["messages"], cleared["messages"]) {
		t.Fatalf("clearing the cache changed the output:\nbefore: %s\nafter:  %s", cold["messages"], cleared["messages"])
	}
	if coldRep != warmRep || coldRep != clearedRep {
		t.Fatalf("reports differ: cold %+v, warm %+v, cleared %+v", coldRep, warmRep, clearedRep)
	}
}

// TestCompressRequest_ContentArrayOfParts covers the tool_result form whose
// content is an array of typed parts.
func TestCompressRequest_ContentArrayOfParts(t *testing.T) {
	in := mustRawMap(t, map[string]any{
		"messages": []any{map[string]any{"role": "user",
			"content": []any{map[string]any{
				"type":        "tool_result",
				"tool_use_id": "t",
				"content": []any{
					map[string]any{"type": "text", "text": fixtureJest},
				},
			}}}},
	})
	out, rep := CompressRequest(in)
	if rep.BlocksCompressed != 1 {
		t.Fatalf("BlocksCompressed = %d, want 1 (%+v)", rep.BlocksCompressed, rep)
	}

	msgs := mustMessages(t, out)
	obj := mustObject(t, msgs[0])
	blocks := mustArray(t, obj["content"])
	parts := mustArray(t, mustObject(t, blocks[0])["content"])
	var text string
	if err := json.Unmarshal(mustObject(t, parts[0])["text"], &text); err != nil {
		t.Fatalf("decode part text: %v", err)
	}
	if len(text) >= len(fixtureJest) {
		t.Fatalf("part text %d, want < %d", len(text), len(fixtureJest))
	}
	if !strings.Contains(text, "Tests:       2 failed, 5 passed, 7 total") {
		t.Errorf("lost the jest summary:\n%s", text)
	}
}

// TestCompressRequest_PlainStringContentIsNeverCompressed pins the trust
// boundary: a message whose content is a plain string is what the caller typed
// or pasted, not machine-generated output. It is left alone even when it is a
// log, because the caller would not know a line went missing.
func TestCompressRequest_PlainStringContentIsNeverCompressed(t *testing.T) {
	in := mustRawMap(t, map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": fixtureCargo},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "t1", "name": "Bash", "input": map[string]any{"command": "cargo build"}},
			}},
			// A compressible tool_result in a later message: the plain-string
			// message must be skipped without stopping the walk.
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": fixtureGoTest},
			}},
		},
	})
	if !LooksLikeLog(fixtureCargo) {
		t.Fatalf("fixtureCargo is not detected as a log, so the test proves nothing")
	}
	out, rep := CompressRequest(in)
	if rep.BlocksCompressed != 1 {
		t.Fatalf("BlocksCompressed = %d, want 1 (%+v)", rep.BlocksCompressed, rep)
	}
	if rep.Skipped != "" {
		t.Errorf("Skipped = %q, want empty", rep.Skipped)
	}
	inMsgs := mustMessages(t, in)
	outMsgs := mustMessages(t, out)
	if !bytes.Equal(inMsgs[0], outMsgs[0]) {
		t.Errorf("a plain-string content was modified:\nbefore: %s\nafter:  %s", inMsgs[0], outMsgs[0])
	}
	if bytes.Equal(inMsgs[2], outMsgs[2]) {
		t.Errorf("the tool_result of a later message was not compressed")
	}
}

// TestCompressRequest_MalformedInputIsSafe throws shapes that a fuzzer would
// find and asserts nothing panics and nothing changes.
func TestCompressRequest_MalformedInputIsSafe(t *testing.T) {
	cases := map[string]string{
		"messages not an array":  `{"messages":"nope"}`,
		"message not an object":  `{"messages":[1,2,3]}`,
		"content not stringable": `{"messages":[{"role":"user","content":{"a":1}}]}`,
		"content array of ints":  `{"messages":[{"role":"user","content":[1,2,3]}]}`,
		"empty object":           `{}`,
		"null messages":          `{"messages":null}`,
		"tool_result without id": `{"messages":[{"role":"user","content":[{"type":"tool_result","content":"x"}]}]}`,
		"tool_result bad body":   `{"messages":[{"role":"user","content":[{"type":"tool_result","content":{"a":1}}]}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var in map[string]json.RawMessage
			if err := json.Unmarshal([]byte(body), &in); err != nil {
				t.Fatalf("unmarshal fixture: %v", err)
			}
			out, rep := CompressRequest(in)
			if rep.BlocksCompressed != 0 {
				t.Errorf("BlocksCompressed = %d, want 0", rep.BlocksCompressed)
			}
			for k := range in {
				if !bytes.Equal(in[k], out[k]) {
					t.Errorf("key %q changed", k)
				}
			}
		})
	}
}

// TestCompressRequest_NilMapIsSafe: a nil request is not a crash.
func TestCompressRequest_NilMapIsSafe(t *testing.T) {
	out, rep := CompressRequest(nil)
	if out != nil {
		t.Errorf("out = %v, want nil", out)
	}
	if rep.Skipped != skipNoMessages {
		t.Errorf("Skipped = %q, want %q", rep.Skipped, skipNoMessages)
	}
}

func mustRawMap(t *testing.T, v any) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	return m
}

func mustMessages(t *testing.T, m map[string]json.RawMessage) []json.RawMessage {
	t.Helper()
	var msgs []json.RawMessage
	if err := json.Unmarshal(m["messages"], &msgs); err != nil {
		t.Fatalf("unmarshal messages: %v", err)
	}
	return msgs
}

func mustObject(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal object %s: %v", raw, err)
	}
	return obj
}

func mustArray(t *testing.T, raw json.RawMessage) []json.RawMessage {
	t.Helper()
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		t.Fatalf("unmarshal array %s: %v", raw, err)
	}
	return arr
}

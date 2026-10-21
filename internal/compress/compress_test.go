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

// TestCompressLiveZone_OnlyLastUserMessageIsTouched builds a full Anthropic
// request — system, tools, a previous turn whose tool_result is also a log — and
// asserts that only the last user message's tool_result changed.
func TestCompressLiveZone_OnlyLastUserMessageIsTouched(t *testing.T) {
	historyLog := fixtureGoTest
	req := map[string]any{
		"model": "claude-3-5-sonnet-latest",
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude Code."},
		},
		"tools": []any{
			map[string]any{
				"name":        "Bash",
				"description": "Run a command",
				"input_schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{"type": "string"},
					},
				},
			},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": "run the tests"},
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Bash",
						"input": map[string]any{"command": "go test ./... -run TestParse"}},
				},
			},
			// A previous turn whose tool result is a log too. It sits in the
			// cache prefix, so it must survive untouched.
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": historyLog},
				},
			},
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "text", "text": "now run pytest"},
				},
			},
			// The live zone.
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "here is the pytest output"},
					map[string]any{"type": "tool_result", "tool_use_id": "toolu_2", "content": fixturePytest},
				},
			},
		},
		"max_tokens": 1024,
	}

	in := mustRawMap(t, req)
	out, rep := CompressLiveZone(in)

	if rep.BlocksCompressed != 1 {
		t.Fatalf("BlocksCompressed = %d, want 1 (%+v)", rep.BlocksCompressed, rep)
	}
	if rep.Skipped != "" {
		t.Fatalf("Skipped = %q, want empty", rep.Skipped)
	}
	if rep.OriginalBytes <= rep.CompressedBytes {
		t.Fatalf("bytes %d -> %d, want strictly smaller", rep.OriginalBytes, rep.CompressedBytes)
	}

	// system and tools: identical bytes.
	for _, key := range []string{"system", "tools"} {
		if !bytes.Equal(in[key], out[key]) {
			t.Errorf("%s changed:\nbefore: %s\nafter:  %s", key, in[key], out[key])
		}
	}

	inMsgs := mustMessages(t, in)
	outMsgs := mustMessages(t, out)
	if len(inMsgs) != len(outMsgs) {
		t.Fatalf("messages length %d, want %d", len(outMsgs), len(inMsgs))
	}
	// Everything but the last message: identical bytes.
	for i := 0; i < len(inMsgs)-1; i++ {
		if !bytes.Equal(inMsgs[i], outMsgs[i]) {
			t.Errorf("messages[%d] changed:\nbefore: %s\nafter:  %s", i, inMsgs[i], outMsgs[i])
		}
	}

	// The last message: every field except content is identical.
	inLast := mustObject(t, inMsgs[len(inMsgs)-1])
	outLast := mustObject(t, outMsgs[len(outMsgs)-1])
	for k, v := range inLast {
		if k == "content" {
			continue
		}
		if !bytes.Equal(v, outLast[k]) {
			t.Errorf("last message field %q changed: %s -> %s", k, v, outLast[k])
		}
	}

	inBlocks := mustArray(t, inLast["content"])
	outBlocks := mustArray(t, outLast["content"])
	if len(inBlocks) != len(outBlocks) {
		t.Fatalf("content blocks %d, want %d", len(outBlocks), len(inBlocks))
	}
	// The text block is untouched: only tool_result text is eligible.
	if !bytes.Equal(inBlocks[0], outBlocks[0]) {
		t.Errorf("text block changed:\nbefore: %s\nafter:  %s", inBlocks[0], outBlocks[0])
	}
	inBody := mustObject(t, inBlocks[1])
	outBody := mustObject(t, outBlocks[1])
	for k, v := range inBody {
		if k == "content" {
			continue
		}
		if !bytes.Equal(v, outBody[k]) {
			t.Errorf("tool_result field %q changed: %s -> %s", k, v, outBody[k])
		}
	}

	var before, after string
	if err := json.Unmarshal(inBody["content"], &before); err != nil {
		t.Fatalf("decode original content: %v", err)
	}
	if err := json.Unmarshal(outBody["content"], &after); err != nil {
		t.Fatalf("decode compressed content: %v", err)
	}
	if len(after) >= len(before) {
		t.Fatalf("tool_result text %d -> %d, want strictly smaller", len(before), len(after))
	}
	for _, want := range []string{"FAILED tests/test_api.py::test_update_user", "2 failed, 126 passed"} {
		if !strings.Contains(after, want) {
			t.Errorf("compressed tool_result lost %q", want)
		}
	}
}

// TestCompressLiveZone_CacheControlIsUntouched pins the rule that pays for the
// whole feature: rewriting a cached block is billed as a cache write, so a block
// carrying cache_control is not touched at all.
func TestCompressLiveZone_CacheControlIsUntouched(t *testing.T) {
	req := map[string]any{
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
	}
	in := mustRawMap(t, req)
	out, rep := CompressLiveZone(in)

	if !bytes.Equal(in["messages"], out["messages"]) {
		t.Errorf("a cache_control block was modified")
	}
	if rep.BlocksCompressed != 0 {
		t.Errorf("BlocksCompressed = %d, want 0", rep.BlocksCompressed)
	}
	if rep.Skipped != skipCacheControl {
		t.Errorf("Skipped = %q, want %q", rep.Skipped, skipCacheControl)
	}
}

// TestCompressLiveZone_SkipReasons covers the paths that leave a request alone.
func TestCompressLiveZone_SkipReasons(t *testing.T) {
	logText := fixturePytest
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
			name: "no user message",
			req: map[string]any{
				"messages": []any{map[string]any{"role": "assistant", "content": "hi"}},
			},
			want: skipNoUserMsg,
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
				"messages": []any{map[string]any{"role": "user", "content": logText}},
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
			// Only tool_result blocks are eligible: a text block in the live
			// zone is the caller's own words and is left alone.
			name: "text block is not eligible",
			req: map[string]any{
				"messages": []any{map[string]any{"role": "user",
					"content": []any{map[string]any{"type": "text", "text": logText}}}},
			},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := mustRawMap(t, tc.req)
			out, rep := CompressLiveZone(in)
			if rep.Skipped != tc.want {
				t.Fatalf("Skipped = %q, want %q (%+v)", rep.Skipped, tc.want, rep)
			}
			if !bytes.Equal(in["messages"], out["messages"]) {
				t.Errorf("messages changed although nothing was compressed")
			}
		})
	}
}

// TestCompressLiveZone_NoGainFallsBackToOriginal pins the hard gate: when every
// line is an error there is nothing to drop, and a compression that grew the
// payload must never ship.
func TestCompressLiveZone_NoGainFallsBackToOriginal(t *testing.T) {
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
	out, rep := CompressLiveZone(in)
	if rep.Skipped != skipNoGain {
		t.Fatalf("Skipped = %q, want %q (%+v)", rep.Skipped, skipNoGain, rep)
	}
	if !bytes.Equal(in["messages"], out["messages"]) {
		t.Errorf("messages changed although compression would not have helped")
	}
}

// TestCompressLiveZone_Deterministic is the cache-prefix guarantee: the same
// input must produce the same bytes every time, or the prefix changes on every
// turn and the cache never hits.
func TestCompressLiveZone_Deterministic(t *testing.T) {
	in := mustRawMap(t, map[string]any{
		"messages": []any{map[string]any{"role": "user",
			"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t", "content": fixturePytest}}}},
	})

	var want []byte
	for i := 0; i < 10; i++ {
		out, _ := CompressLiveZone(in)
		if i == 0 {
			want = append([]byte(nil), out["messages"]...)
			continue
		}
		if !bytes.Equal(want, out["messages"]) {
			t.Fatalf("run %d produced different bytes:\nfirst: %s\nrun %d: %s", i, want, i, out["messages"])
		}
	}
	if len(want) == 0 {
		t.Fatal("nothing was produced")
	}
}

// TestCompressLiveZone_ContentArrayOfParts covers the tool_result form whose
// content is an array of typed parts.
func TestCompressLiveZone_ContentArrayOfParts(t *testing.T) {
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
	out, rep := CompressLiveZone(in)
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

// TestCompressLiveZone_PlainStringContentIsNeverCompressed pins the trust
// boundary: the last user message's plain-string content is what the caller
// typed or pasted, not machine-generated output. It is left alone even when it
// is a log, because the caller would not know a line went missing.
func TestCompressLiveZone_PlainStringContentIsNeverCompressed(t *testing.T) {
	in := mustRawMap(t, map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": fixtureCargo},
		},
	})
	if !LooksLikeLog(fixtureCargo) {
		t.Fatalf("fixtureCargo is not detected as a log, so the test proves nothing")
	}
	out, rep := CompressLiveZone(in)
	if rep.BlocksCompressed != 0 {
		t.Fatalf("BlocksCompressed = %d, want 0 (%+v)", rep.BlocksCompressed, rep)
	}
	if rep.Skipped != skipPlainContent {
		t.Fatalf("Skipped = %q, want %q", rep.Skipped, skipPlainContent)
	}
	if !bytes.Equal(in["messages"], out["messages"]) {
		t.Errorf("a plain-string content was modified:\nbefore: %s\nafter:  %s", in["messages"], out["messages"])
	}
}

// TestCompressLiveZone_MalformedInputIsSafe throws shapes that a fuzzer would
// find and asserts nothing panics and nothing changes.
func TestCompressLiveZone_MalformedInputIsSafe(t *testing.T) {
	cases := map[string]string{
		"messages not an array":  `{"messages":"nope"}`,
		"message not an object":  `{"messages":[1,2,3]}`,
		"content not stringable": `{"messages":[{"role":"user","content":{"a":1}}]}`,
		"content array of ints":  `{"messages":[{"role":"user","content":[1,2,3]}]}`,
		"empty object":           `{}`,
		"null messages":          `{"messages":null}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var in map[string]json.RawMessage
			if err := json.Unmarshal([]byte(body), &in); err != nil {
				t.Fatalf("unmarshal fixture: %v", err)
			}
			out, rep := CompressLiveZone(in)
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

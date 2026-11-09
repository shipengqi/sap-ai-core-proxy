package agentconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/huh/v2"
)

func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv("ProgramData", filepath.Join(home, "programdata"))
	return home
}

// lineReader yields one supplied line per Read. huh's accessible prompts build
// a fresh bufio.Scanner on every question; returning a single line per Read
// stops that Scanner from swallowing the lines meant for later prompts/forms.
type lineReader struct {
	lines []string
	rest  string
}

func (r *lineReader) Read(p []byte) (int, error) {
	if r.rest == "" {
		if len(r.lines) == 0 {
			return 0, io.EOF
		}
		r.rest = r.lines[0] + "\n"
		r.lines = r.lines[1:]
	}
	n := copy(p, r.rest)
	r.rest = r.rest[n:]
	return n, nil
}

// input builds a non-TTY reader that drives accessible huh forms line by line.
func input(lines ...string) io.Reader { return &lineReader{lines: lines} }

func TestRunDryRunDoesNotWrite(t *testing.T) {
	home := isolatedHome(t)
	var out bytes.Buffer
	opts := Options{Port: 3001, Agents: IDs(), Yes: true, DryRun: true}
	if err := Run(strings.NewReader(""), &out, opts); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Planned changes", "Dry run", "settings.json", "config.toml", "opencode.jsonc"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	for _, path := range []string{
		filepath.Join(home, "claude", "settings.json"),
		filepath.Join(home, "codex", "config.toml"),
		filepath.Join(home, "codex", "auth.json"),
		filepath.Join(home, "xdg", "opencode", "opencode.jsonc"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("dry run created %s", path)
		}
	}
}

func TestRunAppliesAllAndIsIdempotent(t *testing.T) {
	home := isolatedHome(t)
	opts := Options{Port: 6655, Agents: IDs(), Yes: true}
	var out bytes.Buffer
	if err := Run(strings.NewReader(""), &out, opts); err != nil {
		t.Fatal(err)
	}
	paths := []string{
		filepath.Join(home, "claude", "settings.json"),
		filepath.Join(home, "codex", "config.toml"),
		filepath.Join(home, "codex", "auth.json"),
		filepath.Join(home, "xdg", "opencode", "opencode.jsonc"),
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("missing %s: %v", path, err)
		}
	}
	settings := read(t, paths[0])
	if !strings.Contains(settings, "http://localhost:6655/anthropic") {
		t.Errorf("port not applied to Claude Code: %s", settings)
	}
	codexConfig := read(t, paths[1])
	if !strings.Contains(codexConfig, "http://localhost:6655/openai/v1") {
		t.Errorf("port not applied to Codex: %s", codexConfig)
	}
	opencode := read(t, paths[3])
	if !strings.Contains(opencode, "aicore-anthropic") || strings.Contains(opencode, "aicore-google") {
		t.Errorf("unexpected opencode providers: %s", opencode)
	}

	// Second run must be a no-op: identical config means no rewrite and no
	// backup (backups are only made for files that actually change).
	var second bytes.Buffer
	if err := Run(strings.NewReader(""), &second, opts); err != nil {
		t.Fatal(err)
	}
	if strings.Count(second.String(), "unchanged") != len(paths) {
		t.Errorf("second run should report %d unchanged files:\n%s", len(paths), second.String())
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		previous := map[string]string{
			paths[0]: settings, paths[1]: codexConfig, paths[3]: opencode,
		}
		if want, ok := previous[path]; ok && string(data) != want {
			t.Errorf("second run changed %s", path)
		}
	}
}

// Non-TTY input must no longer hard-error; it degrades to huh's accessible
// line prompts and still completes (cancelling when there is no input).
func TestRunNonTerminalDegradesToAccessible(t *testing.T) {
	t.Run("empty agent selection cancels", func(t *testing.T) {
		isolatedHome(t)
		var out bytes.Buffer
		err := Run(strings.NewReader(""), &out, Options{Port: 3001, Yes: true})
		if err != nil {
			t.Fatalf("non-TTY must not error: %v", err)
		}
		if !strings.Contains(out.String(), "Cancelled.") {
			t.Errorf("expected Cancelled.:\n%s", out.String())
		}
		if !strings.Contains(out.String(), "Select up to") {
			t.Errorf("expected accessible agent form prompt:\n%s", out.String())
		}
	})
	t.Run("agents provided still reaches confirm", func(t *testing.T) {
		isolatedHome(t)
		var out bytes.Buffer
		err := Run(strings.NewReader(""), &out, Options{Port: 3001, Agents: IDs()})
		if err != nil {
			t.Fatalf("non-TTY must not error: %v", err)
		}
		if !strings.Contains(out.String(), "Cancelled.") {
			t.Errorf("expected Cancelled.:\n%s", out.String())
		}
		if !strings.Contains(out.String(), "Select up to") {
			t.Errorf("expected accessible model form prompt:\n%s", out.String())
		}
	})
}

func TestRunStatus(t *testing.T) {
	isolatedHome(t)
	var out bytes.Buffer
	if err := Run(strings.NewReader(""), &out, Options{Port: 3001, Status: true}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Proxy URL", "Claude Code", "Codex", "OpenCode"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status output missing %q:\n%s", want, out.String())
		}
	}
}

// The non-interactive path (--yes plus --agents) must build no form at all,
// even when stray stdin data is available.
func TestYesSkipsAllForms(t *testing.T) {
	isolatedHome(t)
	var out bytes.Buffer
	err := Run(input("1", "0", "0", "y"), &out, Options{Port: 3001, Agents: IDs(), Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Configuration written.") {
		t.Errorf("--yes path should apply config:\n%s", out.String())
	}
	for _, marker := range []string{"Select up to", "Enter a number between", "Write these files?"} {
		if strings.Contains(out.String(), marker) {
			t.Errorf("--yes path must not build a form (found %q):\n%s", marker, out.String())
		}
	}
}

// Buffer-injected accessible input must drive a MultiSelect (agent choice) and
// a Confirm to completion, and the selection must actually take effect.
func TestAccessibleMultiSelectAndConfirm(t *testing.T) {
	home := isolatedHome(t)
	var out bytes.Buffer
	// Agent form: toggle option 1 (claude-code), then 0 to confirm.
	// Model form: 0 confirms an empty selection -> keep curated defaults.
	// Confirm form: "y" applies the changes.
	err := Run(input("1", "0", "0", "y"), &out, Options{Port: 6655})
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"Select up to", "Enter a number between", "Write these files?", "Configuration written.",
	} {
		if !strings.Contains(out.String(), marker) {
			t.Errorf("output missing %q:\n%s", marker, out.String())
		}
	}
	settings := filepath.Join(home, "claude", "settings.json")
	if _, err := os.Stat(settings); err != nil {
		t.Fatalf("selected agent config not written: %v", err)
	}
	if !strings.Contains(read(t, settings), "http://localhost:6655/anthropic") {
		t.Errorf("selected agent config missing base URL:\n%s", read(t, settings))
	}
	// Only claude-code was selected; the other agents' files must not exist.
	for _, p := range []string{
		filepath.Join(home, "codex", "config.toml"),
		filepath.Join(home, "xdg", "opencode", "opencode.jsonc"),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("unselected agent file was written: %s", p)
		}
	}
}

// Cancellation must map to the exact same "Cancelled." message and a nil error,
// whether the user aborts an empty agent selection or declines the confirm.
func TestRunCancelMapping(t *testing.T) {
	t.Run("empty agent selection", func(t *testing.T) {
		isolatedHome(t)
		var out bytes.Buffer
		if err := Run(strings.NewReader(""), &out, Options{Port: 3001}); err != nil {
			t.Fatalf("cancel must not error: %v", err)
		}
		if !strings.Contains(out.String(), "Cancelled.") {
			t.Errorf("expected Cancelled.:\n%s", out.String())
		}
		if strings.Contains(out.String(), "Configuration written.") {
			t.Error("cancel must not write config")
		}
	})
	t.Run("declined confirmation", func(t *testing.T) {
		isolatedHome(t)
		var out bytes.Buffer
		// Model form: 0 keeps defaults; confirm form: "n" declines.
		if err := Run(input("0", "n"), &out, Options{Port: 3001, Agents: IDs()}); err != nil {
			t.Fatalf("cancel must not error: %v", err)
		}
		if !strings.Contains(out.String(), "Cancelled.") {
			t.Errorf("expected Cancelled.:\n%s", out.String())
		}
		if strings.Contains(out.String(), "Configuration written.") {
			t.Error("cancel must not write config")
		}
	})
}

// isUserAbort normalises huh.ErrUserAborted (including wrapped errors) so a
// Bubble Tea Ctrl+C maps to the same cancel path as an empty/declined choice.
func TestIsUserAbort(t *testing.T) {
	if !isUserAbort(huh.ErrUserAborted) {
		t.Error("bare ErrUserAborted must be an abort")
	}
	if !isUserAbort(fmt.Errorf("wrapped: %w", huh.ErrUserAborted)) {
		t.Error("wrapped ErrUserAborted must be an abort")
	}
	if isUserAbort(nil) {
		t.Error("nil must not be an abort")
	}
	if isUserAbort(errors.New("boom")) {
		t.Error("an unrelated error must not be an abort")
	}
}

// Selecting models through the accessible form must write opts.Models back
// grouped by family, so curated defaults are replaced by the chosen subset.
func TestModelSelectionGroupsByFamily(t *testing.T) {
	home := isolatedHome(t)
	var out bytes.Buffer
	// Agents: all three (claude-code -> Claude, codex -> GPT, opencode -> both),
	// so the flat model list is [claude x3, gpt x3] with indices 1..6.
	// Agent form: 1,2,3 selects all, 0 confirms.
	// Model form: 2,5 selects claude-opus-4-8 (Claude) and gpt-5.6-terra (GPT),
	// 0 confirms.
	// Confirm form: "y" applies.
	err := Run(input("1", "2", "3", "0", "2", "5", "0", "y"), &out, Options{Port: 6655})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Configuration written.") {
		t.Fatalf("expected applied config:\n%s", out.String())
	}
	settings := read(t, filepath.Join(home, "claude", "settings.json"))
	root := decodeJSONC(t, settings)
	env, _ := root["env"].(map[string]any)
	if env["ANTHROPIC_MODEL"] != "claude-opus-4-8" {
		t.Errorf("Claude family selection not applied: %s", settings)
	}
	if strings.Contains(settings, "claude-sonnet-4-6") || strings.Contains(settings, "claude-haiku-4-5") {
		t.Errorf("unselected Claude models must be replaced: %s", settings)
	}
	opencode := read(t, filepath.Join(home, "xdg", "opencode", "opencode.jsonc"))
	if !strings.Contains(opencode, "aicore-openai/gpt-5.6-terra") {
		t.Errorf("GPT family selection not applied: %s", opencode)
	}
	if strings.Contains(opencode, "gpt-6.1-sol") || strings.Contains(opencode, "gpt-5.4") {
		t.Errorf("unselected GPT models must be replaced: %s", opencode)
	}
}

// Enter/empty selection on the model form must keep the curated defaults.
func TestModelSelectionEmptyKeepsDefaults(t *testing.T) {
	home := isolatedHome(t)
	var out bytes.Buffer
	// Agent: claude-code only. Model form: 0 confirms nothing selected.
	err := Run(input("1", "0", "0", "y"), &out, Options{Port: 6655})
	if err != nil {
		t.Fatal(err)
	}
	settings := read(t, filepath.Join(home, "claude", "settings.json"))
	for _, want := range []string{"claude-sonnet-4-6", "claude-opus-4-8", "claude-haiku-4-5"} {
		if !strings.Contains(settings, want) {
			t.Errorf("curated default %q missing: %s", want, settings)
		}
	}
}

// TestLineDelimitedReader pins down the production wrapper used by Run on the
// non-TTY path: at most one line per Read, long lines delivered in chunks
// without loss, a final line without '\n' still emitted, and EOF deferred until
// after all buffered bytes have been handed out.
func TestLineDelimitedReader(t *testing.T) {
	t.Run("one line per Read", func(t *testing.T) {
		r := newLineDelimitedReader(strings.NewReader("1\n0\ny\n"))
		buf := make([]byte, 8)
		var got strings.Builder
		var reads []string
		for {
			n, err := r.Read(buf)
			if n > 0 {
				got.Write(buf[:n])
				reads = append(reads, string(buf[:n]))
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if got.String() != "1\n0\ny\n" {
			t.Fatalf("got %q, want %q", got.String(), "1\n0\ny\n")
		}
		for i, line := range reads {
			if !strings.HasSuffix(line, "\n") {
				t.Errorf("Read %d returned a partial line %q", i, line)
			}
		}
	})

	t.Run("final line without newline and deferred EOF", func(t *testing.T) {
		r := newLineDelimitedReader(strings.NewReader("yes"))
		buf := make([]byte, 8)
		n, err := r.Read(buf)
		if err != nil {
			t.Fatalf("a Read carrying data must not return an error: %v", err)
		}
		if string(buf[:n]) != "yes" {
			t.Fatalf("got %q, want %q", buf[:n], "yes")
		}
		if n, err := r.Read(buf); n != 0 || err != io.EOF {
			t.Fatalf("EOF must be deferred to the next Read: n=%d err=%v", n, err)
		}
	})

	t.Run("long line is chunked without loss", func(t *testing.T) {
		line := strings.Repeat("x", 9000) + "\n"
		r := newLineDelimitedReader(strings.NewReader(line))
		buf := make([]byte, 4096) // same size as a bufio.Scanner's initial buffer
		var got strings.Builder
		for {
			n, err := r.Read(buf)
			got.Write(buf[:n])
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if got.String() != line {
			t.Fatalf("long line corrupted: got %d bytes, want %d", got.Len(), len(line))
		}
	})

	t.Run("empty input", func(t *testing.T) {
		r := newLineDelimitedReader(strings.NewReader(""))
		buf := make([]byte, 8)
		if n, err := r.Read(buf); n != 0 || err != io.EOF {
			t.Fatalf("want (0, EOF), got (%d, %v)", n, err)
		}
	})
}

// Long absolute paths in option labels wrap inside huh's 80-column form and
// consume a viewport row, which pushed the last agent off screen. Keep one
// compact option per registered agent so the multi-select stays complete.
func TestAgentOptionsListsEveryAgentCompactly(t *testing.T) {
	home := isolatedHome(t)
	env := Env{Home: home, Cwd: home, Getenv: os.Getenv}
	options := agentOptions(env)
	if len(options) != len(IDs()) {
		t.Fatalf("got %d options, want one per agent (%d)", len(options), len(IDs()))
	}
	for i, agent := range All() {
		label := options[i].Key
		if !strings.Contains(label, agent.Name()) {
			t.Errorf("option %d missing agent name %q: %q", i, agent.Name(), label)
		}
		if len(label) > 80 {
			t.Errorf("option %d is %d chars, wider than the default 80-column form: %q", i, len(label), label)
		}
		if options[i].Value != agent.ID() {
			t.Errorf("option %d stores %q, want agent ID %q", i, options[i].Value, agent.ID())
		}
	}
}

// Every agent must also be visible in the line-based (non-TTY) fallback, not
// just in the Bubble Tea renderer.
func TestAccessibleAgentPromptShowsAllThree(t *testing.T) {
	isolatedHome(t)
	var out bytes.Buffer
	if err := Run(strings.NewReader(""), &out, Options{Port: 3001}); err != nil {
		t.Fatalf("empty input must cancel cleanly: %v", err)
	}
	for _, name := range []string{"Claude Code", "Codex", "OpenCode"} {
		if !strings.Contains(out.String(), name) {
			t.Errorf("accessible prompt is missing %q:\n%s", name, out.String())
		}
	}
}

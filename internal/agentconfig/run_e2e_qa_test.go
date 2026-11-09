package agentconfig

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// QA regression (independent verification of the huh v2 migration).
//
// qaFullInput is the exact keystroke sequence a user would pipe into
// `aicoreproxy configure` with NO --yes and NO --agents, in order to pick a
// single agent, keep the curated model defaults, and confirm the write.
//
// The protocol is taken verbatim from charm.land/huh/v2@v2.0.3 accessible
// runners (internal/accessibility/accessibility.go, field_multiselect.go's
// RunAccessible, field_confirm.go's RunAccessible):
//
//	Agent MultiSelect : "1" toggles claude-code, then "0" finishes the field
//	Model MultiSelect : "0" finishes immediately (empty -> curated defaults)
//	Confirm           : "y" writes the files
//
// Each line is one answer to one accessible prompt.
const qaFullInput = "1\n0\n0\ny\n"

// qaAssertConfigWritten verifies the full interactive flow actually completed
// and wrote a real config file into the isolated HOME, rather than bailing out
// with "Cancelled.". The latter is the symptom of huh's fresh bufio.Scanner per
// prompt greedily draining a real (non-line-at-a-time) input stream.
func qaAssertConfigWritten(t *testing.T, out *bytes.Buffer, home string) {
	t.Helper()
	got := out.String()
	if strings.Contains(got, "Cancelled.") {
		t.Errorf("flow was cancelled instead of applied; full output:\n%s", got)
	}
	if !strings.Contains(got, "Configuration written.") {
		t.Errorf("missing %q; full output:\n%s", "Configuration written.", got)
	}
	settings := filepath.Join(home, "claude", "settings.json")
	if _, err := os.Stat(settings); err != nil {
		t.Fatalf("config file %s not written (%v); full output:\n%s", settings, err, got)
	}
	content := read(t, settings)
	if !strings.Contains(content, "localhost:3001") {
		t.Errorf("config lacks the requested port:\n%s", content)
	}
}

// TestQA_LineReaderControl is the control half of the experiment. It proves
// qaFullInput is a valid, complete sequence: fed one line per Read (the
// lineReader helper from run_test.go) it drives all three forms to completion
// and writes config. Keeping this green while the real-reader test below is red
// isolates the root cause to the reader type, not to the input sequence.
func TestQA_LineReaderControl(t *testing.T) {
	home := isolatedHome(t)
	var out bytes.Buffer
	if err := Run(input("1", "0", "0", "y"), &out, Options{Port: 3001}); err != nil {
		t.Fatalf("control run errored: %v", err)
	}
	qaAssertConfigWritten(t, &out, home)
}

// TestQA_RealNonTTYReaderCompletesFullFlow is the bug-reproduction test.
//
// It feeds the SAME logical input through plain, real non-TTY readers — exactly
// what a shell pipe, a Docker attach, a CI job, or
// `printf '1\n0\n0\ny\n' | aicoreproxy configure` produces. None of these
// return only one line per Read. If run.go shares one raw `in` across its three
// independent forms without wrapping it, huh's accessible PromptString builds a
// fresh bufio.Scanner on every question; the very first Scanner reads the whole
// stream, is then discarded, and every later form sees EOF. Confirm then falls
// back to its default "n" -> the command prints "Cancelled." and writes nothing.
//
// Expected (correct) behaviour: "Configuration written." + a file on disk.
// This test deliberately does NOT use lineReader.
func TestQA_RealNonTTYReaderCompletesFullFlow(t *testing.T) {
	t.Run("strings.NewReader", func(t *testing.T) {
		home := isolatedHome(t)
		var out bytes.Buffer
		if err := Run(strings.NewReader(qaFullInput), &out, Options{Port: 3001}); err != nil {
			t.Fatalf("Run errored: %v", err)
		}
		qaAssertConfigWritten(t, &out, home)
	})

	t.Run("bytes.Buffer", func(t *testing.T) {
		home := isolatedHome(t)
		var out bytes.Buffer
		if err := Run(bytes.NewBufferString(qaFullInput), &out, Options{Port: 3001}); err != nil {
			t.Fatalf("Run errored: %v", err)
		}
		qaAssertConfigWritten(t, &out, home)
	})

	t.Run("os.Pipe", func(t *testing.T) {
		home := isolatedHome(t)
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = r.Close() })
		if _, err := w.WriteString(qaFullInput); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := Run(r, &out, Options{Port: 3001}); err != nil {
			t.Fatalf("Run errored: %v", err)
		}
		qaAssertConfigWritten(t, &out, home)
	})
}

package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestConfigureCommandRegistered(t *testing.T) {
	cmd := NewRootCommand()
	child, _, err := cmd.Find([]string{"configure"})
	if err != nil || child.Name() != "configure" {
		t.Fatalf("configure command not registered: %v", err)
	}
	for _, flag := range []string{"agents", "port", "models", "yes", "dry-run", "status", "discover"} {
		if child.Flags().Lookup(flag) == nil {
			t.Errorf("missing --%s", flag)
		}
	}
}

func TestConfigureDoesNotStartServer(t *testing.T) {
	cmd := newRootCommand(func(bool) error {
		t.Fatal("configure started the server")
		return nil
	})
	var out, errOut bytes.Buffer
	if code := Execute(cmd, []string{"configure", "--help"}, &out, &errOut); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, &errOut)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("missing help: %s", out.String())
	}
}

func TestConfigureErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"configure", "--agents", "bogus", "--yes", "--dry-run"}, 1, "unknown agent"},
		{[]string{"configure", "--models", "gemini=x", "--yes", "--dry-run"}, 1, "unknown model family"},
		{[]string{"configure", "--port=invalid"}, 2, ""},
		{[]string{"configure", "extra"}, 2, ""},
	} {
		var out, errOut bytes.Buffer
		cmd := newRootCommand(func(bool) error {
			t.Fatal("configure started the server")
			return nil
		})
		if code := Execute(cmd, tc.args, &out, &errOut); code != tc.code {
			t.Errorf("%v: code=%d, want %d (stderr=%s)", tc.args, code, tc.code, &errOut)
		}
		if tc.want != "" && !strings.Contains(errOut.String(), tc.want) {
			t.Errorf("%v: stderr=%q, want %q", tc.args, &errOut, tc.want)
		}
	}
}

func TestConfigureDryRunWithoutSAPConfig(t *testing.T) {
	t.Setenv("AICOREPROXY_CONFIG_FILE", t.TempDir()+"/missing.json")
	t.Setenv("PORT", "")
	var out, errOut bytes.Buffer
	cmd := NewRootCommand()
	code := Execute(cmd, []string{"configure", "--agents", "claude-code", "--yes", "--dry-run"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, &errOut)
	}
	if !strings.Contains(out.String(), "Dry run") {
		t.Errorf("unexpected output: %s", out.String())
	}
	if !strings.Contains(out.String(), "http://localhost:3001") {
		t.Error("default port 3001 not used")
	}
}

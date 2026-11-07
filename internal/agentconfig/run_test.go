package agentconfig

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestRunNonInteractiveGuards(t *testing.T) {
	isolatedHome(t)
	var out bytes.Buffer
	err := Run(strings.NewReader(""), &out, Options{Port: 3001, Yes: true})
	if err == nil || !strings.Contains(err.Error(), "--agents") {
		t.Fatalf("want terminal/--agents guard, got %v", err)
	}
	err = Run(strings.NewReader(""), &out, Options{Port: 3001, Agents: IDs()})
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("want --yes guard, got %v", err)
	}
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

func TestParseSelection(t *testing.T) {
	for _, tc := range []struct {
		line string
		want string
	}{
		{"1 3", "claude-code,opencode"},
		{"2,3", "codex,opencode"},
		{"all", strings.Join(IDs(), ",")},
		{"", ""},
	} {
		got, err := parseSelection(tc.line)
		if err != nil {
			t.Fatalf("%q: %v", tc.line, err)
		}
		if strings.Join(got, ",") != tc.want {
			t.Errorf("%q = %v, want %q", tc.line, got, tc.want)
		}
	}
	if _, err := parseSelection("9"); err == nil {
		t.Error("out-of-range selection should fail")
	}
}

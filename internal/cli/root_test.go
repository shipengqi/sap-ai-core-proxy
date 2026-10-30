package cli

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/shipengqi/sap-ai-core-proxy/internal/version"
)

func TestServerDispatch(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		noTUI bool
	}{
		{nil, false}, {[]string{"serve"}, false},
		{[]string{"--no-tui"}, true}, {[]string{"-no-tui"}, true},
		{[]string{"serve", "-no-tui=true"}, true},
		{[]string{"--no-tui=false"}, false}, {[]string{"-no-tui=false"}, false},
		{[]string{"--version=false"}, false},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			calls := 0
			cmd := newRootCommand(func(noTUI bool) error {
				calls++
				if noTUI != tc.noTUI {
					t.Errorf("noTUI=%v, want %v", noTUI, tc.noTUI)
				}
				return nil
			})
			var out, errOut bytes.Buffer
			if code := Execute(cmd, tc.args, &out, &errOut); code != 0 {
				t.Fatalf("code=%d: %s", code, &errOut)
			}
			if calls != 1 {
				t.Fatalf("server calls=%d", calls)
			}
		})
	}
}

func TestVersionAndHelpBypassServer(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}, {"-version"}, {"serve", "--version"}, {"serve", "-version=true"}, {"--help"}, {"-help"}, {"serve", "--help"}, {"tui", "--help"}, {"cacheprobe", "--help"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := newRootCommand(func(bool) error { t.Fatal("server invoked for version/help"); return nil })
			var out, errOut bytes.Buffer
			if code := Execute(cmd, args, &out, &errOut); code != 0 {
				t.Fatalf("code=%d: %s", code, &errOut)
			}
			if errOut.Len() != 0 {
				t.Fatalf("stderr=%s", &errOut)
			}
			if strings.Contains(strings.Join(args, " "), "help") {
				if !strings.Contains(out.String(), "Usage:") {
					t.Fatalf("missing help: %s", &out)
				}
			} else if out.String() != version.String()+"\n" {
				t.Fatalf("version=%q", out.String())
			}
		})
	}
}

func TestVersionWithoutValidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("AICOREPROXY_CONFIG_FILE", path)
	for _, value := range []string{"", "malformed JSON"} {
		if value != "" {
			if err := os.WriteFile(path, []byte(value), 0600); err != nil {
				t.Fatal(err)
			}
		}
		for _, args := range [][]string{{"version"}, {"-version"}, {"serve", "--version"}, {"--help"}, {"tui", "--help"}, {"cacheprobe", "--help"}} {
			var out, errOut bytes.Buffer
			if code := Execute(NewRootCommand(), args, &out, &errOut); code != 0 {
				t.Fatalf("%v: code=%d: %s", args, code, &errOut)
			}
		}
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{{"--unknown"}, {"-unknown"}, {"bogus"}, {"serve", "extra"}, {"--no-tui=invalid"}, {"tui", "--interval=invalid"}, {"cacheprobe", "--models"}, {"tui", "--", "-plain"}} {
		var out, errOut bytes.Buffer
		cmd := newRootCommand(func(bool) error { t.Fatal("server invoked for invalid args"); return nil })
		if code := Execute(cmd, args, &out, &errOut); code != 2 {
			t.Errorf("%v: code=%d, stderr=%s", args, code, &errOut)
		}
		if errOut.Len() == 0 {
			t.Errorf("%v: missing diagnostic", args)
		}
	}
}

func TestRuntimeError(t *testing.T) {
	var out, errOut bytes.Buffer
	cmd := newRootCommand(func(bool) error { return errors.New("config error: test") })
	if code := Execute(cmd, nil, &out, &errOut); code != 1 {
		t.Fatalf("code=%d", code)
	}
	if errOut.String() != "config error: test\n" {
		t.Fatalf("diagnostic=%q", errOut.String())
	}
}

func TestToolFlags(t *testing.T) {
	for _, tc := range []struct {
		args              []string
		name, field, want string
	}{
		{[]string{"tui", "-url", "-plain", "-interval=5s", "-plain=false"}, "tui", "url", "-plain"},
		{[]string{"cacheprobe", "-models", "gpt-4o", "-timeout", "3s", "-salt", "-models"}, "cacheprobe", "salt", "-models"},
	} {
		cmd := NewRootCommand()
		child, _, err := cmd.Find([]string{tc.name})
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		child.RunE = func(c *cobra.Command, _ []string) error {
			calls++
			if got, _ := c.Flags().GetString(tc.field); got != tc.want {
				t.Errorf("%s=%q, want %q", tc.field, got, tc.want)
			}
			return nil
		}
		var out, errOut bytes.Buffer
		if code := Execute(cmd, tc.args, &out, &errOut); code != 0 {
			t.Fatalf("%v: code=%d: %s", tc.args, code, &errOut)
		}
		if calls != 1 {
			t.Fatalf("calls=%d", calls)
		}
	}
}

func TestTUIPlainWithoutConfig(t *testing.T) {
	t.Setenv("AICOREPROXY_CONFIG_FILE", filepath.Join(t.TempDir(), "missing.json"))
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/version" {
			_, _ = w.Write([]byte(`{"version":"remote-test"}`))
		} else {
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	for _, cmd := range []*cobra.Command{NewRootCommand(), NewTUICommand()} {
		requests = 0
		args := []string{"-url", server.URL, "-plain"}
		if cmd.Name() == "aicoreproxy" {
			args = append([]string{"tui"}, args...)
		}
		var out, errOut bytes.Buffer
		if code := Execute(cmd, args, &out, &errOut); code != 0 {
			t.Fatalf("code=%d: %s", code, &errOut)
		}
		if requests != 4 {
			t.Fatalf("requests=%d, want one four-endpoint snapshot", requests)
		}
		if strings.Contains(out.String(), "\033") || !strings.Contains(out.String(), "remote-test  [OK]") {
			t.Fatalf("plain output=%q", out.String())
		}
	}
}

func TestToolDefaults(t *testing.T) {
	tui := NewTUICommand()
	if got, _ := tui.Flags().GetString("url"); got != "http://localhost:3001" {
		t.Fatal(got)
	}
	if got, _ := tui.Flags().GetDuration("interval"); got.String() != "2s" {
		t.Fatal(got)
	}
	if got, _ := tui.Flags().GetBool("plain"); got {
		t.Fatal("plain defaults true")
	}
	probe := NewCacheProbeCommand()
	if got, _ := probe.Flags().GetString("models"); got != "gpt-5.6-terra,gpt-4o" {
		t.Fatal(got)
	}
	if got, _ := probe.Flags().GetDuration("timeout"); got.String() != "2m0s" {
		t.Fatal(got)
	}
	if got, _ := probe.Flags().GetString("salt"); got != "" {
		t.Fatal(got)
	}
}

func TestLegacyArgsTerminator(t *testing.T) {
	args := []string{"tui", "--", "-plain", "-url=example"}
	got := legacyArgs(NewRootCommand(), args)
	if strings.Join(got, "|") != strings.Join(args, "|") {
		t.Fatalf("rewrote args after --: %v", got)
	}
}

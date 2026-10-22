package version

import (
	"strings"
	"testing"
)

func TestGetFillsEveryField(t *testing.T) {
	info := Get()
	for name, value := range map[string]string{
		"version":    info.Version,
		"commit":     info.Commit,
		"build_date": info.BuildDate,
		"go_version": info.GoVersion,
		"platform":   info.Platform,
	} {
		if value == "" {
			t.Errorf("%s is empty, want a usable placeholder", name)
		}
	}
	if !strings.Contains(info.Platform, "/") {
		t.Errorf("platform = %q, want os/arch", info.Platform)
	}
}

// TestStringIsScriptable guards the -version output contract: one line, no
// trailing whitespace, and it must actually carry the version. Anything that
// prints extra lines breaks `$(aicoreproxy -version)` callers.
func TestStringIsScriptable(t *testing.T) {
	out := String()
	if strings.Contains(out, "\n") {
		t.Fatalf("version output must be a single line: %q", out)
	}
	if out != strings.TrimSpace(out) {
		t.Fatalf("version output has surrounding whitespace: %q", out)
	}
	for _, want := range []string{Product, Get().Version, "commit ", "built "} {
		if !strings.Contains(out, want) {
			t.Errorf("version output missing %q: %s", want, out)
		}
	}
}

// TestGetIsStable makes sure the getters do not mutate shared state between
// calls; the HTTP handler and the dashboards all read through Get.
func TestGetIsStable(t *testing.T) {
	if first, second := Get(), Get(); first != second {
		t.Fatalf("Get is not stable: %+v != %+v", first, second)
	}
}

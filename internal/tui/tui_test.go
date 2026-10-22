package tui

import (
	"strings"
	"testing"

	"github.com/shipengqi/sap-ai-core-proxy/internal/compress"
	"github.com/shipengqi/sap-ai-core-proxy/internal/tui/stats"
)

func TestWrapLogLinesPreservesCompleteRecord(t *testing.T) {
	line := `{"msg":"upstream request failed","err":"Post https://example.invalid/invoke: context canceled"}`
	got := wrapLogLines([]string{line}, 24)
	if len(got) < 2 {
		t.Fatalf("expected wrapped rows, got %q", got)
	}
	if joined := strings.Join(got, ""); joined != line {
		t.Fatalf("wrapped rows lost content:\n got %q\nwant %q", joined, line)
	}
}

func TestWrapLogLinesNormalizesTerminalBreakingWhitespace(t *testing.T) {
	got := wrapLogLines([]string{"left\tright\r"}, 80)
	if len(got) != 1 || got[0] != "left    right" {
		t.Fatalf("got %q, want normalized one-line record", got)
	}
}

func TestFormatLogRecordMakesUpstreamErrorReadable(t *testing.T) {
	line := `{"time":"2026-10-21T08:00:00.123Z","level":"ERROR","msg":"upstream request failed","method":"POST","url":"https://api.example/v2/inference/deployments/d20/invoke","err":"Post https://api.example/v2/inference/deployments/d20/invoke: context canceled"}`
	got, visible := formatLogRecord(line)
	if !visible {
		t.Fatal("upstream error unexpectedly hidden")
	}
	if len(got) != 3 {
		t.Fatalf("got %d lines, want summary + url + error: %q", len(got), got)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"ERROR  upstream request failed", "method=POST", "url: https://api.example", "error: Post https://api.example", "context canceled"} {
		if !strings.Contains(joined, want) {
			t.Errorf("formatted log missing %q: %s", want, joined)
		}
	}
}

func TestRenderSeparatesLogRegionFromHeaderAndFooter(t *testing.T) {
	logs := NewLogBuffer(10)
	_, _ = logs.Write([]byte(`{"level":"ERROR","msg":"upstream failed","err":"rate limited"}` + "\n"))

	out := render("http://localhost:3001", stats.Snapshot{}, logs, 120, 40)
	if !strings.Contains(out, "health/stats polls hidden"+reset+"\n\n") {
		t.Fatal("diagnostic title must have a blank line before log rows")
	}
	if !strings.Contains(out, reset+"\n\n  "+dim+"Updated ") {
		t.Fatal("log region must have a blank line before the footer")
	}
}

func TestRenderShowsCompressionEffect(t *testing.T) {
	snap := stats.Snapshot{Compress: stats.CompressResponse{
		Bytes:  []compress.BytesSeries{{Surface: "anthropic", BytesBefore: 10240, BytesAfter: 2048}},
		Blocks: []compress.BlockSeries{{Surface: "anthropic", Result: "compressed", Count: 8}, {Surface: "anthropic", Result: "skipped", Count: 2}},
	}}
	out := render("http://localhost:3001", snap, nil, 120, 40)
	for _, want := range []string{"LOG COMPRESSION", "10.0 KiB", "2.0 KiB", "8.0 KiB", "80.0%", "Blocks compressed 8", "skipped 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q", want)
		}
	}
}

func TestRenderShowsEmptyCompressionState(t *testing.T) {
	out := render("http://localhost:3001", stats.Snapshot{}, nil, 120, 40)
	if !strings.Contains(out, "No eligible diagnostic blocks compressed yet") {
		t.Fatal("render must explain empty compression data")
	}
}

func TestLogLevelColor(t *testing.T) {
	tests := map[string]string{
		"ERROR": red + bold,
		"WARN":  yellow,
		"INFO":  white,
		"DEBUG": dim,
	}
	for level, want := range tests {
		if got := logLevelColor(level); got != want {
			t.Errorf("logLevelColor(%q) = %q, want %q", level, got, want)
		}
	}
}

func TestWrappedDiagnosticRowsPreserveLevel(t *testing.T) {
	got := wrapDiagnosticLogLines([]diagnosticLogLine{{Text: "a very long error detail", Level: "ERROR"}}, 8)
	if len(got) < 2 {
		t.Fatalf("expected wrapped rows, got %+v", got)
	}
	for _, line := range got {
		if line.Level != "ERROR" {
			t.Fatalf("wrapped row lost level: %+v", got)
		}
	}
}

func TestFormatLogRecordHidesDashboardPolling(t *testing.T) {
	for _, path := range []string{"/health", "/stats", "/stats/cache", "/stats/compress"} {
		line := `{"level":"INFO","msg":"request","method":"GET","path":"` + path + `","status":200}`
		if got, visible := formatLogRecord(line); visible || got != nil {
			t.Fatalf("poll %s should be hidden, got %q", path, got)
		}
	}

	line := `{"level":"INFO","msg":"request","method":"POST","path":"/anthropic/v1/messages","status":200,"model":"claude-sonnet-4-6[1m]"}`
	if got, visible := formatLogRecord(line); !visible || len(got) != 1 {
		t.Fatalf("inference request should remain visible, got %q", got)
	}
}

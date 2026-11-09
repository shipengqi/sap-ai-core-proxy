package dashboard

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shipengqi/sap-ai-core-proxy/internal/tui/stats"
)

func statsFetcherFor(url string) *stats.Fetcher { return stats.NewWithVersion(url) }

// TestRunRepaintPlainExitsAfterOneFrame confirms that --plain mode renders
// exactly one frame and returns, matching the original contract that pipe
// consumers and tests rely on.
func TestRunRepaintPlainExitsAfterOneFrame(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/version" {
			_, _ = w.Write([]byte(`{"version":"repaint-1"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var out bytes.Buffer
	err := runRepaint(context.Background(), &out, statsFetcherFor(srv.URL), 50*time.Millisecond, true)
	if err != nil {
		t.Fatalf("runRepaint plain failed: %v", err)
	}
	// Plain mode must not emit escape codes.
	if strings.Contains(out.String(), "\x1b[") {
		t.Errorf("plain output should not contain escape codes:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "repaint-1") {
		t.Errorf("plain output missing version:\n%s", out.String())
	}
}

// TestRunRepaintLoopingExitsOnCancel confirms that non-plain repaint mode
// (non-terminal stdout without --plain) loops until the context is cancelled.
func TestRunRepaintLoopingExitsOnCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay so the loop has time to run at least once.
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	var out bytes.Buffer
	err := runRepaint(ctx, &out, statsFetcherFor(srv.URL), 30*time.Millisecond, false)
	if err != nil {
		t.Fatalf("runRepaint looping failed: %v", err)
	}
	// Non-plain mode should contain escape codes (screen clear).
	if !strings.Contains(out.String(), "\x1b[") {
		t.Errorf("non-plain repaint should contain escape codes for screen clear")
	}
}

// TestIsTerminalWriterOnBuffer confirms that a bytes.Buffer is not detected as
// a terminal, which is what routes pipe consumers to runRepaint.
func TestIsTerminalWriterOnBuffer(t *testing.T) {
	var buf bytes.Buffer
	if isTerminalWriter(&buf) {
		t.Fatal("bytes.Buffer should not be detected as a terminal writer")
	}
}

// TestDashboardModelUpdateCycle verifies the standalone model's snapshot/tick
// cycle mirrors the in-process one.
func TestDashboardModelUpdateCycle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	m := dashboardModel{
		base:     srv.URL,
		interval: 50 * time.Millisecond,
		fetcher:  statsFetcherFor(srv.URL),
		ctx:      context.Background(),
	}

	// Init should produce a fetch command.
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init returned nil command")
	}
	msg := cmd()
	if _, ok := msg.(snapshotMsg); !ok {
		t.Fatalf("Init produced %T, want snapshotMsg", msg)
	}

	// snapshotMsg should flip ready and schedule a tick.
	updated, tickCmd := m.Update(snapshotMsg(stats.Snapshot{}))
	if !updated.(dashboardModel).ready {
		t.Fatal("snapshotMsg did not set ready")
	}
	if tickCmd == nil {
		t.Fatal("snapshotMsg should schedule a tick")
	}

	// View before ready is empty; after ready it renders.
	if m.View().Content != "" {
		t.Fatal("pre-ready view should be empty")
	}
	v := updated.View()
	if v.Content == "" {
		t.Fatal("ready view should not be empty")
	}
}

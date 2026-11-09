package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shipengqi/sap-ai-core-proxy/internal/tui/stats"
)

// TestModelInitFetches starts a model and confirms Init returns a command
// that produces a snapshotMsg when executed.
func TestModelInitFetches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	m := dashboardModel{
		base:     srv.URL,
		interval: 50 * time.Millisecond,
		fetcher:  stats.New(srv.URL),
		ctx:      context.Background(),
		width:    100,
		height:   30,
	}
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init returned nil command, expected a fetch")
	}
	msg := cmd()
	if _, ok := msg.(snapshotMsg); !ok {
		t.Fatalf("Init command produced %T, want snapshotMsg", msg)
	}
}

// TestModelUpdateTransitionsReady verifies that receiving a snapshotMsg flips
// ready to true and schedules a tick, and that a tickMsg triggers another fetch.
func TestModelUpdateTransitionsReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	m := dashboardModel{
		base:     srv.URL,
		interval: 50 * time.Millisecond,
		fetcher:  stats.New(srv.URL),
		ctx:      context.Background(),
		width:    100,
		height:   30,
	}
	if m.ready {
		t.Fatal("model should start not-ready")
	}
	updated, tickCmd := m.Update(snapshotMsg(stats.Snapshot{}))
	if !updated.(dashboardModel).ready {
		t.Fatal("snapshotMsg did not flip ready to true")
	}
	if tickCmd == nil {
		t.Fatal("snapshotMsg should schedule a tick command")
	}
	// The tick command produces a tickMsg.
	tm := tickCmd()
	if _, ok := tm.(tickMsg); !ok {
		t.Fatalf("tick command produced %T, want tickMsg", tm)
	}
	// On tickMsg, Update should return a fetch command.
	_, fetchCmd := updated.Update(tickMsg{})
	if fetchCmd == nil {
		t.Fatal("tickMsg should trigger a new fetch command")
	}
}

// TestModelViewBeforeReadyIsEmpty ensures the first frame is blank until the
// first poll completes, so the screen does not flash stale defaults.
func TestModelViewBeforeReadyIsEmpty(t *testing.T) {
	m := dashboardModel{ready: false}
	v := m.View()
	if v.Content != "" {
		t.Fatalf("pre-ready view should be empty, got %q", v.Content)
	}
}

// TestModelViewAfterReadyRenders confirms that once a snapshot arrives, the
// view contains the expected header and is not blank.
func TestModelViewAfterReadyRenders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	m := dashboardModel{
		base:    srv.URL,
		fetcher: stats.New(srv.URL),
		ctx:     context.Background(),
		ready:   true,
		snap:    stats.Snapshot{},
		width:   100,
		height:  30,
	}
	v := m.View()
	if v.Content == "" {
		t.Fatal("ready view should not be empty")
	}
	if !strings.Contains(v.Content, "SAP AI CORE PROXY") {
		t.Fatalf("ready view missing header:\n%s", v.Content)
	}
}

// TestModelWindowSizeUpdatesDimensions checks that a WindowSizeMsg updates the
// model's width and height so render can adapt its layout.
func TestModelWindowSizeUpdatesDimensions(t *testing.T) {
	m := dashboardModel{width: 0, height: 0}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	dm := updated.(dashboardModel)
	if dm.width != 80 || dm.height != 24 {
		t.Fatalf("WindowSizeMsg did not update dimensions: got %dx%d, want 80x24", dm.width, dm.height)
	}
}

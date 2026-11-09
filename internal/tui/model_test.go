package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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

func TestProgramErrorPreservesConcurrentFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	failure := errors.New("terminal write failed")
	for _, err := range []error{
		tea.ErrProgramKilled,
		fmt.Errorf("%w: %w", tea.ErrProgramKilled, tea.ErrProgramPanic),
		fmt.Errorf("%w: %w", tea.ErrProgramKilled, failure),
		errors.Join(tea.ErrProgramKilled, context.Canceled, tea.ErrProgramPanic),
		errors.Join(tea.ErrProgramKilled, context.Canceled, failure),
	} {
		if got := programError(ctx, err); got != err {
			t.Fatalf("concurrent cancellation swallowed %v", err)
		}
	}
	err := fmt.Errorf("%w: %w", tea.ErrProgramKilled, context.Canceled)
	if got := programError(ctx, err); got != nil {
		t.Fatalf("expected cancellation returned %v", got)
	}
	if got := programError(context.Background(), err); got != err {
		t.Fatal("uncanceled owner masked cancellation from elsewhere")
	}
}

func TestModelViewFitsRendererHeight(t *testing.T) {
	for _, width := range []int{64, 120} {
		m := dashboardModel{ready: true, width: width, height: 48}
		frame := m.View().Content
		if strings.Count(frame, "\n")+1 > m.height || strings.HasSuffix(frame, "\n") {
			t.Fatalf("view overflows renderer height at width %d", width)
		}
	}
}

func TestModelViewUsesUpdateSnapshot(t *testing.T) {
	logs := NewLogBuffer(500)
	_, _ = logs.Write([]byte("first log\n"))
	m := dashboardModel{logs: logs, width: 120, height: 48}
	updated, _ := m.Update(snapshotMsg(stats.Snapshot{}))
	before := updated.View().Content
	_, _ = logs.Write([]byte("new log must wait for Update\n"))
	if after := updated.View().Content; after != before {
		t.Fatal("View changed without an Update message")
	}
}

func TestModelLogNavigationFreezesHistoryAndEndFollows(t *testing.T) {
	logs := NewLogBuffer(500)
	for i := 0; i < 80; i++ {
		_, _ = fmt.Fprintf(logs, "entry-%03d\n", i)
	}
	m := dashboardModel{logs: logs, width: 120, height: 48}
	updated, _ := m.Update(snapshotMsg(stats.Snapshot{}))
	m = updated.(dashboardModel)
	_, rows := renderLogFrame(m.base, m.snap, m.lines, m.width, m.height, 0, false)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	m = updated.(dashboardModel)
	if !m.paused || m.offset != rows {
		t.Fatalf("PgUp offset=%d paused=%v, want %d", m.offset, m.paused, rows)
	}
	paused := m.View().Content
	for i := 80; i < 700; i++ {
		_, _ = fmt.Fprintf(logs, "entry-%03d\n", i)
	}
	updated, _ = m.Update(snapshotMsg(stats.Snapshot{}))
	m = updated.(dashboardModel)
	if m.View().Content != paused {
		t.Fatal("new logs or ring eviction moved paused history")
	}
	for _, key := range []rune{tea.KeyUp, tea.KeyDown, tea.KeyPgDown} {
		old := m.offset
		updated, _ = m.Update(tea.KeyPressMsg{Code: key})
		m = updated.(dashboardModel)
		want := old - 1
		switch key {
		case tea.KeyUp:
			want = old + 1
		case tea.KeyPgDown:
			want = max(0, old-rows)
		}
		if m.offset != want || !m.paused {
			t.Fatalf("key=%d offset=%d want=%d paused=%v", key, m.offset, want, m.paused)
		}
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	m = updated.(dashboardModel)
	if m.paused || m.offset != 0 || !strings.Contains(m.View().Content, "entry-699") {
		t.Fatal("End did not return to newest live logs")
	}
	for _, hint := range []string{"FOLLOW", "PgUp/PgDn", "End follow", "Ctrl-C exit"} {
		if !strings.Contains(m.View().Content, hint) {
			t.Fatalf("missing navigation help %q", hint)
		}
	}
}

func TestLogFrameTinyDimensionsAndUnicode(t *testing.T) {
	for _, size := range [][2]int{{1, 1}, {2, 2}, {20, 12}, {40, 48}, {80, 24}, {120, 48}} {
		frame, _ := renderLogFrame("local", stats.Snapshot{}, nil, size[0], size[1], 0, false)
		if visibleLines(frame) > size[1] {
			t.Fatalf("size %v overflows height", size)
		}
	}
	for _, text := range []string{strings.Repeat("界", 100), strings.Repeat("👩‍💻", 100), strings.Repeat("e\u0301", 100)} {
		rows := wrapDiagnosticLogLines([]diagnosticLogLine{{Text: text}}, 20)
		var rebuilt strings.Builder
		for _, row := range rows {
			if ansi.StringWidth(row.Text) > 20 {
				t.Fatal("Unicode log exceeds physical cell budget")
			}
			rebuilt.WriteString(row.Text)
		}
		if rebuilt.String() != text {
			t.Fatal("Unicode grapheme wrapping lost content")
		}
	}
}

func TestLogFrameNarrowFooterKeepsExitVisible(t *testing.T) {
	for _, width := range []int{40, 64, 120} {
		frame, _ := renderLogFrame("local", stats.Snapshot{}, nil, width, 48, 0, false)
		lines := strings.Split(frame, "\n")
		footer := lines[len(lines)-2]
		if !strings.Contains(footer, "FOLLOW") || !strings.Contains(footer, "Ctrl-C") || !strings.Contains(footer, "End") {
			t.Fatalf("width %d hides navigation controls: %q", width, footer)
		}
	}
}

func TestLogFrameCountsEmbeddedNewlinesAsPhysicalRows(t *testing.T) {
	lines := []diagnosticLogLine{{Text: strings.Repeat("detail\n", 100), Level: "ERROR"}}
	wrapped := wrapDiagnosticLogLines(lines, 80)
	if len(wrapped) != 101 {
		t.Fatalf("got %d physical rows, want 101", len(wrapped))
	}
	for _, row := range wrapped {
		if strings.Contains(row.Text, "\n") || row.Level != "ERROR" {
			t.Fatal("embedded newline or lost level in physical row")
		}
	}
	frame, _ := renderLogFrame("local", stats.Snapshot{}, lines, 120, 48, 0, false)
	if visibleLines(frame) > 48 {
		t.Fatal("multiline log overflowed the terminal frame")
	}
}

func TestModelLogNavigationClampsEmptyWrappedAndResizedHistory(t *testing.T) {
	m := dashboardModel{ready: true, width: 12, height: 48}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	m = updated.(dashboardModel)
	if !m.paused || m.offset != 0 {
		t.Fatal("empty history navigation is not clamped")
	}
	m.lines = []diagnosticLogLine{{Text: strings.Repeat("abcdefgh", 100), Level: "ERROR"}}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = updated.(dashboardModel)
	if m.offset != 1 || !strings.Contains(m.View().Content, "PAUSED") {
		t.Fatal("Up must move one physical wrapped row")
	}
	for range 100 {
		updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
		m = updated.(dashboardModel)
	}
	_, rows := renderLogFrame(m.base, m.snap, m.lines, m.width, m.height, m.offset, true)
	if m.offset != len(wrapDiagnosticLogLines(m.lines, 4))-rows {
		t.Fatal("PgUp did not clamp to oldest physical rows")
	}
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 200, Height: 200})
	m = updated.(dashboardModel)
	if m.offset != 0 || !m.paused {
		t.Fatal("resize did not rewrap/clamp while preserving paused mode")
	}
}

func TestModelCtrlCRequestsQuit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := dashboardModel{cancel: cancel}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if ctx.Err() == nil {
		t.Fatal("Ctrl-C must cancel the server owner, not just quit the panel")
	}
	if cmd == nil {
		t.Fatal("Ctrl-C must request a clean panel exit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("Ctrl-C command must quit Bubble Tea")
	}
}

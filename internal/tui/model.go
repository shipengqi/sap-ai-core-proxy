package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shipengqi/sap-ai-core-proxy/internal/tui/stats"
)

// snapshotMsg carries one poll result into the Bubble Tea update loop.
type snapshotMsg stats.Snapshot

// tickMsg asks for the next poll once the refresh interval has elapsed.
type tickMsg struct{}

// dashboardModel owns a frozen log view; the server owns process cancellation.
type dashboardModel struct {
	base     string
	interval time.Duration
	logs     *LogBuffer
	fetcher  *stats.Fetcher
	ctx      context.Context
	snap     stats.Snapshot
	width    int
	height   int
	ready    bool
	cancel   context.CancelFunc
	lines    []diagnosticLogLine
	paused   bool
	offset   int
}

func fetchCmd(ctx context.Context, f *stats.Fetcher) tea.Cmd {
	return func() tea.Msg { return snapshotMsg(f.Fetch(ctx)) }
}

func (m dashboardModel) Init() tea.Cmd { return fetchCmd(m.ctx, m.fetcher) }

func (m dashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case snapshotMsg:
		m.snap = stats.Snapshot(v)
		m.ready = true
		if !m.paused {
			m.captureLogs()
		}
		m.clampOffset()
		return m, tea.Tick(m.interval, func(time.Time) tea.Msg { return tickMsg{} })
	case tickMsg:
		return m, fetchCmd(m.ctx, m.fetcher)
	case tea.WindowSizeMsg:
		if v.Width > 0 && v.Height > 0 {
			m.width, m.height = v.Width, v.Height
			m.clampOffset()
		}
	case tea.KeyPressMsg:
		if v.String() == "ctrl+c" {
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		}
		_, rows := renderLogFrame(m.base, m.snap, m.lines, m.width, m.height, m.offset, m.paused)
		switch v.Code {
		case tea.KeyUp:
			m.paused = true
			m.offset++
		case tea.KeyDown:
			m.paused = true
			m.offset--
		case tea.KeyPgUp:
			m.paused = true
			m.offset += rows
		case tea.KeyPgDown:
			m.paused = true
			m.offset -= rows
		case tea.KeyEnd:
			m.paused = false
			m.offset = 0
			m.captureLogs()
		}
		m.clampOffset()
	}
	return m, nil
}

func (m dashboardModel) View() tea.View {
	if !m.ready {
		return tea.NewView("")
	}
	frame, _ := renderLogFrame(m.base, m.snap, m.lines, m.width, m.height, m.offset, m.paused)
	// A trailing newline adds a blank row and makes Bubble Tea redraw overflow.
	return tea.NewView(strings.TrimSuffix(frame, "\n"))
}

func (m *dashboardModel) captureLogs() {
	if m.logs != nil {
		m.lines = formatLogRecords(m.logs.Tail(500))
	}
}

func (m *dashboardModel) clampOffset() {
	_, rows := renderLogFrame(m.base, m.snap, m.lines, m.width, m.height, m.offset, m.paused)
	count := len(wrapDiagnosticLogLines(m.lines, max(1, m.width-8)))
	m.offset = min(max(0, m.offset), max(0, count-rows))
}

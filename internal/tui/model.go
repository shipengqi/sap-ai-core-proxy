package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shipengqi/sap-ai-core-proxy/internal/tui/stats"
)

// snapshotMsg carries one poll result into the Bubble Tea update loop.
type snapshotMsg stats.Snapshot

// tickMsg asks for the next poll once the refresh interval has elapsed.
type tickMsg struct{}

// dashboardModel is a display-only Bubble Tea model. It deliberately reads no
// input: this process also runs the HTTP server, and putting the terminal into
// raw mode would swallow the Ctrl-C that stops that server.
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
		return m, tea.Tick(m.interval, func(time.Time) tea.Msg { return tickMsg{} })
	case tickMsg:
		return m, fetchCmd(m.ctx, m.fetcher)
	case tea.WindowSizeMsg:
		if v.Width > 0 && v.Height > 0 {
			m.width, m.height = v.Width, v.Height
		}
	}
	return m, nil
}

func (m dashboardModel) View() tea.View {
	if !m.ready {
		return tea.NewView("")
	}
	width, height := m.width, m.height
	// Bubble Tea's signal handler is disabled here, so SIGWINCH may never turn
	// into a WindowSizeMsg. Re-reading the size every frame keeps the layout
	// correct after a resize without owning a process-wide signal handler.
	if w, h := terminalSize(); w > 0 && h > 0 {
		width, height = w, h
	}
	return tea.NewView(render(m.base, m.snap, m.logs, width, height))
}

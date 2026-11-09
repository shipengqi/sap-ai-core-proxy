package dashboard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shipengqi/sap-ai-core-proxy/internal/tui/stats"
)

// snapshotMsg carries one poll result into the Bubble Tea update loop.
type snapshotMsg stats.Snapshot

// tickMsg asks for the next poll once the refresh interval has elapsed.
type tickMsg struct{}

// dashboardModel renders the standalone panel and handles terminal Ctrl-C keys.
type dashboardModel struct {
	base     string
	interval time.Duration
	fetcher  *stats.Fetcher
	ctx      context.Context
	snap     stats.Snapshot
	ready    bool
	cancel   context.CancelFunc
}

func (m dashboardModel) Init() tea.Cmd {
	ctx, f := m.ctx, m.fetcher
	return func() tea.Msg { return snapshotMsg(f.Fetch(ctx)) }
}

func (m dashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case snapshotMsg:
		m.snap = stats.Snapshot(msg)
		m.ready = true
		return m, tea.Tick(m.interval, func(time.Time) tea.Msg { return tickMsg{} })
	case tickMsg:
		return m, m.Init()
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m dashboardModel) View() tea.View {
	if !m.ready {
		return tea.NewView("")
	}
	return tea.NewView(render(m.base, m.snap))
}

// runInteractive uses Bubble Tea's diff-based renderer, which repaints only the
// lines that changed instead of clearing and redrawing the whole screen every
// interval. Only used when out is a real terminal.
func runInteractive(ctx context.Context, out io.Writer, fetcher *stats.Fetcher, interval time.Duration) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := dashboardModel{base: fetcher.Base(), interval: interval, fetcher: fetcher, ctx: ctx, cancel: cancel}
	p := tea.NewProgram(m,
		tea.WithInput(os.Stdin),
		tea.WithOutput(out),
		tea.WithoutSignalHandler(),
		tea.WithContext(ctx),
	)
	_, err := p.Run()
	// Cancelling the context is how Run stops on Ctrl-C; Bubble Tea reports it
	// as ErrProgramKilled. That is a normal exit, not a failure.
	return programError(ctx, err)
}

func programError(ctx context.Context, err error) error {
	if ctx.Err() != nil && errors.Is(err, tea.ErrProgramKilled) && errors.Is(err, ctx.Err()) && onlyCancellation(err, ctx.Err()) {
		return nil
	}
	return err
}

func onlyCancellation(err, cause error) bool {
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		for _, child := range wrapped.Unwrap() {
			if !onlyCancellation(child, cause) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return onlyCancellation(wrapped.Unwrap(), cause)
	default:
		return err == tea.ErrProgramKilled || err == cause
	}
}

// runRepaint keeps the original behaviour for redirected output (pipes, files,
// tests) and --plain. It renders one frame and, when plain is true, exits —
// matching the original contract so pipe consumers and tests see a single
// snapshot. When plain is false (non-terminal stdout without --plain) it loops
// like the interactive panel but with full-screen clears instead of diffing.
func runRepaint(ctx context.Context, out io.Writer, fetcher *stats.Fetcher, interval time.Duration, plain bool) error {
	for {
		snap := fetcher.Fetch(ctx)
		if !plain {
			if _, err := fmt.Fprint(out, "\033[H\033[2J"); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprint(out, render(fetcher.Base(), snap)); err != nil {
			return err
		}
		if plain || ctx.Err() != nil {
			return nil
		}
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
	}
}

func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shipengqi/sap-ai-core-proxy/internal/tui/format"
	"github.com/shipengqi/sap-ai-core-proxy/internal/tui/stats"
	"github.com/shipengqi/sap-ai-core-proxy/internal/version"
)

const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	cyan   = "\033[36m"
	blue   = "\033[34m"
	green  = "\033[32m"
	yellow = "\033[33m"
	red    = "\033[31m"
	white  = "\033[97m"
)

func Interactive() bool {
	in, inErr := os.Stdin.Stat()
	out, outErr := os.Stdout.Stat()
	return inErr == nil && outErr == nil &&
		in.Mode()&os.ModeCharDevice != 0 && out.Mode()&os.ModeCharDevice != 0 &&
		os.Getenv("TERM") != "" && os.Getenv("TERM") != "dumb"
}

// Run reads navigation keys and forwards Ctrl-C to the server's lifecycle owner.
// It returns only after Bubble Tea has restored the shared terminal.
func Run(ctx context.Context, cancel context.CancelFunc, base string, interval time.Duration, logs *LogBuffer) error {
	width, height := terminalSize()
	m := dashboardModel{
		base:     base,
		interval: interval,
		logs:     logs,
		fetcher:  stats.New(base),
		ctx:      ctx,
		cancel:   cancel,
		width:    width,
		height:   height,
	}
	p := tea.NewProgram(m,
		tea.WithInput(os.Stdin),
		tea.WithOutput(os.Stdout),
		tea.WithoutSignalHandler(), // the HTTP server owns SIGINT/SIGTERM
		tea.WithContext(ctx),
	)
	_, err := p.Run()
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

func render(base string, s stats.Snapshot, logs *LogBuffer, width, height int) string {
	var lines []diagnosticLogLine
	if logs != nil {
		lines = formatLogRecords(logs.Tail(500))
	}
	frame, _ := renderLogFrame(base, s, lines, width, height, 0, false)
	return frame
}

func renderLogFrame(base string, s stats.Snapshot, lines []diagnosticLogLine, width, height, offset int, paused bool) (string, int) {
	var b strings.Builder
	status, statusColor := "HEALTHY", green
	if s.Down() {
		status, statusColor = "OFFLINE", red
	} else if !s.OK() {
		status, statusColor = "DEGRADED", yellow
	}
	// The version is read in-process rather than polled from GET /version: this
	// dashboard shares a process with the server it describes, so the build
	// identity is known before the first poll and stays visible even when the
	// proxy is OFFLINE.
	b.WriteString("\n  " + bold + white + "SAP AI CORE PROXY" + reset + "  " + dim + version.Get().Version + "  /  OBSERVABILITY" + reset + "\n")
	b.WriteString("  " + dim + base + reset + "                                      " + statusColor + "● " + status + reset + "\n\n")
	if width >= 72 {
		b.WriteString(cardRow(card("REQUESTS", format.HumanCount(s.Stats.Totals.Requests), "total requests", cyan), card("FAILURES", format.HumanCount(s.Stats.Totals.Failures), "upstream failures", sColor(s.Stats.Totals.Failures)), card("CACHE HIT RATE", format.Percent(s.Stats.Totals.CacheHitRatio), "prompt cache", green)))
	} else {
		b.WriteString("  " + line("Requests", format.HumanCount(s.Stats.Totals.Requests), cyan) + "\n  " + line("Failures", format.HumanCount(s.Stats.Totals.Failures), sColor(s.Stats.Totals.Failures)) + "\n  " + line("Cache hit rate", format.Percent(s.Stats.Totals.CacheHitRatio), green) + "\n")
	}
	b.WriteString("\n  " + bold + white + "TOKEN USAGE" + reset + "  " + dim + "measurement window " + format.Duration(s.Stats.UptimeSeconds) + reset + "\n")
	b.WriteString("  " + line("Input tokens", format.HumanCount(s.Stats.Totals.Tokens.Input), cyan) + "\n  " + line("Output tokens", format.HumanCount(s.Stats.Totals.Tokens.Output), blue) + "\n  " + line("Cache read", format.HumanCount(s.Stats.Totals.Tokens.CacheRead), green) + "\n  " + line("Cache creation", format.HumanCount(s.Stats.Totals.Tokens.CacheWrite), yellow) + "\n\n  " + bold + white + "BY SURFACE" + reset + "\n")
	if len(s.Stats.BySurface) == 0 {
		b.WriteString("  " + dim + "No requests recorded yet" + reset + "\n")
	}
	for _, x := range s.Stats.BySurface {
		fmt.Fprintf(&b, "  %-14s %8s requests   %8s failures   %s\n", x.Surface, format.HumanCount(x.Requests), format.HumanCount(x.Failures), format.Percent(x.CacheHitRatio))
	}
	b.WriteString(renderCompression(s))
	b.WriteString("\n  " + bold + white + "QUALITY SIGNALS" + reset + "\n" + fmt.Sprintf("  %s  %s  %s\n", badge("MISSING USAGE", s.Stats.Totals.UsageMissing, yellow), badge("UNINSTRUMENTED", s.Stats.Totals.Uninstrumented, yellow), badge("CAPTURE DROPPED", s.Stats.Totals.UsageCaptureDropped, yellow)))
	for _, e := range s.Errors {
		b.WriteString("  " + red + "× " + reset + e.Error() + "\n")
	}
	width, height = max(1, width), max(1, height)
	header := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	if height < len(header)+6 {
		// Tight screens prioritize title/status and logs over secondary metrics.
		header = header[1:]
		header = header[:min(len(header), max(0, height-6))]
	}
	b.Reset()
	for _, text := range header {
		b.WriteString(ansi.Truncate(text, width, "") + "\n")
	}
	const logChromeRows = 5
	rows := max(0, height-len(header)-logChromeRows)
	mode := "FOLLOW"
	if paused {
		mode = "PAUSED · frozen history"
	}
	title := "  " + bold + white + "DIAGNOSTIC LOGS" + reset + "  " + dim + mode + " · health/stats polls hidden" + reset
	if width < 40 {
		shortMode := "FOLLOW"
		if paused {
			shortMode = "PAUSED"
		}
		title = "  " + bold + white + shortMode + " LOGS" + reset
	}
	b.WriteString("\n" + title + "\n\n")
	entries := wrapDiagnosticLogLines(lines, max(1, width-8))
	offset = min(max(0, offset), max(0, len(entries)-rows))
	end := len(entries) - offset
	start := max(0, end-rows)
	entries = entries[start:end]
	for i := 0; i < rows; i++ {
		entry := diagnosticLogLine{}
		if i < len(entries) {
			entry = entries[i]
		}
		b.WriteString("  " + logLevelColor(entry.Level) + entry.Text + reset + "\n")
	}
	help := "PgUp/PgDn ↑/↓ scroll  •  End follow  •  Ctrl-C exit"
	footerMode := "FOLLOW"
	if paused {
		footerMode = "PAUSED"
	}
	if width >= 100 {
		help = "Updated " + format.Clock(s.FetchedAt) + "  •  " + footerMode + "  •  " + help
	} else if width < 56 {
		help = footerMode + " PgUp/Dn ↑/↓ End:live Ctrl-C:exit"
	} else {
		help = footerMode + "  •  " + help
	}
	if width < 40 {
		help = "^C exit End live"
	}
	b.WriteString("\n  " + dim + help + reset + "\n")
	frame := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	if len(frame) > height {
		// Extremely small screens show the exit hint instead of overflowing.
		frame = frame[len(frame)-height:]
	}
	for i, text := range frame {
		frame[i] = ansi.Truncate(text, width, "")
	}
	return strings.Join(frame, "\n") + "\n", rows
}
func renderCompression(s stats.Snapshot) string {
	var before, after, compressed, skipped int64
	for _, x := range s.Compress.Bytes {
		before += x.BytesBefore
		after += x.BytesAfter
	}
	for _, x := range s.Compress.Blocks {
		switch x.Result {
		case "compressed":
			compressed += x.Count
		case "skipped":
			skipped += x.Count
		}
	}
	var b strings.Builder
	b.WriteString("\n  " + bold + white + "LOG COMPRESSION" + reset + "\n")
	if before <= 0 {
		b.WriteString("  " + dim + "No eligible diagnostic blocks compressed yet" + reset + "\n")
		return b.String()
	}
	saved := before - after
	if saved < 0 {
		saved = 0
	}
	reduction := float64(saved) / float64(before)
	fmt.Fprintf(&b, "  Before %s   After %s   Saved %s   Reduction %s\n",
		format.HumanBytes(before), format.HumanBytes(after), green+format.HumanBytes(saved)+reset, green+format.Percent(reduction)+reset)
	fmt.Fprintf(&b, "  Blocks compressed %s   skipped %s\n", format.HumanCount(compressed), format.HumanCount(skipped))
	return b.String()
}

func visibleLines(s string) int { return strings.Count(s, "\n") }

func wrapLogLines(lines []string, width int) []string {
	input := make([]diagnosticLogLine, 0, len(lines))
	for _, line := range lines {
		input = append(input, diagnosticLogLine{Text: line})
	}
	wrapped := wrapDiagnosticLogLines(input, width)
	out := make([]string, 0, len(wrapped))
	for _, line := range wrapped {
		out = append(out, line.Text)
	}
	return out
}

func wrapDiagnosticLogLines(lines []diagnosticLogLine, width int) []diagnosticLogLine {
	if width < 1 {
		return nil
	}
	var wrapped []diagnosticLogLine
	for _, line := range lines {
		for text := range strings.SplitSeq(line.Text, "\n") {
			text = strings.ReplaceAll(strings.TrimSuffix(text, "\r"), "\t", "    ")
			for row := range strings.SplitSeq(ansi.Hardwrap(text, width, true), "\n") {
				wrapped = append(wrapped, diagnosticLogLine{Text: row, Level: line.Level})
			}
		}
	}
	return wrapped
}

func logLevelColor(level string) string {
	switch strings.ToUpper(level) {
	case "ERROR":
		return red + bold
	case "WARN", "WARNING":
		return yellow
	case "DEBUG":
		return dim
	default:
		return white
	}
}

func card(title, value, subtitle, color string) string {
	return fmt.Sprintf("┌────────────────────┐\n│ %-18s │\n│ %s%-18s%s │\n│ %-18s │\n└────────────────────┘", title, color+bold, format.PadRight(value, 18), reset, format.PadRight(subtitle, 18))
}
func cardRow(a, b, c string) string {
	lines, lb, lc := strings.Split(a, "\n"), strings.Split(b, "\n"), strings.Split(c, "\n")
	var out strings.Builder
	for i := range lines {
		out.WriteString("  " + lines[i] + "  " + lb[i] + "  " + lc[i] + "\n")
	}
	return out.String()
}
func line(label, value, color string) string {
	return fmt.Sprintf("%-20s %s%s%s", label, color+bold, value, reset)
}
func badge(label string, value int64, color string) string {
	return fmt.Sprintf("%s%s%s %s", color, label, reset, format.HumanCount(value))
}
func sColor(v int64) string {
	if v > 0 {
		return red
	}
	return green
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

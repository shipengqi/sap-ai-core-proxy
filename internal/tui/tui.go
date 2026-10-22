package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shipengqi/sap-ai-core-proxy/internal/tui/format"
	"github.com/shipengqi/sap-ai-core-proxy/internal/tui/stats"
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

// Run starts the display-only dashboard. It never reads terminal input.
// The caller owns cancellation; the dashboard deliberately does not subscribe
// to process-wide signals because it shares a process with the HTTP server.
func Run(ctx context.Context, base string, interval time.Duration, logs *LogBuffer) {
	fetcher := stats.New(base)
	first := true
	for {
		snap := fetcher.Fetch(ctx)
		width, height := terminalSize()
		if first {
			fmt.Print("\033[2J")
			first = false
		}
		// Clear the whole frame before repainting. Moving home and clearing only
		// below the final cursor left suffixes from longer previous log rows on
		// screen, visually splicing unrelated JSON records together.
		fmt.Print("\033[H\033[2J" + render(fetcher.Base(), snap, logs, width, height))
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			fmt.Print("\033[0m\n")
			return
		case <-t.C:
		}
	}
}

func render(base string, s stats.Snapshot, logs *LogBuffer, width, height int) string {
	var b strings.Builder
	status, statusColor := "HEALTHY", green
	if s.Down() {
		status, statusColor = "OFFLINE", red
	} else if !s.OK() {
		status, statusColor = "DEGRADED", yellow
	}
	b.WriteString("\n  " + bold + white + "SAP AI CORE PROXY" + reset + "  " + dim + "/  OBSERVABILITY" + reset + "\n")
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
	used := visibleLines(b.String())
	// Log chrome occupies five rows: spacer, title, spacer, pre-footer spacer,
	// and footer. Subtract it explicitly so visual breathing room never pushes
	// the frame beyond the terminal height.
	const logChromeRows = 5
	rows := height - used - logChromeRows
	if rows < 1 {
		rows = 1
	}
	b.WriteString("\n  " + bold + white + "DIAGNOSTIC LOGS" + reset + "  " + dim + "auto-follow · health/stats polls hidden" + reset + "\n\n")
	entries := []diagnosticLogLine(nil)
	if logs != nil {
		// Wrap complete records instead of truncating them. Taking a generous tail
		// before wrapping keeps the newest physical screen rows and makes the error
		// suffix (usually the useful part) visible without joining unrelated logs.
		entries = wrapDiagnosticLogLines(formatLogRecords(logs.Tail(500)), max(10, width-8))
		if len(entries) > rows {
			entries = entries[len(entries)-rows:]
		}
	}
	for i := 0; i < rows; i++ {
		entry := diagnosticLogLine{}
		if i < len(entries) {
			entry = entries[i]
		}
		b.WriteString("  " + logLevelColor(entry.Level) + entry.Text + reset + "\n")
	}
	b.WriteString("\n  " + dim + "Updated " + format.Clock(s.FetchedAt) + "  •  refreshes automatically  •  Ctrl-C to exit" + reset + "\n")
	return b.String()
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
		runes := []rune(strings.ReplaceAll(strings.TrimSuffix(line.Text, "\r"), "\t", "    "))
		if len(runes) == 0 {
			wrapped = append(wrapped, diagnosticLogLine{Level: line.Level})
			continue
		}
		for len(runes) > width {
			wrapped = append(wrapped, diagnosticLogLine{Text: string(runes[:width]), Level: line.Level})
			runes = runes[width:]
		}
		wrapped = append(wrapped, diagnosticLogLine{Text: string(runes), Level: line.Level})
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

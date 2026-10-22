// Package format renders counters, byte sizes and timestamps the way the
// dashboard needs them: short, fixed-vocabulary, and never wider than the
// column they sit in.
//
// Everything here is pure so the rendering rules can be unit tested without a
// terminal.
package format

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// unitBase is 1024: byte counters are binary units (KiB/MiB), which is what
	// everybody means when they say "1 MB of prompt".
	unitBase = 1024.0
	// ellipsis is the single-cell truncation marker.
	ellipsis = "…"
)

// HumanBytes renders n with a binary unit suffix.
//
// The dashboard shows compression numbers, and those are bytes rather than
// tokens because the proxy has no tokenizer. Keeping the unit visible in the
// string is what stops somebody reading "1.2" as 1.2 million tokens.
func HumanBytes(n int64) string {
	v := math.Abs(float64(n))
	sign := ""
	if n < 0 {
		sign = "-"
	}
	switch {
	case v < unitBase:
		return sign + strconv.FormatInt(int64(v), 10) + " B"
	case v < unitBase*unitBase:
		return fmt.Sprintf("%s%.1f KiB", sign, v/unitBase)
	case v < unitBase*unitBase*unitBase:
		return fmt.Sprintf("%s%.1f MiB", sign, v/(unitBase*unitBase))
	case v < unitBase*unitBase*unitBase*unitBase:
		return fmt.Sprintf("%s%.1f GiB", sign, v/(unitBase*unitBase*unitBase))
	default:
		return fmt.Sprintf("%s%.1f TiB", sign, v/(unitBase*unitBase*unitBase*unitBase))
	}
}

// HumanCount renders a request or token count with a k/M/B suffix.
//
// Exact digits matter only below 1000; above that the panel is read at a glance
// and "12.3k" is more useful than "12345".
func HumanCount(n int64) string {
	v := math.Abs(float64(n))
	sign := ""
	if n < 0 {
		sign = "-"
	}
	switch {
	case v < 1000:
		return sign + strconv.FormatInt(int64(v), 10)
	case v < 1e6:
		return fmt.Sprintf("%s%.1fk", sign, v/1000)
	case v < 1e9:
		return fmt.Sprintf("%s%.1fM", sign, v/1e6)
	default:
		return fmt.Sprintf("%s%.1fB", sign, v/1e9)
	}
}

// ExactCount renders a count with thousands separators. Used where the exact
// number is the answer (session counters, small tables).
func ExactCount(n int64) string {
	neg := n < 0
	v := strconv.FormatInt(abs(n), 10)
	var b strings.Builder
	for i, c := range v {
		if i > 0 && (len(v)-i)%3 == 0 {
			b.WriteString(",")
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// Percent renders a 0..1 ratio as a percentage.
//
// NaN, Inf and negatives render as "n/a": the server already clamps the ratio
// and returns 0 for an empty denominator, but a hand-edited or future response
// could still carry something odd, and "n/a" is honest where "NaN%" is not.
func Percent(r float64) string {
	if math.IsNaN(r) || math.IsInf(r, 0) || r < 0 {
		return "n/a"
	}
	// Use decimal half-up rounding rather than fmt's binary tie-to-even behavior.
	return fmt.Sprintf("%.1f%%", math.Floor(r*1000+0.5)/10)
}

// PercentOf renders a/b as a percentage, or "n/a" when there is no denominator.
func PercentOf(a, b float64) string {
	if b <= 0 {
		return "n/a"
	}
	return Percent(a / b)
}

// Ratio renders a/b as a 0..1 float with three decimals. Used for the
// compression reduction ratio where a percentage would be misleadingly precise.
func Ratio(a, b float64) string {
	if b <= 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.3f", a/b)
}

// Duration renders seconds as a compact elapsed time ("45s", "12m34s", "3h20m").
func Duration(seconds float64) string {
	if math.IsNaN(seconds) || seconds < 0 {
		seconds = 0
	}
	total := int64(seconds)
	switch {
	case total < 60:
		return fmt.Sprintf("%ds", total)
	case total < 3600:
		return fmt.Sprintf("%dm%02ds", total/60, total%60)
	case total < 86400:
		return fmt.Sprintf("%dh%02dm", total/3600, (total%3600)/60)
	default:
		return fmt.Sprintf("%dd%02dh", total/86400, (total%86400)/3600)
	}
}

// Ago renders how long ago something happened, coarse enough that it does not
// flicker between two values every frame.
func Ago(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int64(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int64(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int64(d.Hours()))
	}
}

// Clock renders the wall-clock time of a log line.
func Clock(t time.Time) string {
	if t.IsZero() {
		return "--:--:--"
	}
	return t.Format("15:04:05")
}

// Ellipsis shortens s to at most width cells, ending with "…" when it cut.
//
// Width is counted in runes. Model names and log messages are ASCII in practice,
// and libvterm-era width tables would be overkill for a panel that truncates to
// keep its columns aligned anyway.
func Ellipsis(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= width {
		return s
	}
	if width == 1 {
		return ellipsis
	}
	runes := []rune(s)
	return string(runes[:width-1]) + ellipsis
}

// PadRight pads s with spaces to width cells, truncating with an ellipsis when
// s is longer.
func PadRight(s string, width int) string {
	s = Ellipsis(s, width)
	return s + strings.Repeat(" ", width-utf8.RuneCountInString(s))
}

// PadLeft pads s with spaces on the left to width cells, truncating with an
// ellipsis when s is longer.
func PadLeft(s string, width int) string {
	s = Ellipsis(s, width)
	return strings.Repeat(" ", width-utf8.RuneCountInString(s)) + s
}

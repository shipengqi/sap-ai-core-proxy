package format

import (
	"math"
	"testing"
	"time"
)

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1 << 20, "1.0 MiB"},
		{3 << 20, "3.0 MiB"},
		{1 << 30, "1.0 GiB"},
		{1 << 40, "1.0 TiB"},
		{1 << 50, "1024.0 TiB"},
		{-2048, "-2.0 KiB"},
	}
	for _, c := range cases {
		if got := HumanBytes(c.in); got != c.want {
			t.Errorf("HumanBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHumanCount(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.0k"},
		{12345, "12.3k"},
		{999999, "1000.0k"},
		{1_000_000, "1.0M"},
		{1_234_567, "1.2M"},
		{2_000_000_000, "2.0B"},
		{-1500, "-1.5k"},
	}
	for _, c := range cases {
		if got := HumanCount(c.in); got != c.want {
			t.Errorf("HumanCount(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExactCount(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{123, "123"},
		{1234, "1,234"},
		{1234567, "1,234,567"},
		{-1234, "-1,234"},
	}
	for _, c := range cases {
		if got := ExactCount(c.in); got != c.want {
			t.Errorf("ExactCount(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPercent(t *testing.T) {
	if got := Percent(0.6325); got != "63.3%" { //nolint:gocritic // readable literal
		t.Errorf("Percent(0.6325) = %q, want 63.3%%", got)
	}
	if got := Percent(1); got != "100.0%" {
		t.Errorf("Percent(1) = %q, want 100.0%%", got)
	}
	for name, in := range map[string]float64{
		"nan":   math.NaN(),
		"+inf":  math.Inf(1),
		"-inf":  math.Inf(-1),
		"neg":   -0.5,
		"empty": 0,
	} {
		if name == "empty" {
			if got := Percent(in); got != "0.0%" {
				t.Errorf("Percent(0) = %q, want 0.0%%", got)
			}
			continue
		}
		if got := Percent(in); got != "n/a" {
			t.Errorf("Percent(%s) = %q, want n/a", name, got)
		}
	}
}

func TestPercentOfAndRatio(t *testing.T) {
	if got := PercentOf(5, 0); got != "n/a" {
		t.Errorf("PercentOf(5, 0) = %q, want n/a", got)
	}
	if got := PercentOf(1, 4); got != "25.0%" {
		t.Errorf("PercentOf(1, 4) = %q, want 25.0%%", got)
	}
	if got := Ratio(3, 4); got != "0.750" {
		t.Errorf("Ratio(3, 4) = %q, want 0.750", got)
	}
	if got := Ratio(3, 0); got != "n/a" {
		t.Errorf("Ratio(3, 0) = %q, want n/a", got)
	}
}

func TestDuration(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0s"},
		{45, "45s"},
		{60, "1m00s"},
		{754, "12m34s"},
		{3600, "1h00m"},
		{12000, "3h20m"},
		{86400, "1d00h"},
		{200000, "2d07h"},
		{-5, "0s"},
	}
	for _, c := range cases {
		if got := Duration(c.in); got != c.want {
			t.Errorf("Duration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAgo(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{2 * time.Second, "2s"},
		{90 * time.Second, "1m"},
		{3 * time.Hour, "3h"},
		{-time.Second, "0s"},
	}
	for _, c := range cases {
		if got := Ago(c.in); got != c.want {
			t.Errorf("Ago(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestClock(t *testing.T) {
	ts := time.Date(2026, 3, 4, 15, 4, 5, 0, time.UTC)
	if got := Clock(ts); got != "15:04:05" {
		t.Errorf("Clock = %q, want 15:04:05", got)
	}
	if got := Clock(time.Time{}); got != "--:--:--" {
		t.Errorf("Clock(zero) = %q, want --:--:--", got)
	}
}

func TestEllipsisAndPadding(t *testing.T) {
	if got := Ellipsis("hello", 10); got != "hello" {
		t.Errorf("Ellipsis(hello, 10) = %q", got)
	}
	if got := Ellipsis("hello world", 5); got != "hell…" {
		t.Errorf("Ellipsis(hello world, 5) = %q, want hell…", got)
	}
	if got := Ellipsis("hello", 1); got != "…" {
		t.Errorf("Ellipsis(hello, 1) = %q, want …", got)
	}
	if got := Ellipsis("hello", 0); got != "" {
		t.Errorf("Ellipsis(hello, 0) = %q, want empty", got)
	}
	if got := PadLeft("7", 4); got != "   7" {
		t.Errorf("PadLeft(7, 4) = %q", got)
	}
	if got := PadRight("ab", 5); got != "ab   " {
		t.Errorf("PadRight(ab, 5) = %q", got)
	}
	// Over-long input must still land on exactly the requested width.
	if got := PadRight("abcdefghij", 5); got != "abcd…" {
		t.Errorf("PadRight(abcdefghij, 5) = %q, want abcd…", got)
	}
}

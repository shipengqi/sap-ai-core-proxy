package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/shipengqi/sap-ai-core-proxy/internal/tui/stats"
)

func TestQAPausedWindowEndAndResize(t *testing.T) {
	logs := NewLogBuffer(500)
	for i := range 90 {
		_, _ = fmt.Fprintf(logs, "qa-%03d-%s\n", i, strings.Repeat("x", 100))
	}
	m := dashboardModel{logs: logs, width: 80, height: 48}
	update := func(msg tea.Msg) { next, _ := m.Update(msg); m = next.(dashboardModel) }
	update(snapshotMsg(stats.Snapshot{}))
	update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	frozen := append([]diagnosticLogLine(nil), m.lines...)
	offset := m.offset
	for i := 90; i < 900; i++ {
		_, _ = fmt.Fprintf(logs, "qa-%03d-%s\n", i, strings.Repeat("y", 100))
	}
	update(snapshotMsg(stats.Snapshot{}))
	if !reflect.DeepEqual(m.lines, frozen) || m.offset != offset {
		t.Fatal("paused log snapshot or physical-row offset moved under ring eviction")
	}
	update(tea.WindowSizeMsg{Width: 120, Height: 60})
	if !m.paused || !reflect.DeepEqual(m.lines, frozen) {
		t.Fatal("resize lost frozen history")
	}
	update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if m.paused || m.offset != 0 || len(m.lines) != 500 {
		t.Fatalf("End: paused=%v offset=%d records=%d", m.paused, m.offset, len(m.lines))
	}
	if got := m.lines[len(m.lines)-1].Text; got != "qa-899-"+strings.Repeat("y", 100) {
		t.Fatalf("End did not capture the complete latest record: %q", got)
	}
	wrapped := wrapDiagnosticLogLines(m.lines, m.width-8)
	_, rows := renderLogFrame(m.base, m.snap, m.lines, m.width, m.height, 0, false)
	want := wrapped[len(wrapped)-rows:]
	frame := ansi.Strip(m.View().Content)
	for _, row := range want {
		if !strings.Contains(frame, row.Text) {
			t.Fatalf("latest End window omits complete physical row %q", row.Text)
		}
	}
}

func TestQAEmptyAndLongLogNavigation(t *testing.T) {
	for _, text := range []string{"", strings.Repeat("x", 20000)} {
		m := dashboardModel{ready: true, width: 80, height: 48}
		if text != "" {
			m.lines = []diagnosticLogLine{{Text: text}}
		}
		for _, key := range []rune{tea.KeyDown, tea.KeyPgDown, tea.KeyUp, tea.KeyPgUp, tea.KeyEnd} {
			next, _ := m.Update(tea.KeyPressMsg{Code: key})
			m = next.(dashboardModel)
			_, rows := renderLogFrame(m.base, m.snap, m.lines, m.width, m.height, m.offset, m.paused)
			if m.offset < 0 || m.offset > max(0, len(wrapDiagnosticLogLines(m.lines, m.width-8))-rows) {
				t.Fatalf("key %d produced invalid physical-row offset %d", key, m.offset)
			}
		}
	}
}

func TestQAUnicodeLogRowsUseTerminalCells(t *testing.T) {
	for _, text := range []string{strings.Repeat("界", 100), strings.Repeat("e\u0301", 100)} {
		rows := wrapDiagnosticLogLines([]diagnosticLogLine{{Text: text}}, 20)
		var rebuilt strings.Builder
		for _, row := range rows {
			if width := ansi.StringWidth(row.Text); width > 20 {
				t.Errorf("wrapped physical row occupies %d terminal cells, budget 20", width)
			}
			rebuilt.WriteString(row.Text)
		}
		if rebuilt.String() != text {
			t.Fatal("wrapping changed log content")
		}
	}
}

func TestQAFramePhysicalGeometry(t *testing.T) {
	for _, size := range [][2]int{{120, 48}, {80, 48}, {80, 24}, {40, 48}, {20, 12}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			frame, _ := renderLogFrame("http://localhost:31234", stats.Snapshot{}, nil, size[0], size[1], 0, false)
			physical := 0
			for _, line := range strings.Split(strings.TrimSuffix(ansi.Strip(frame), "\n"), "\n") {
				physical += max(1, (ansi.StringWidth(line)+size[0]-1)/size[0])
			}
			if physical > size[1] {
				t.Fatalf("frame consumes %d physical rows in %d-row terminal (%d newline rows)", physical, size[1], visibleLines(frame))
			}
		})
	}
}

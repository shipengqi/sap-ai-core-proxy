package tui

import (
	"bytes"
	"sync"
)

// LogBuffer is a bounded, concurrency-safe line buffer used by the dashboard.
type LogBuffer struct {
	mu    sync.Mutex
	lines []string
	cap   int
	carry []byte
}

func NewLogBuffer(capacity int) *LogBuffer {
	if capacity < 1 {
		capacity = 1
	}
	return &LogBuffer{cap: capacity}
}

func (b *LogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	data := append(b.carry, p...)
	parts := bytes.Split(data, []byte{'\n'})
	b.carry = append(b.carry[:0], parts[len(parts)-1]...)
	for _, part := range parts[:len(parts)-1] {
		b.lines = append(b.lines, string(part))
	}
	if len(b.lines) > b.cap {
		b.lines = append([]string(nil), b.lines[len(b.lines)-b.cap:]...)
	}
	return n, nil
}

func (b *LogBuffer) Tail(n int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n <= 0 || len(b.lines) == 0 {
		return nil
	}
	start := len(b.lines) - n
	if start < 0 {
		start = 0
	}
	return append([]string(nil), b.lines[start:]...)
}

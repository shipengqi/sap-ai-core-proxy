package tui

import (
	"strings"
	"testing"
)

func TestLogBufferKeepsLatestLines(t *testing.T) {
	b := NewLogBuffer(3)
	_, _ = b.Write([]byte("one\ntwo\n"))
	_, _ = b.Write([]byte("three\nfour\n"))
	got := b.Tail(10)
	want := []string{"two", "three", "four"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Tail = %v, want %v", got, want)
	}
}

func TestLogBufferHandlesPartialWrites(t *testing.T) {
	b := NewLogBuffer(5)
	_, _ = b.Write([]byte("partial"))
	if got := b.Tail(5); len(got) != 0 {
		t.Fatalf("unexpected incomplete line: %v", got)
	}
	_, _ = b.Write([]byte(" line\n"))
	if got := b.Tail(5); len(got) != 1 || got[0] != "partial line" {
		t.Fatalf("Tail = %v", got)
	}
}

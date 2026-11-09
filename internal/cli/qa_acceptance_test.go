package cli

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestQAServerCancelledBeforeStartup(t *testing.T) {
	for range 100 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		server := &http.Server{Addr: "127.0.0.1:0", ReadHeaderTimeout: time.Second}
		done := make(chan error, 1)
		go func() { done <- runHTTPServer(ctx, cancel, server, time.Second, nil) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			_ = server.Close()
			t.Fatal("pre-start cancellation left server running")
		}
	}
}

func TestQAServerCancelPreservesPanelFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	want := errors.Join(tea.ErrProgramKilled, tea.ErrProgramPanic)
	panel := func() error { cancel(); return want }
	server := &http.Server{Addr: "127.0.0.1:0", ReadHeaderTimeout: time.Second}
	got := runHTTPServer(ctx, cancel, server, time.Second, panel)
	if !errors.Is(got, tea.ErrProgramPanic) {
		t.Fatalf("cancel plus panel panic swallowed: %v", got)
	}
}

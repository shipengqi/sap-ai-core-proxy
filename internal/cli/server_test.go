package cli

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestHTTPServerCancellationBeforeListen(t *testing.T) {
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
			t.Fatal("pre-listen cancellation left a server running")
		}
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("shutdown did not prevent subsequent ListenAndServe: %v", err)
		}
	}
}

func TestHTTPServerCancellationWaitsForPanel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	panelStopping := make(chan struct{})
	allowRestore := make(chan struct{})
	server := &http.Server{Addr: "127.0.0.1:0", ReadHeaderTimeout: time.Second}
	panel := func() error {
		cancel()
		close(panelStopping)
		<-allowRestore
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- runHTTPServer(ctx, cancel, server, time.Second, panel) }()
	<-panelStopping
	select {
	case err := <-done:
		close(allowRestore)
		t.Fatalf("server returned before panel restoration: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(allowRestore)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not finish after terminal restoration")
	}
}

func TestHTTPServerBindFailureCancelsPanel(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	panelRestored := false
	panel := func() error {
		<-ctx.Done()
		panelRestored = true
		return nil
	}
	server := &http.Server{Addr: listener.Addr().String(), ReadHeaderTimeout: time.Second}
	if err := runHTTPServer(ctx, cancel, server, time.Second, panel); err == nil {
		t.Fatal("bind failure was swallowed")
	}
	if !panelRestored {
		t.Fatal("bind failure did not wait for panel")
	}
}

func TestHTTPServerPreservesPanelError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	want := errors.New("panel output failed")
	server := &http.Server{Addr: "127.0.0.1:0", ReadHeaderTimeout: time.Second}
	if err := runHTTPServer(ctx, cancel, server, time.Second, func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("got %v, want panel error", err)
	}
}

func TestHTTPServerActiveRequestShutdown(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "graceful", true: "deadline"}[force], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan struct{})
			release := make(chan struct{})
			finished := make(chan struct{})
			server := &http.Server{ReadHeaderTimeout: time.Second}
			server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				defer close(finished)
				select {
				case <-release:
					_, _ = io.WriteString(w, "finished")
				case <-r.Context().Done():
				}
			})
			clientDone := make(chan struct{})
			server.Addr = "127.0.0.1:0"
			serverReady := make(chan string, 1)
			// Capture the actual listener address before serving with port zero.
			server.BaseContext = func(listener net.Listener) context.Context {
				serverReady <- listener.Addr().String()
				return context.Background()
			}
			go func() {
				defer close(clientDone)
				address := <-serverReady
				response, err := http.Get("http://" + address)
				if err == nil {
					_, _ = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
				}
			}()
			panel := func() error {
				<-started
				cancel()
				if !force {
					close(release)
				}
				return nil
			}
			before := time.Now()
			err := runHTTPServer(ctx, cancel, server, 30*time.Millisecond, panel)
			if force && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("forced shutdown returned %v", err)
			}
			if !force && err != nil {
				t.Fatal(err)
			}
			if time.Since(before) > time.Second {
				t.Fatal("shutdown exceeded its bound")
			}
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("request did not stop")
			}
			<-clientDone
		})
	}
}

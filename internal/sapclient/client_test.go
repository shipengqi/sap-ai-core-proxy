package sapclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsTransientNetworkErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil",
			err:  nil,
			want: false,
		},
		{
			name: "context canceled",
			err:  context.Canceled,
			want: false,
		},
		{
			name: "context deadline exceeded",
			err:  context.DeadlineExceeded,
			want: false,
		},
		{
			name: "dns timeout",
			err:  &net.DNSError{IsTimeout: true},
			want: true,
		},
		{
			name: "dns temporary",
			err:  &net.DNSError{IsTemporary: true},
			want: true,
		},
		{
			name: "dns permission denied — the exact production bug",
			err:  &net.DNSError{Err: "write udp 128.128.0.3:53444 -> 192.168.31.53: write: permission denied"},
			want: true,
		},
		{
			name: "dns operation not permitted",
			err:  &net.DNSError{Err: "operation not permitted"},
			want: true,
		},
		{
			name: "dns server misbehaving (not temporary, not a known string)",
			err:  &net.DNSError{Err: "server misbehaving"},
			want: false,
		},
		{
			name: "non-dns error",
			err:  net.ErrClosed,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isTransientNetworkErr(tc.err)
			if got != tc.want {
				t.Errorf("isTransientNetworkErr(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestDo_401RefreshAndRetry verifies that a 401 from upstream causes the client to
// invalidate its cached token and retry exactly once, returning the second response.
func TestDo_401RefreshAndRetry(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"error":"Token Is Expired"}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	tokenCallCount := 0
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCallCount++
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":3600}`, tokenCallCount)
	}))
	defer tokenSrv.Close()

	auth := NewAuthManager(tokenSrv.URL, "id", "secret")
	client := NewClient(srv.URL, "default", auth)

	resp, err := client.Do(context.Background(), http.MethodPost, srv.URL, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 after retry, got %d", resp.StatusCode)
	}
	if callCount != 2 {
		t.Errorf("expected 2 upstream calls (401 + retry), got %d", callCount)
	}
	if tokenCallCount != 2 {
		t.Errorf("expected 2 token fetches (initial + refresh), got %d", tokenCallCount)
	}
}

// TestDo_401NoInfiniteLoop verifies that if upstream keeps returning 401, the client
// returns the 401 to the caller after one retry (no infinite loop).
func TestDo_401NoInfiniteLoop(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":"Token Is Expired"}`)
	}))
	defer srv.Close()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
	}))
	defer tokenSrv.Close()

	auth := NewAuthManager(tokenSrv.URL, "id", "secret")
	client := NewClient(srv.URL, "default", auth)

	resp, err := client.Do(context.Background(), http.MethodPost, srv.URL, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 after exhausted retries, got %d", resp.StatusCode)
	}
	if callCount != 2 {
		t.Errorf("expected exactly 2 upstream calls, got %d", callCount)
	}
}

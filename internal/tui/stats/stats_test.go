package stats

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// The two dashboards used to ship separate pollers: the in-process one knows
// its own build and never calls /version, while the standalone one needs the
// remote build identity. Unifying them must not silently make the in-process
// panel issue an extra request on every two-second refresh.
func TestVersionPollingIsOptIn(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == EndpointVersion {
			_, _ = w.Write([]byte(`{"version":"9.9.9"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	plain := New(srv.URL).Fetch(context.Background())
	withVersion := NewWithVersion(srv.URL).Fetch(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if got := hits[EndpointVersion]; got != 1 {
		t.Errorf("GET /version hit %d times, want 1 (NewWithVersion only)", got)
	}
	if plain.Version.Version != "" {
		t.Errorf("New populated Version %q, want empty", plain.Version.Version)
	}
	if withVersion.Version.Version != "9.9.9" {
		t.Errorf("NewWithVersion Version = %q, want 9.9.9", withVersion.Version.Version)
	}
	if !withVersion.OK() || !plain.OK() {
		t.Errorf("version polling must not affect health: plain=%v withVersion=%v", plain.Errors, withVersion.Errors)
	}
	for _, path := range EndpointAll {
		if hits[path] != 2 {
			t.Errorf("%s hit %d times, want 2 (one per fetcher)", path, hits[path])
		}
	}
}

// A proxy built before GET /version existed must still render as healthy.
func TestMissingVersionDoesNotDegrade(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == EndpointVersion {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	snap := NewWithVersion(srv.URL).Fetch(context.Background())
	if !snap.OK() {
		t.Errorf("a failing /version must not mark the proxy degraded: %v", snap.Errors)
	}
	if snap.Version.Version != "" {
		t.Errorf("expected empty Version, got %q", snap.Version.Version)
	}
}

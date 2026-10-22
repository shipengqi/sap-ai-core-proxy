package test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shipengqi/sap-ai-core-proxy/internal/version"
	"github.com/shipengqi/sap-ai-core-proxy/test/testutil"
)

// TestVersionEndpoint pins the contract that deploy scripts and both
// dashboards rely on: a plain unauthenticated GET whose body names the build.
func TestVersionEndpoint(t *testing.T) {
	mock := testutil.NewMockSAPServer(t)
	defer mock.Close()
	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer proxy.Close()

	resp, err := http.Get(proxy.URL + "/version")
	if err != nil {
		t.Fatalf("GET /version: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /version status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got version.Info
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode /version: %v", err)
	}
	if got != version.Get() {
		t.Fatalf("/version body = %+v, want the compiled-in %+v", got, version.Get())
	}
	if got.Version == "" || got.Commit == "" || got.Platform == "" {
		t.Fatalf("/version reported an empty build identity: %+v", got)
	}
}

// TestVersionEndpointDoesNotPolluteUsage guards the accounting invariant: every
// inference route is a POST, and observation routes must stay out of the
// counters. A dashboard polling /version every two seconds would otherwise
// inflate requests_total without any tokens to match.
func TestVersionEndpointDoesNotPolluteUsage(t *testing.T) {
	mock := testutil.NewMockSAPServer(t)
	defer mock.Close()
	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer proxy.Close()

	for i := 0; i < 3; i++ {
		resp, err := http.Get(proxy.URL + "/version")
		if err != nil {
			t.Fatalf("GET /version: %v", err)
		}
		_ = resp.Body.Close()
	}

	resp, err := http.Get(proxy.URL + "/stats")
	if err != nil {
		t.Fatalf("GET /stats: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var stats struct {
		Totals struct {
			Requests int64 `json:"requests"`
		} `json:"totals"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		t.Fatalf("decode /stats: %v", err)
	}
	if stats.Totals.Requests != 0 {
		t.Fatalf("requests = %d after three GET /version calls, want 0", stats.Totals.Requests)
	}
}

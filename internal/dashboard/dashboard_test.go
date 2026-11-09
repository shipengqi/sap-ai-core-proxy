package dashboard

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The standalone dashboard talks to a proxy it did not build, so it must ask
// GET /version for the header. If it accidentally switches back to the plain
// stats.New, the version silently becomes "unknown" and this goes red.
func TestPlainDashboardReportsRemoteVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/version" {
			_, _ = w.Write([]byte(`{"version":"9.9.9"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var out bytes.Buffer
	if err := Run(context.Background(), &out, Options{URL: srv.URL, Plain: true}); err != nil {
		t.Fatalf("plain dashboard failed: %v", err)
	}
	if !strings.Contains(out.String(), "9.9.9") {
		t.Errorf("header is missing the remote build version:\n%s", out.String())
	}
	if strings.Contains(out.String(), "unknown") {
		t.Errorf("header fell back to unknown, /version was not polled:\n%s", out.String())
	}
}

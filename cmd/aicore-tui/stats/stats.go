// Package stats polls the proxy's read-only observation endpoints and its
// build identity, and turns the counters into the handful of conclusions the
// dashboard shows.
//
// The response payload types are the ones the server marshals (internal/usage,
// internal/diagnostics, internal/compress), so this panel cannot drift from the
// wire shape. Only the three envelopes are declared here, because the server
// keeps those unexported; the fields inside them are not ours to reinvent.
package stats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shipengqi/sap-ai-core-proxy/internal/compress"
	"github.com/shipengqi/sap-ai-core-proxy/internal/diagnostics"
	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
	"github.com/shipengqi/sap-ai-core-proxy/internal/version"
)

// Endpoint paths. Exported so the UI can merge a partial failure without
// comparing magic strings.
const (
	EndpointStats    = "/stats"
	EndpointCache    = "/stats/cache"
	EndpointCompress = "/stats/compress"
	EndpointVersion  = "/version"
)

// EndpointAll is every endpoint whose failure counts towards health. The UI
// uses its length to tell "one endpoint is flaky" apart from "the proxy is
// gone". EndpointVersion is polled as well but stays out of this list: a proxy
// that predates the route must not be reported as degraded.
var EndpointAll = []string{EndpointStats, EndpointCache, EndpointCompress}

const (
	// requestTimeout bounds a poll. The panel refreshes every two seconds by
	// default, so a hung endpoint must not stall the UI for longer than that.
	requestTimeout = 3 * time.Second
	// maxBody caps a response. The endpoints are small; a hostile or broken
	// server must not be able to make the panel allocate without bound.
	maxBody = 8 << 20
)

// ErrNoEndpoint is returned when no base URL was configured.
var ErrNoEndpoint = errors.New("no endpoint configured")

// StatsResponse is the body of GET /stats.
type StatsResponse struct {
	Since         string            `json:"since"`
	UptimeSeconds float64           `json:"uptime_seconds"`
	Totals        usage.Aggregate   `json:"totals"`
	BySurface     []usage.Breakdown `json:"by_surface"`
	ByModel       []usage.Breakdown `json:"by_model"`
}

// CacheResponse is the body of GET /stats/cache.
type CacheResponse struct {
	Since           string                       `json:"since"`
	UptimeSeconds   float64                      `json:"uptime_seconds"`
	TrackedSessions int                          `json:"tracked_sessions"`
	ByResult        []diagnostics.ResultSeries   `json:"by_result"`
	ByOutcome       []diagnostics.OutcomeSeries  `json:"by_outcome"`
	Volatile        []diagnostics.VolatileSeries `json:"volatile"`
	Skipped         []diagnostics.SkippedSeries  `json:"skipped"`
	NoMarker        []diagnostics.NoMarkerSeries `json:"no_marker"`
	TopSessions     []diagnostics.SessionSeries  `json:"top_sessions"`
}

// CompressResponse is the body of GET /stats/compress. Every byte figure in it
// is bytes, not tokens: the proxy has no tokenizer.
type CompressResponse struct {
	Since         string                   `json:"since"`
	UptimeSeconds float64                  `json:"uptime_seconds"`
	Blocks        []compress.BlockSeries   `json:"blocks"`
	Bytes         []compress.BytesSeries   `json:"bytes"`
	Skipped       []compress.SkippedSeries `json:"skipped"`
}

// EndpointError is one endpoint that failed during a poll.
type EndpointError struct {
	Endpoint string
	Err      error
}

// Error implements error.
func (e EndpointError) Error() string { return e.Endpoint + ": " + e.Err.Error() }

// Unwrap exposes the underlying error to errors.Is.
func (e EndpointError) Unwrap() error { return e.Err }

// Snapshot is the result of one poll of every endpoint.
//
// A Snapshot is always returned, even when everything failed: the panel keeps
// rendering (with the last good numbers) instead of branching on an error.
type Snapshot struct {
	FetchedAt time.Time
	Stats     StatsResponse
	Cache     CacheResponse
	Compress  CompressResponse
	// Version is the build identity reported by the proxy. It stays zero when
	// the proxy predates GET /version, which is why it is not part of Errors.
	Version version.Info
	// Errors holds one entry per endpoint that failed. Empty means all three
	// answered.
	Errors []EndpointError
}

// OK reports whether every endpoint answered.
func (s Snapshot) OK() bool { return len(s.Errors) == 0 }

// Down reports whether every endpoint failed, which is what "the proxy is not
// reachable" means. One or two failures is degradation, not an outage.
func (s Snapshot) Down() bool { return len(s.Errors) >= len(EndpointAll) }

// Failed reports whether the named endpoint failed in this poll.
func (s Snapshot) Failed(endpoint string) bool {
	for _, e := range s.Errors {
		if e.Endpoint == endpoint {
			return true
		}
	}
	return false
}

// ErrorText renders the failures for the header line.
func (s Snapshot) ErrorText() string {
	if len(s.Errors) == 0 {
		return ""
	}
	parts := make([]string, 0, len(s.Errors))
	for _, e := range s.Errors {
		parts = append(parts, e.Endpoint+": "+e.Err.Error())
	}
	return strings.Join(parts, " · ")
}

// Fetcher polls the endpoints. It is safe for concurrent use and holds no state
// beyond its configuration.
type Fetcher struct {
	base   string
	client *http.Client
}

// New returns a Fetcher for base, e.g. "http://localhost:3001". A trailing
// slash is tolerated; an empty base yields ErrNoEndpoint on every poll.
func New(base string) *Fetcher {
	return &Fetcher{
		base:   strings.TrimRight(base, "/"),
		client: &http.Client{Timeout: requestTimeout},
	}
}

// Base returns the configured base URL.
func (f *Fetcher) Base() string { return f.base }

// Fetch polls every endpoint. It never returns an error: partial results are a
// normal outcome and belong in Snapshot.Errors so the UI can degrade
// per-endpoint instead of blanking the whole panel.
func (f *Fetcher) Fetch(ctx context.Context) Snapshot {
	snap := Snapshot{FetchedAt: time.Now()}

	if f.base == "" {
		snap.Errors = append(snap.Errors, EndpointError{Endpoint: "-", Err: ErrNoEndpoint})
		return snap
	}

	if err := f.get(ctx, EndpointStats, &snap.Stats); err != nil {
		snap.Errors = append(snap.Errors, EndpointError{Endpoint: EndpointStats, Err: err})
	}
	if err := f.get(ctx, EndpointCache, &snap.Cache); err != nil {
		snap.Errors = append(snap.Errors, EndpointError{Endpoint: EndpointCache, Err: err})
	}
	if err := f.get(ctx, EndpointCompress, &snap.Compress); err != nil {
		snap.Errors = append(snap.Errors, EndpointError{Endpoint: EndpointCompress, Err: err})
	}
	// Best effort, and deliberately not recorded in Errors: the build identity
	// is decoration, not health. A proxy built before /version existed must
	// still render as healthy rather than DEGRADED, so a failure here is
	// dropped and the header simply omits the version.
	_ = f.get(ctx, EndpointVersion, &snap.Version)
	return snap
}

// get performs one GET and decodes the body into dst.
func (f *Fetcher) get(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// Drain a little so the connection can be reused, and never more than a
		// line of text we are about to throw away anyway.
		_, _ = io.CopyN(io.Discard, resp.Body, 512)
		return fmt.Errorf("HTTP %s", resp.Status)
	}

	dec := json.NewDecoder(io.LimitReader(resp.Body, maxBody))
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

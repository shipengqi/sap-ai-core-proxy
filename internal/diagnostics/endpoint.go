package diagnostics

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// statsResponse is the JSON body of GET /stats/cache. It is declared explicitly
// so the wire shape stays stable when the internal snapshot grows.
type statsResponse struct {
	Since           string           `json:"since"`
	UptimeSeconds   float64          `json:"uptime_seconds"`
	TrackedSessions int              `json:"tracked_sessions"`
	ByResult        []ResultSeries   `json:"by_result"`
	ByOutcome       []OutcomeSeries  `json:"by_outcome"`
	Volatile        []VolatileSeries `json:"volatile"`
	Skipped         []SkippedSeries  `json:"skipped"`
	TopSessions     []SessionSeries  `json:"top_sessions"`
}

// CacheStatsHandler serves GET /stats/cache.
func (c *Collector) CacheStatsHandler(ctx *gin.Context) {
	snap := c.Snapshot()

	// Marshal empty lists rather than null so consumers can iterate blindly.
	if snap.ByResult == nil {
		snap.ByResult = []ResultSeries{}
	}
	if snap.ByOutcome == nil {
		snap.ByOutcome = []OutcomeSeries{}
	}
	if snap.Volatile == nil {
		snap.Volatile = []VolatileSeries{}
	}
	if snap.Skipped == nil {
		snap.Skipped = []SkippedSeries{}
	}
	if snap.TopSessions == nil {
		snap.TopSessions = []SessionSeries{}
	}

	ctx.JSON(http.StatusOK, statsResponse{
		Since:           snap.Since.UTC().Format("2006-01-02T15:04:05Z07:00"),
		UptimeSeconds:   snap.UptimeSeconds,
		TrackedSessions: snap.Sessions,
		ByResult:        snap.ByResult,
		ByOutcome:       snap.ByOutcome,
		Volatile:        snap.Volatile,
		Skipped:         snap.Skipped,
		TopSessions:     snap.TopSessions,
	})
}

// MetricsText renders the Prometheus families this package owns. The router
// concatenates it with the usage package's payload so /metrics stays a single
// endpoint.
func (c *Collector) MetricsText() string {
	return c.Snapshot().Prometheus()
}

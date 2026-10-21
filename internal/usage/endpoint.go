package usage

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// statsResponse is the JSON body of GET /stats. It is defined explicitly rather
// than reusing Snapshot so the wire shape stays stable when the internal
// snapshot grows (the Prometheus-only series are deliberately absent).
type statsResponse struct {
	Since         string      `json:"since"`
	UptimeSeconds float64     `json:"uptime_seconds"`
	Totals        Aggregate   `json:"totals"`
	BySurface     []Breakdown `json:"by_surface"`
	ByModel       []Breakdown `json:"by_model"`
}

// MetricsHandler serves GET /metrics in the Prometheus text exposition format.
func (c *Collector) MetricsHandler(ctx *gin.Context) {
	ctx.Data(http.StatusOK, metricsContentType, []byte(c.Snapshot().Prometheus()))
}

// StatsHandler serves GET /stats as JSON, ordered by total tokens descending.
//
// by_model is truncated to the top entries because a proxy that has served many
// model names would otherwise return an unusable payload; /metrics keeps the
// complete set for time series.
func (c *Collector) StatsHandler(ctx *gin.Context) {
	snap := c.Snapshot()

	byModel := snap.ByModel
	if len(byModel) > maxModelBreakdowns {
		byModel = byModel[:maxModelBreakdowns]
	}
	if byModel == nil {
		// Marshal an empty list rather than null so consumers can iterate blindly.
		byModel = []Breakdown{}
	}
	bySurface := snap.BySurface
	if bySurface == nil {
		bySurface = []Breakdown{}
	}

	ctx.JSON(http.StatusOK, statsResponse{
		Since:         snap.Since.UTC().Format("2006-01-02T15:04:05Z07:00"),
		UptimeSeconds: snap.UptimeSeconds,
		Totals:        snap.Totals,
		BySurface:     bySurface,
		ByModel:       byModel,
	})
}

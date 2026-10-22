package compress

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Results of sap_proxy_compress_blocks_total.
const (
	resultCompressed = "compressed"
	resultSkipped    = "skipped"
)

// labelEscaper matches the usage and diagnostics packages byte for byte. The
// exposition format defines exactly three escapes — backslash, double quote and
// line feed — and a parser rejects anything else, so \r and \t are emitted raw
// (they are legal inside a label value).
var labelEscaper = strings.NewReplacer(
	`\`, `\\`,
	`"`, `\"`,
	"\n", `\n`,
)

type blockKey struct {
	surface string
	result  string
}

type skipKey struct {
	surface string
	reason  string
}

// Collector aggregates compression counters for the lifetime of the process.
type Collector struct {
	mu      sync.Mutex
	since   time.Time
	blocks  map[blockKey]int64
	before  map[string]int64
	after   map[string]int64
	skipped map[skipKey]int64
}

// NewCollector returns an empty collector whose measurement window starts now.
func NewCollector() *Collector {
	return &Collector{
		since:   time.Now(),
		blocks:  make(map[blockKey]int64),
		before:  make(map[string]int64),
		after:   make(map[string]int64),
		skipped: make(map[skipKey]int64),
	}
}

// defaultCollector is the process-wide collector the handlers report into and
// the /metrics and /stats/compress endpoints read.
var defaultCollector = NewCollector()

// Observe records one request's report into the process-wide collector.
//
// A report that considered no block is ignored: nothing was eligible, so there
// is no before/after pair and no reason to publish an empty series.
func Observe(surface string, r Report) {
	defaultCollector.Observe(surface, r)
}

// MetricsText renders the families this package owns. The router concatenates it
// with the usage and diagnostics payloads so /metrics stays one endpoint.
func MetricsText() string {
	return defaultCollector.MetricsText()
}

// StatsHandler serves GET /stats/compress.
func StatsHandler(c *gin.Context) {
	defaultCollector.StatsHandler(c)
}

// Observe records one request's report.
func (c *Collector) Observe(surface string, r Report) {
	if surface == "" || (r.BlocksConsidered == 0 && r.Skipped == "") {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if r.BlocksCompressed > 0 {
		c.blocks[blockKey{surface, resultCompressed}] += int64(r.BlocksCompressed)
	}
	if skipped := r.BlocksConsidered - r.BlocksCompressed; skipped > 0 {
		c.blocks[blockKey{surface, resultSkipped}] += int64(skipped)
	}
	if r.OriginalBytes > 0 {
		c.before[surface] += int64(r.OriginalBytes)
	}
	if r.CompressedBytes > 0 {
		c.after[surface] += int64(r.CompressedBytes)
	}
	// Skipped is only set when nothing was compressed, so this is one counter
	// per request rather than one per block.
	if r.Skipped != "" {
		c.skipped[skipKey{surface, r.Skipped}]++
	}
}

// BlockSeries is one (surface, result) block counter.
type BlockSeries struct {
	Surface string `json:"surface"`
	Result  string `json:"result"`
	Count   int64  `json:"count"`
}

// BytesSeries is the before/after byte totals of one surface.
type BytesSeries struct {
	Surface     string `json:"surface"`
	BytesBefore int64  `json:"bytes_before"`
	BytesAfter  int64  `json:"bytes_after"`
}

// SkippedSeries is one (surface, reason) counter.
type SkippedSeries struct {
	Surface string `json:"surface"`
	Reason  string `json:"reason"`
	Count   int64  `json:"count"`
}

// Snapshot is an immutable view backing both endpoints.
type Snapshot struct {
	Since         time.Time       `json:"-"`
	UptimeSeconds float64         `json:"uptime_seconds"`
	Blocks        []BlockSeries   `json:"blocks"`
	Bytes         []BytesSeries   `json:"bytes"`
	Skipped       []SkippedSeries `json:"skipped"`
}

// Snapshot returns a consistent copy of all counters.
func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	snap := Snapshot{
		Since:         c.since,
		UptimeSeconds: time.Since(c.since).Seconds(),
	}

	blocks := make([]BlockSeries, 0, len(c.blocks))
	for k, n := range c.blocks {
		blocks = append(blocks, BlockSeries{Surface: k.surface, Result: k.result, Count: n})
	}
	sort.Slice(blocks, func(i, j int) bool {
		if blocks[i].Surface != blocks[j].Surface {
			return blocks[i].Surface < blocks[j].Surface
		}
		return blocks[i].Result < blocks[j].Result
	})
	snap.Blocks = blocks

	surfaceSet := make(map[string]bool, len(c.before)+len(c.after))
	for s := range c.before {
		surfaceSet[s] = true
	}
	for s := range c.after {
		surfaceSet[s] = true
	}
	surfaces := make([]string, 0, len(surfaceSet))
	for s := range surfaceSet {
		surfaces = append(surfaces, s)
	}
	sort.Strings(surfaces)
	for _, s := range surfaces {
		snap.Bytes = append(snap.Bytes, BytesSeries{
			Surface:     s,
			BytesBefore: c.before[s],
			BytesAfter:  c.after[s],
		})
	}

	skipped := make([]SkippedSeries, 0, len(c.skipped))
	for k, n := range c.skipped {
		skipped = append(skipped, SkippedSeries{Surface: k.surface, Reason: k.reason, Count: n})
	}
	sort.Slice(skipped, func(i, j int) bool {
		if skipped[i].Surface != skipped[j].Surface {
			return skipped[i].Surface < skipped[j].Surface
		}
		return skipped[i].Reason < skipped[j].Reason
	})
	snap.Skipped = skipped

	return snap
}

// MetricsText renders the snapshot in the Prometheus text exposition format.
//
// The counters are bytes, not tokens: this proxy has no tokenizer, and naming
// them anything else would be a lie that somebody would size a dashboard on.
func (c *Collector) MetricsText() string {
	s := c.Snapshot()

	// Nothing observed yet: emit no families at all rather than four empty
	// ones, so a proxy that never compressed keeps its /metrics payload as it
	// was before this feature existed.
	if len(s.Blocks) == 0 && len(s.Bytes) == 0 && len(s.Skipped) == 0 {
		return ""
	}

	var b strings.Builder
	writeHeader := func(name, help string) {
		fmt.Fprintf(&b, "# HELP %s %s\n", name, help)
		fmt.Fprintf(&b, "# TYPE %s counter\n", name)
	}
	writeSeries := func(name string, labels [][2]string, value int64) {
		b.WriteString(name)
		if len(labels) > 0 {
			b.WriteString("{")
			for i, l := range labels {
				if i > 0 {
					b.WriteString(",")
				}
				b.WriteString(l[0])
				b.WriteString(`="`)
				b.WriteString(labelEscaper.Replace(l[1]))
				b.WriteString(`"`)
			}
			b.WriteString("}")
		}
		b.WriteString(" ")
		b.WriteString(strconv.FormatInt(value, 10))
		b.WriteString("\n")
	}

	writeHeader("sap_proxy_compress_blocks_total",
		"tool_result content blocks the compressor looked at, by outcome. compressed counts blocks that shrank; skipped counts eligible blocks that were left alone. Every tool_result block in the request is considered, including the ones already in the message history.")
	for _, r := range s.Blocks {
		writeSeries("sap_proxy_compress_blocks_total", [][2]string{
			{"surface", r.Surface}, {"result", r.Result},
		}, r.Count)
	}

	writeHeader("sap_proxy_compress_bytes_before_total",
		"Bytes of tool_result content handed to the compressor. Bytes, not tokens: this proxy has no tokenizer, so these are the raw byte counts of the text that was compressed.")
	for _, r := range s.Bytes {
		writeSeries("sap_proxy_compress_bytes_before_total", [][2]string{{"surface", r.Surface}}, r.BytesBefore)
	}

	writeHeader("sap_proxy_compress_bytes_after_total",
		"Bytes of tool_result content sent upstream after compression. Bytes, not tokens. Compare with sap_proxy_compress_bytes_before_total for the reduction ratio.")
	for _, r := range s.Bytes {
		writeSeries("sap_proxy_compress_bytes_after_total", [][2]string{{"surface", r.Surface}}, r.BytesAfter)
	}

	writeHeader("sap_proxy_compress_skipped_total",
		"Requests in which no tool_result block was compressed, by reason. Skipping is not an error: the request was proxied with its original bytes.")
	for _, r := range s.Skipped {
		writeSeries("sap_proxy_compress_skipped_total", [][2]string{
			{"surface", r.Surface}, {"reason", r.Reason},
		}, r.Count)
	}

	return b.String()
}

// statsResponse is the JSON body of GET /stats/compress.
type statsResponse struct {
	Since         string          `json:"since"`
	UptimeSeconds float64         `json:"uptime_seconds"`
	Blocks        []BlockSeries   `json:"blocks"`
	Bytes         []BytesSeries   `json:"bytes"`
	Skipped       []SkippedSeries `json:"skipped"`
}

// StatsHandler serves GET /stats/compress: the same counters as the Prometheus
// families, small enough to read by eye when deciding whether the feature is
// doing anything.
func (c *Collector) StatsHandler(ctx *gin.Context) {
	snap := c.Snapshot()

	// Marshal empty lists rather than null so consumers can iterate blindly.
	if snap.Blocks == nil {
		snap.Blocks = []BlockSeries{}
	}
	if snap.Bytes == nil {
		snap.Bytes = []BytesSeries{}
	}
	if snap.Skipped == nil {
		snap.Skipped = []SkippedSeries{}
	}

	ctx.JSON(http.StatusOK, statsResponse{
		Since:         snap.Since.UTC().Format("2006-01-02T15:04:05Z07:00"),
		UptimeSeconds: snap.UptimeSeconds,
		Blocks:        snap.Blocks,
		Bytes:         snap.Bytes,
		Skipped:       snap.Skipped,
	})
}

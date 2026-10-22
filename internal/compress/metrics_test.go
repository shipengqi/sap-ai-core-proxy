package compress

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestCollector_Render pins the exposition the router concatenates into
// /metrics: the families, the labels, and the fact that the byte counters say
// bytes rather than tokens.
func TestCollector_Render(t *testing.T) {
	c := NewCollector()
	c.Observe("anthropic", Report{
		BlocksConsidered: 2, BlocksCompressed: 1,
		OriginalBytes: 9000, CompressedBytes: 800, Strategy: StrategyLog,
	})
	// A second request whose block was left alone.
	c.Observe("anthropic", Report{BlocksConsidered: 1, Skipped: skipNotLog})
	// A request rejected before a block is eligible must still expose its reason;
	// otherwise an empty dashboard is indistinguishable from a disabled path.
	c.Observe("anthropic", Report{Skipped: skipPlainContent})
	// No observation and no reason stays invisible.
	c.Observe("anthropic", Report{})

	text := c.MetricsText()
	t.Logf("rendered:\n%s", text)

	for _, want := range []string{
		"# TYPE sap_proxy_compress_blocks_total counter",
		`sap_proxy_compress_blocks_total{surface="anthropic",result="compressed"} 1`,
		// 2: one skipped block in the first report, one in the second.
		`sap_proxy_compress_blocks_total{surface="anthropic",result="skipped"} 2`,
		"# TYPE sap_proxy_compress_bytes_before_total counter",
		`sap_proxy_compress_bytes_before_total{surface="anthropic"} 9000`,
		"# TYPE sap_proxy_compress_bytes_after_total counter",
		`sap_proxy_compress_bytes_after_total{surface="anthropic"} 800`,
		`{surface="anthropic",reason="not_log"} 1`,
		`{surface="anthropic",reason="plain_content"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	// The unit must be stated: there is no tokenizer behind these numbers.
	if !strings.Contains(text, "Bytes, not tokens") {
		t.Errorf("the byte counters must say they are bytes, not tokens:\n%s", text)
	}
	// Rendering twice must be identical: a scrape that reorders is a scrape
	// that produces spurious diffs.
	if again := c.MetricsText(); !strings.Contains(again, `sap_proxy_compress_blocks_total{surface="anthropic",result="compressed"} 1`) {
		t.Errorf("second render differs:\n%s", again)
	}

	// Every sample line must be well formed: name, optional labels, value.
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, " ")
		if !ok || name == "" || value == "" {
			t.Errorf("malformed sample line %q", line)
		}
	}
}

// TestCollector_StatsHandler covers GET /stats/compress.
func TestCollector_StatsHandler(t *testing.T) {
	c := NewCollector()
	c.Observe("anthropic", Report{
		BlocksConsidered: 1, BlocksCompressed: 1,
		OriginalBytes: 5000, CompressedBytes: 700,
	})

	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/stats/compress", nil)
	c.StatsHandler(ctx)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var doc struct {
		Since  string `json:"since"`
		Blocks []struct {
			Surface string `json:"surface"`
			Result  string `json:"result"`
			Count   int64  `json:"count"`
		} `json:"blocks"`
		Bytes []struct {
			Surface     string `json:"surface"`
			BytesBefore int64  `json:"bytes_before"`
			BytesAfter  int64  `json:"bytes_after"`
		} `json:"bytes"`
		Skipped []struct {
			Reason string `json:"reason"`
		} `json:"skipped"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode body: %v\n%s", err, w.Body.String())
	}
	if doc.Since == "" {
		t.Errorf("since is empty")
	}
	if len(doc.Blocks) != 1 || doc.Blocks[0].Count != 1 || doc.Blocks[0].Result != resultCompressed {
		t.Errorf("blocks = %+v, want one compressed entry", doc.Blocks)
	}
	if len(doc.Bytes) != 1 || doc.Bytes[0].BytesBefore != 5000 || doc.Bytes[0].BytesAfter != 700 {
		t.Errorf("bytes = %+v, want 5000 -> 700", doc.Bytes)
	}
	// Empty lists marshal as [] so a consumer can iterate blindly.
	if doc.Skipped == nil {
		t.Errorf("skipped is null, want []")
	}
}

// TestCollector_IgnoresUnknownSurface keeps the counters clean.
func TestCollector_IgnoresUnknownSurface(t *testing.T) {
	c := NewCollector()
	c.Observe("", Report{BlocksConsidered: 1, BlocksCompressed: 1, OriginalBytes: 10, CompressedBytes: 1})
	if text := c.MetricsText(); text != "" {
		t.Errorf("an unknown surface published %q", text)
	}
}

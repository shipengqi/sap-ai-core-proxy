package usage

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// accumulatorKey is the private context key for the per-request Accumulator.
//
// The value travels in the request context rather than in gin.Keys because
// gin's Set/Get only accept string keys, and a string key in a shared map is
// exactly the collision hazard this avoids. Replacing c.Request with a derived
// request keeps the original context (and therefore cancellation and deadlines)
// intact.
type accumulatorKey struct{}

// Middleware attaches an Accumulator to the request and publishes the finished
// request to col exactly once.
//
// It runs after the handler returns, which is the only point where the token
// numbers are final: the streaming helpers observe usage while they copy bytes
// to the client, so the totals are complete only after c.Next() comes back.
func Middleware(col *Collector) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil {
			c.Next()
			return
		}

		acc := &Accumulator{}
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), accumulatorKey{}, acc))

		start := time.Now()
		c.Next()

		// Every inference route in this proxy is a POST. Restricting to POST keeps
		// model listings, the HEAD connectivity probe, /health, /metrics, /stats
		// and the debug endpoints out of the counters — and, more importantly, out
		// of usage_missing, which must only measure requests that should have
		// produced token counts.
		if c.Request.Method != http.MethodPost {
			return
		}

		var model string
		if v, ok := c.Get("model"); ok {
			model, _ = v.(string)
		}

		// The Record carries the *effective* status: a stream whose upstream
		// failed is recorded as failed even though the client was sent a 200,
		// because the wire status is locked before the upstream answer is known.
		record := acc.Report(c.Writer.Status())
		record.Surface = surfaceFromPath(c.Request.URL.Path)
		record.Model = model
		record.Latency = time.Since(start)
		col.Add(record)
	}
}

// surfaceFromPath derives the surface from the request path. Requests that match
// no inference surface (health, /metrics, /stats, /debug) return "" and are not
// recorded.
func surfaceFromPath(path string) string {
	switch {
	case strings.HasPrefix(path, "/anthropic/"):
		return SurfaceAnthropic
	case strings.HasPrefix(path, "/openai/"):
		return SurfaceOpenAI
	case strings.HasPrefix(path, "/gemini/"):
		return SurfaceGemini
	case strings.HasPrefix(path, "/litellm/"):
		return SurfaceLiteLLM
	}
	return ""
}

// accumulator returns the request's Accumulator, or nil when the middleware is
// not installed. Every helper below degrades to a no-op on nil, because
// observability must never be able to fail a request.
func accumulator(c *gin.Context) *Accumulator {
	if c == nil || c.Request == nil {
		return nil
	}
	acc, _ := c.Request.Context().Value(accumulatorKey{}).(*Accumulator)
	return acc
}

// MarkStream marks the request as streaming.
func MarkStream(c *gin.Context) {
	accumulator(c).MarkStream()
}

// MarkUpstreamStatus records the HTTP status the upstream returned. The streaming
// handlers must call it before c.Status(200): once the SSE headers are written the
// client-visible status is fixed, and this is the only failure signal left.
func MarkUpstreamStatus(c *gin.Context, code int) {
	accumulator(c).MarkUpstreamStatus(code)
}

// MarkNoUsageExpected declares that the endpoint produces no token counts (image
// generation, audio transcription), so a zero is not a measurement gap.
func MarkNoUsageExpected(c *gin.Context) {
	accumulator(c).MarkNoUsageExpected()
}

// MarkCaptureDropped records that usage existed but was discarded while
// buffering the response.
func MarkCaptureDropped(c *gin.Context) {
	accumulator(c).MarkCaptureDropped()
}

// ObserveAnthropicEvent records one Anthropic SSE payload (without the "data: "
// prefix).
func ObserveAnthropicEvent(c *gin.Context, payload string) {
	accumulator(c).observeAnthropicEvent([]byte(payload))
}

// ObserveOpenAIChunk records one OpenAI SSE payload (without the "data: "
// prefix).
func ObserveOpenAIChunk(c *gin.Context, payload string) {
	accumulator(c).observeOpenAIChunk([]byte(payload))
}

// ObserveGeminiChunk records one Gemini response object: either a
// generateContent body or a single element of a streamGenerateContent array.
func ObserveGeminiChunk(c *gin.Context, payload []byte) {
	accumulator(c).observeGeminiChunk(payload)
}

// ObserveGeminiStream records a fully buffered streamGenerateContent array.
func ObserveGeminiStream(c *gin.Context, body []byte) {
	accumulator(c).observeGeminiStream(body)
}

// ObserveJSONBody records a complete non-streaming response body. Callers pass
// the bytes that are about to be written to the client; the body is only read.
func ObserveJSONBody(c *gin.Context, body []byte) {
	accumulator(c).observeJSON(body)
}

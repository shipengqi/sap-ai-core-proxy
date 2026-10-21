package diagnostics

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

// requestKey is the private context key for the parsed request. A private type
// keeps it from colliding with anything else stored in the context.
type requestKey struct{}

// ObserveRequest fingerprints an inbound request body.
//
// Call it from a handler that already has the body in memory, after c.Set
// ("model", ...) so the model label is available. The body is only read: this
// function cannot change what gets forwarded.
func ObserveRequest(c *gin.Context, body []byte) {
	if c == nil || c.Request == nil {
		return
	}

	surface := surfaceFromPath(c.Request.URL.Path)
	if surface == "" {
		return
	}

	var model string
	if v, ok := c.Get("model"); ok {
		model, _ = v.(string)
	}

	req := parseRequest(body, surface, model, c.ClientIP())
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestKey{}, req))
}

// Middleware commits the attribution once the handler has returned.
//
// It MUST be registered before usage.Middleware: gin runs middleware in
// registration order, so the first one registered is the outermost, and only an
// outer middleware sees the usage.Record that the inner one stores after its
// own c.Next() returns.
//
// Nothing here runs on the request path — every write happens after the
// response has been produced, which is what keeps the byte stream untouched.
func Middleware(col *Collector) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		if c.Request == nil {
			return
		}

		req, ok := c.Request.Context().Value(requestKey{}).(request)
		if !ok {
			// No ObserveRequest call for this route. Do not invent a skip: the
			// usage package already reports uninstrumented endpoints, and
			// reporting them twice would just be noise in a second place.
			return
		}
		if req.skipped != "" {
			col.Add(entry{skipped: req.skipped})
			return
		}

		rec, ok := usage.RecordFromContext(c)
		if !ok {
			// The usage middleware did not commit a record (non-POST, or the
			// route is not an inference surface).
			col.Add(entry{skipped: skipNoRequestState})
			return
		}
		if rec.Status >= 400 {
			// A failed request never reached the cache, so attributing it would
			// inflate whichever bucket its prefix happened to land in.
			col.Add(entry{skipped: skipFailedRequest})
			return
		}

		model := rec.Model
		if model == "" {
			model = req.model
		}
		if !rec.Observed && !rec.NoUsageExpected {
			// Nobody read the response, so there is no cache activity to cross the
			// classification with. Attributing it anyway would fold "we could not
			// look" into "nothing was cached" — the same confusion the usage
			// package separates with its Observed flag.
			col.Add(entry{skipped: skipNoObservation})
			return
		}
		col.observe(req, rec.Tokens, rec.HasUsage, model)
	}
}

// observe classifies the request against its predecessor, stores the new state
// and records the attribution.
//
// The get/classify/put sequence runs under the collector lock so the three steps
// are atomic with respect to other requests of the same conversation.
//
// Honest note on why: two concurrent requests could both read the same
// predecessor, so the second would be judged against a state the first already
// replaced. The race detector cannot see it — the session store locks
// internally, so each individual access is safe — and the window proved too
// small to trigger in practice either: 64 goroutines released from a barrier
// produced first_request=1 with and without the lock. The lock is therefore
// precautionary. It is still worth taking because it makes the invariant
// structural instead of scheduling-dependent, and the cost is one uncontended
// mutex on a path that already allocates.
func (c *Collector) observe(req request, tokens usage.Tokens, hasUsage bool, model string) {
	var outcome Outcome

	c.mu.Lock()
	prev, known := c.sessions.get(req.session)
	outcome = classify(prev, known, req.fp, req.msgCount, req.msgHash)
	c.sessions.put(req.session, sessionState{
		fp:       req.fp,
		msgCount: req.msgCount,
	})
	c.mu.Unlock()

	result := attribute(outcome, tokens, hasUsage)
	// The per-conversation counters live in the same store, so they are bumped
	// under that store's own lock once the classification is settled.
	c.sessions.countBump(req.session, result == ResultBusted)

	c.Add(entry{
		surface:  req.surface,
		model:    model,
		session:  req.session,
		outcome:  outcome,
		result:   result,
		volatile: req.volatile,
		markers:  req.markers,
	})
}

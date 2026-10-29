package openai

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shipengqi/sap-ai-core-proxy/internal/proxy"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
	"github.com/shipengqi/sap-ai-core-proxy/internal/stream"
	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

const responseRouteTTL = 24 * time.Hour
const maxResponseRoutes = 10000

type responseRoute struct {
	Deployment sapclient.Deployment
	Expires    time.Time
}
type responseRegistry struct {
	mu     sync.Mutex
	routes map[string]responseRoute
}

func (r *responseRegistry) remember(id string, dep *sapclient.Deployment) {
	if id == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for key, v := range r.routes {
		if now.After(v.Expires) {
			delete(r.routes, key)
		}
	}
	if _, exists := r.routes[id]; !exists && len(r.routes) >= maxResponseRoutes {
		var oldest string
		var expiry time.Time
		for key, v := range r.routes {
			if oldest == "" || v.Expires.Before(expiry) {
				oldest = key
				expiry = v.Expires
			}
		}
		delete(r.routes, oldest)
	}
	r.routes[id] = responseRoute{Deployment: *dep, Expires: now.Add(responseRouteTTL)}
}
func (r *responseRegistry) lookup(id string) (*sapclient.Deployment, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.routes[id]
	if !ok {
		return nil, false
	}
	if time.Now().After(v.Expires) {
		delete(r.routes, id)
		return nil, false
	}
	return &v.Deployment, true
}
func (r *responseRegistry) forget(id string) { r.mu.Lock(); defer r.mu.Unlock(); delete(r.routes, id) }

// CreateResponse preserves native Responses streaming and stored-response affinity.
func (h *Handler) CreateResponse(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(400, errorBody(err.Error()))
		return
	}
	var req struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Previous string `json:"previous_response_id"`
	}
	if json.Unmarshal(body, &req) != nil {
		c.JSON(400, errorBody("invalid Responses request"))
		return
	}
	var dep *sapclient.Deployment
	if req.Previous != "" {
		var ok bool
		dep, ok = h.responses.lookup(req.Previous)
		if !ok {
			c.JSON(404, errorBody("previous response route not found; response affinity requires the same proxy instance"))
			return
		}
		if req.Model != "" {
			candidate, e := h.deployments.GetDeployment(c.Request.Context(), req.Model)
			if e != nil {
				c.JSON(404, errorBody(e.Error()))
				return
			}
			if candidate.EffectiveModelName() != dep.EffectiveModelName() {
				c.JSON(400, errorBody("previous_response_id belongs to a different model"))
				return
			}
		} else {
			req.Model = dep.EffectiveModelName()
		}
	} else {
		if req.Model == "" {
			c.JSON(400, errorBody("missing required field: model"))
			return
		}
		dep, err = h.deployments.GetDeployment(c.Request.Context(), req.Model)
		if err != nil {
			c.JSON(404, errorBody(err.Error()))
			return
		}
	}
	c.Set("model", req.Model)
	c.Set("resolved_model", dep.EffectiveModelName())
	remember := func(data []byte) {
		var response struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(data, &response) == nil {
			h.responses.remember(response.ID, dep)
		}
	}
	if req.Stream {
		retries := 5
		if req.Previous != "" {
			retries = 0
		}
		upstream, resolved, e := h.deployments.CallStreaming(c.Request.Context(), dep, retries, func(candidate *sapclient.Deployment) (*http.Response, error) {
			return h.client.DoStreaming(c.Request.Context(), http.MethodPost, candidate.DeployedURL+responsesPath, bytes.NewReader(body), rgHeaders(candidate.ResourceGroupID))
		})
		if e != nil {
			c.JSON(502, errorBody(e.Error()))
			return
		}
		dep = resolved
		c.Set("resolved_model", dep.EffectiveModelName())
		if !proxy.CheckStream(c, upstream) {
			return
		}
		stream.PipeResponses(c, upstream, func(data []byte) {
			var ev struct {
				Response json.RawMessage `json:"response"`
			}
			if json.Unmarshal(data, &ev) == nil {
				remember(ev.Response)
			}
		})
		return
	}
	upstream, err := h.client.Do(c.Request.Context(), http.MethodPost, dep.DeployedURL+responsesPath, bytes.NewReader(body), rgHeaders(dep.ResourceGroupID))
	if err != nil {
		c.JSON(502, errorBody(err.Error()))
		return
	}
	defer func() { _ = upstream.Body.Close() }()
	b, err := io.ReadAll(upstream.Body)
	if err != nil {
		c.JSON(502, errorBody(err.Error()))
		return
	}
	proxy.CopyHeaders(c, upstream)
	if upstream.StatusCode >= 200 && upstream.StatusCode < 300 {
		remember(b)
	}
	usage.ObserveJSONBody(c, b)
	c.Data(upstream.StatusCode, "application/json", b)
}
func (h *Handler) GetResponse(c *gin.Context)    { h.storedResponse(c, http.MethodGet) }
func (h *Handler) DeleteResponse(c *gin.Context) { h.storedResponse(c, http.MethodDelete) }
func (h *Handler) storedResponse(c *gin.Context, method string) {
	id := c.Param("id")
	dep, ok := h.responses.lookup(id)
	if !ok {
		c.JSON(404, errorBody("response route not found: "+id))
		return
	}
	upstream, err := h.client.Do(c.Request.Context(), method, dep.DeployedURL+"/responses/"+id+"?api-version=2024-02-01", nil, rgHeaders(dep.ResourceGroupID))
	if err != nil {
		c.JSON(502, errorBody(err.Error()))
		return
	}
	defer func() { _ = upstream.Body.Close() }()
	body, err := io.ReadAll(upstream.Body)
	if err != nil {
		c.JSON(502, errorBody(err.Error()))
		return
	}
	proxy.CopyHeaders(c, upstream)
	if method == http.MethodDelete && upstream.StatusCode >= 200 && upstream.StatusCode < 300 {
		h.responses.forget(id)
	}
	c.Data(upstream.StatusCode, "application/json", body)
}

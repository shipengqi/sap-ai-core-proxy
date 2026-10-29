package gemini

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/proxy"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
	"github.com/shipengqi/sap-ai-core-proxy/internal/stream"
	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

func (h *Handler) Generate(c *gin.Context) {
	raw := strings.TrimPrefix(c.Param("model"), "/")

	lastColon := strings.LastIndex(raw, ":")
	if lastColon < 0 {
		c.JSON(http.StatusBadRequest, errorBody("path must be models/{model}:{operation}"))
		return
	}
	modelName := raw[:lastColon]
	operation := raw[lastColon+1:]

	// Set model in context for logging middleware
	c.Set("model", modelName)

	dep, err := h.deployments.GetDeployment(c.Request.Context(), modelName)
	if err != nil {
		c.JSON(http.StatusNotFound, errorBody(err.Error()))
		return
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorBody("read request body: "+err.Error()))
		return
	}

	buildURL := func(candidate *sapclient.Deployment) string {
		resolved := candidate.EffectiveModelName()
		if _, name, ok := strings.Cut(resolved, "--"); ok {
			resolved = name
		}
		url := candidate.DeployedURL + "/models/" + resolved + ":" + operation
		if r := c.Request.URL.RawQuery; r != "" {
			url += "?" + r
		}
		return url
	}
	upstreamURL := buildURL(dep)
	headers := func(candidate *sapclient.Deployment) map[string]string {
		if candidate.ResourceGroupID == "" {
			return nil
		}
		return map[string]string{"AI-Resource-Group": candidate.ResourceGroupID}
	}

	if operation == "streamGenerateContent" {
		slog.Info("calling gemini model", "model", modelName, "operation", operation, "streaming", true, "deployment_id", dep.ID)
		upstream, _, err := h.deployments.CallStreaming(c.Request.Context(), dep, 5, func(candidate *sapclient.Deployment) (*http.Response, error) {
			return h.client.DoStreaming(c.Request.Context(), http.MethodPost, buildURL(candidate), bytes.NewReader(body), headers(candidate))
		})
		if err != nil {
			c.JSON(http.StatusBadGateway, errorBody(err.Error()))
			return
		}
		// Recorded before c.Status(200) locks the client-visible status.
		if !proxy.CheckStream(c, upstream) {
			return
		}
		c.Status(http.StatusOK)
		stream.PipeGemini(c, upstream)
		return
	}

	slog.Info("calling gemini model", "model", modelName, "operation", operation, "streaming", false, "deployment_id", dep.ID)
	upstream, err := h.client.Do(c.Request.Context(), http.MethodPost, upstreamURL, bytes.NewReader(body), headers(dep))
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}
	defer func() { _ = upstream.Body.Close() }()
	respBody, readErr := io.ReadAll(upstream.Body)
	if readErr != nil {
		c.JSON(502, errorBody(readErr.Error()))
		return
	}
	// Native Gemini body: the observer reads usageMetadata. Observation only, the
	// bytes are forwarded unchanged.
	usage.ObserveJSONBody(c, respBody)
	c.Data(upstream.StatusCode, "application/json", respBody)
}

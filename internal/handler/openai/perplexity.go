package openai

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/proxy"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
	"github.com/shipengqi/sap-ai-core-proxy/internal/stream"
	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

// chatCompletionsPerplexity passes Perplexity Sonar requests through unchanged.
// Perplexity uses the OpenAI format natively; the response may include
// provider-specific fields like citations and search_results.
func (h *Handler) chatCompletionsPerplexity(c *gin.Context, modelStr string, raw map[string]json.RawMessage, streaming bool) {
	bodyBytes, _ := json.Marshal(raw)

	buildURL := func(dep *sapclient.Deployment) string {
		return dep.DeployedURL + "/chat/completions?api-version=2024-12-01-preview"
	}

	if streaming {
		dep, err := h.deployments.GetDeployment(c.Request.Context(), modelStr)
		if err != nil {
			c.JSON(http.StatusNotFound, errorBody(err.Error()))
			return
		}
		slog.Info("calling perplexity model", "model", modelStr, "streaming", true, "deployment_id", dep.ID)
		upstream, _, err := h.deployments.CallStreaming(c.Request.Context(), dep, 5, func(candidate *sapclient.Deployment) (*http.Response, error) {
			return h.client.DoStreaming(c.Request.Context(), http.MethodPost, buildURL(candidate), bytes.NewReader(bodyBytes), rgHeaders(candidate.ResourceGroupID))
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
		stream.PipeOpenAI(c, upstream)
		return
	}

	slog.Info("calling perplexity model", "model", modelStr, "streaming", false)
	status, respBody, err := h.deployments.FindAndCall(
		c.Request.Context(), modelStr, 5,
		func(dep *sapclient.Deployment) (int, []byte, error) {
			resp, err := h.client.Do(c.Request.Context(), http.MethodPost,
				buildURL(dep), bytes.NewReader(bodyBytes), rgHeaders(dep.ResourceGroupID))
			if err != nil {
				return 0, nil, err
			}
			defer func() { _ = resp.Body.Close() }()
			b, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				return 0, nil, readErr
			}
			return resp.StatusCode, b, nil
		},
	)
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}
	// Sonar answers in OpenAI format; observation only, the body is unchanged.
	usage.ObserveJSONBody(c, respBody)
	c.Data(status, "application/json", respBody)
}

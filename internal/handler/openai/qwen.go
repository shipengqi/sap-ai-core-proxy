package openai

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
	"github.com/shipengqi/sap-ai-core-proxy/internal/stream"
)

// thinkingBudgets maps OpenAI reasoning_effort levels to Qwen thinking_budget token counts.
var thinkingBudgets = map[string]int{
	"minimal": 1024,
	"low":     2048,
	"medium":  5000,
	"high":    10000,
	"xhigh":   20000,
}

// applyQwenThinking translates OpenAI's reasoning_effort into Qwen's
// enable_thinking / thinking_budget fields. Returns a new map; never mutates raw.
func applyQwenThinking(raw map[string]json.RawMessage, deploymentID string) map[string]json.RawMessage {
	effortRaw, hasEffort := raw["reasoning_effort"]
	if !hasEffort {
		return raw
	}

	out := make(map[string]json.RawMessage, len(raw))
	for k, v := range raw {
		out[k] = v
	}
	delete(out, "reasoning_effort")

	// Skip thinking if deployment is known to not support it
	if IsThinkingUnsupported(deploymentID) {
		return out
	}

	var effort string
	_ = json.Unmarshal(effortRaw, &effort)

	if effort == "none" {
		v, _ := json.Marshal(false)
		out["enable_thinking"] = v
		return out
	}

	budget, ok := thinkingBudgets[effort]
	if !ok {
		budget = thinkingBudgets["medium"]
	}
	v, _ := json.Marshal(true)
	out["enable_thinking"] = v
	b, _ := json.Marshal(budget)
	out["thinking_budget"] = b
	return out
}

func (h *Handler) chatCompletionsQwen(c *gin.Context, modelStr string, raw map[string]json.RawMessage, streaming bool) {
	// Get deployment first to pass ID for cache checking
	dep, err := h.deployments.GetDeployment(c.Request.Context(), modelStr)
	if err != nil {
		c.JSON(http.StatusNotFound, errorBody(err.Error()))
		return
	}

	// Remove api-version suffix — Alibaba Bailian ignores it but we keep the URL clean.
	buildURL := func(dep *sapclient.Deployment) string {
		return dep.DeployedURL + "/chat/completions"
	}

	body := applyQwenThinking(raw, dep.ID)
	bodyBytes, _ := json.Marshal(body)

	if streaming {
		slog.Info("calling qwen model", "model", modelStr, "streaming", true, "deployment_id", dep.ID)
		upstream, err := h.client.DoStreaming(c.Request.Context(), http.MethodPost,
			buildURL(dep), bytes.NewReader(bodyBytes), nil)
		if err != nil {
			c.JSON(http.StatusBadGateway, errorBody(err.Error()))
			return
		}
		c.Status(http.StatusOK)
		stream.PipeOpenAI(c, upstream)
		return
	}

	slog.Info("calling qwen model", "model", modelStr, "streaming", false)

	// First attempt with thinking (if not cached as unsupported)
	resp, err := h.client.Do(c.Request.Context(), http.MethodPost,
		buildURL(dep), bytes.NewReader(bodyBytes), nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(resp.Body)
	status := resp.StatusCode

	// Check if we need to retry without thinking
	if status == http.StatusBadRequest && IsAdaptiveThinkingError(respBody) {
		slog.Info("deployment doesn't support adaptive thinking, retrying without it",
			"model", modelStr, "deployment_id", dep.ID)

		MarkThinkingUnsupported(dep.ID)

		// Retry with thinking disabled (cache will skip thinking now)
		body = applyQwenThinking(raw, dep.ID)
		bodyBytes, _ = json.Marshal(body)

		resp, err = h.client.Do(c.Request.Context(), http.MethodPost,
			buildURL(dep), bytes.NewReader(bodyBytes), nil)
		if err != nil {
			c.JSON(http.StatusBadGateway, errorBody(err.Error()))
			return
		}
		defer func() { _ = resp.Body.Close() }()
		respBody, _ = io.ReadAll(resp.Body)
		status = resp.StatusCode
	}

	c.Data(status, "application/json", respBody)
}

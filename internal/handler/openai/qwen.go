package openai

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/proxy"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
	"github.com/shipengqi/sap-ai-core-proxy/internal/stream"
	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
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

	if streaming {

		slog.Info("calling qwen model", "model", modelStr, "streaming", true, "deployment_id", dep.ID)
		_, requested := raw["reasoning_effort"]
		upstream, _, err := h.deployments.CallStreaming(c.Request.Context(), dep, 5, func(candidate *sapclient.Deployment) (*http.Response, error) {
			key := CapabilityKey(candidate, "/chat/completions")
			return h.thinkingStream(c.Request.Context(), candidate, "/chat/completions", key, requested, func() []byte {
				body, _ := json.Marshal(applyQwenThinking(raw, key))
				return body
			})
		})
		if err != nil {
			c.JSON(502, errorBody(err.Error()))
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

	slog.Info("calling qwen model", "model", modelStr, "streaming", false)

	status, respBody, err := h.Complete(c.Request.Context(), raw)
	if err != nil {
		c.JSON(502, errorBody(err.Error()))
		return
	}

	// Qwen answers in OpenAI format; observation only, the body is unchanged.
	usage.ObserveJSONBody(c, respBody)
	c.Data(status, "application/json", respBody)
}

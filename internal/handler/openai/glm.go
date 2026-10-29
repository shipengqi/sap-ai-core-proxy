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
	"github.com/shipengqi/sap-ai-core-proxy/internal/transform"
	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

// chatCompletionsGlm handles Z.ai GLM models (zai--* prefix).
// GLM exposes the Anthropic Messages API natively at /messages on the deployment URL —
// not the Bedrock /invoke path that other Anthropic models use.
// This handler converts the incoming OpenAI-format request to Anthropic Messages format
// and converts the Anthropic response back to OpenAI format.
func (h *Handler) chatCompletionsGlm(c *gin.Context, modelStr string, raw map[string]json.RawMessage, streaming bool) {
	if err := validateGLMChat(raw); err != nil {
		c.JSON(400, errorBody(err.Error()))
		return
	}
	glmBody := openAIToGLMBody(raw, modelStr, streaming)

	buildURL := func(dep *sapclient.Deployment) string {
		return dep.DeployedURL + "/messages"
	}

	if streaming {
		dep, err := h.deployments.GetDeployment(c.Request.Context(), modelStr)
		if err != nil {
			c.JSON(http.StatusNotFound, errorBody(err.Error()))
			return
		}
		slog.Info("calling glm model", "model", modelStr, "streaming", true, "deployment_id", dep.ID)
		upstream, dep, err := h.deployments.CallStreaming(c.Request.Context(), dep, 5, func(candidate *sapclient.Deployment) (*http.Response, error) {
			return h.client.DoStreaming(c.Request.Context(), http.MethodPost, buildURL(candidate), bytes.NewReader(glmBody), rgHeaders(candidate.ResourceGroupID))
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
		stream.PipeAnthropicToOpenAI(c, upstream, "chatcmpl-"+dep.ID, modelStr, wantsStreamUsage(raw))
		return
	}

	slog.Info("calling glm model", "model", modelStr, "streaming", false)
	status, respBody, err := h.deployments.FindAndCall(
		c.Request.Context(), modelStr, 5,
		func(dep *sapclient.Deployment) (int, []byte, error) {
			resp, err := h.client.Do(c.Request.Context(), http.MethodPost,
				buildURL(dep), bytes.NewReader(glmBody), rgHeaders(dep.ResourceGroupID))
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
	if status != http.StatusOK {
		c.Data(status, "application/json", respBody)
		return
	}

	// GLM returns Anthropic Messages format; convert back to OpenAI format.
	openAIResp := glmAnthropicToOpenAIResponse(respBody, modelStr)
	// The converted body carries GLM's usage restated as OpenAI counters.
	usage.ObserveJSONBody(c, openAIResp)
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	_ = json.Unmarshal(openAIResp, &envelope)
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		c.Data(502, "application/json", openAIResp)
		return
	}
	c.Data(http.StatusOK, "application/json", openAIResp)
}

// openAIToGLMBody converts an OpenAI chat request to Anthropic Messages API format
// for GLM's /messages endpoint.
func openAIToGLMBody(raw map[string]json.RawMessage, modelStr string, streaming bool) []byte {
	out := make(map[string]json.RawMessage)

	// Keep the model name — GLM needs it in the body (unlike Bedrock).
	if v, ok := raw["model"]; ok {
		out["model"] = v
	} else {
		v, _ := json.Marshal(modelStr)
		out["model"] = v
	}

	// Promote system messages from the messages array to the top-level system field,
	// then translate OpenAI tool-calling shapes into Anthropic content blocks.
	converted := transform.PromoteSystemMessages(raw)
	converted = transform.ConvertOpenAIToolMessagesToAnthropic(converted)
	converted = transform.ConvertOpenAIToolsToAnthropic(converted)

	if v, ok := converted["messages"]; ok {
		out["messages"] = v
	}
	if v, ok := converted["system"]; ok {
		out["system"] = v
	}

	// max_tokens / max_completion_tokens → max_tokens (Anthropic field name).
	if v, ok := raw["max_completion_tokens"]; ok {
		out["max_tokens"] = v
	} else if v, ok := raw["max_tokens"]; ok {
		out["max_tokens"] = v
	} else {
		v, _ := json.Marshal(8192)
		out["max_tokens"] = v
	}

	for _, field := range []string{"temperature", "top_p", "tools", "tool_choice", "stop_sequences", "metadata"} {
		if v, ok := converted[field]; ok {
			out[field] = v
		}
	}
	// OpenAI stop → Anthropic stop_sequences
	if _, ok := out["stop_sequences"]; !ok {
		if v, ok := raw["stop"]; ok {
			var text string
			if json.Unmarshal(v, &text) == nil {
				out["stop_sequences"], _ = json.Marshal([]string{text})
			} else {
				out["stop_sequences"] = v
			}
		}
	}

	streamVal, _ := json.Marshal(streaming)
	out["stream"] = streamVal

	b, _ := json.Marshal(out)
	return b
}

// glmAnthropicToOpenAIResponse converts a GLM Anthropic Messages response to OpenAI format.
func glmAnthropicToOpenAIResponse(data []byte, model string) []byte {
	return bedrockToOpenAIResponse(data, model)
}

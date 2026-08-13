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
	"github.com/shipengqi/sap-ai-core-proxy/internal/transform"
)

// chatCompletionsGlm handles Z.ai GLM models (zai--* prefix).
// GLM exposes the Anthropic Messages API natively at /messages on the deployment URL —
// not the Bedrock /invoke path that other Anthropic models use.
// This handler converts the incoming OpenAI-format request to Anthropic Messages format
// and converts the Anthropic response back to OpenAI format.
func (h *Handler) chatCompletionsGlm(c *gin.Context, modelStr string, raw map[string]json.RawMessage, streaming bool) {
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
		upstream, err := h.client.DoStreaming(c.Request.Context(), http.MethodPost,
			buildURL(dep), bytes.NewReader(glmBody), nil)
		if err != nil {
			c.JSON(http.StatusBadGateway, errorBody(err.Error()))
			return
		}
		c.Status(http.StatusOK)
		stream.PipeAnthropicToOpenAI(c, upstream, "chatcmpl-"+dep.ID, modelStr)
		return
	}

	slog.Info("calling glm model", "model", modelStr, "streaming", false)
	status, respBody, err := h.deployments.FindAndCall(
		c.Request.Context(), modelStr, 5,
		func(dep *sapclient.Deployment) (int, []byte, error) {
			resp, err := h.client.Do(c.Request.Context(), http.MethodPost,
				buildURL(dep), bytes.NewReader(glmBody), nil)
			if err != nil {
				return 0, nil, err
			}
			defer func() { _ = resp.Body.Close() }()
			b, _ := io.ReadAll(resp.Body)
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

	// Promote system messages from the messages array to the top-level system field.
	promoted := transform.PromoteSystemMessages(raw)

	if v, ok := promoted["messages"]; ok {
		out["messages"] = v
	}
	if v, ok := promoted["system"]; ok {
		out["system"] = v
	}

	// max_tokens / max_completion_tokens → max_tokens (Anthropic field name).
	if v, ok := raw["max_tokens"]; ok {
		out["max_tokens"] = v
	} else if v, ok := raw["max_completion_tokens"]; ok {
		out["max_tokens"] = v
	} else {
		v, _ := json.Marshal(8192)
		out["max_tokens"] = v
	}

	for _, field := range []string{"temperature", "top_p", "tools", "tool_choice", "stop_sequences", "metadata"} {
		if v, ok := raw[field]; ok {
			out[field] = v
		}
	}
	// OpenAI stop → Anthropic stop_sequences
	if _, ok := out["stop_sequences"]; !ok {
		if v, ok := raw["stop"]; ok {
			out["stop_sequences"] = v
		}
	}

	streamVal, _ := json.Marshal(streaming)
	out["stream"] = streamVal

	b, _ := json.Marshal(out)
	return b
}

// glmAnthropicToOpenAIResponse converts a GLM Anthropic Messages response to OpenAI format.
func glmAnthropicToOpenAIResponse(data []byte, model string) []byte {
	var resp struct {
		ID      string `json:"id"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return data
	}

	text := ""
	for _, block := range resp.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}

	finishReason := "stop"
	switch resp.StopReason {
	case "max_tokens":
		finishReason = "length"
	case "tool_use":
		finishReason = "tool_calls"
	}

	openAI := map[string]interface{}{
		"id":     "chatcmpl-" + resp.ID,
		"object": "chat.completion",
		"model":  model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": text},
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]int{
			"prompt_tokens":     resp.Usage.InputTokens,
			"completion_tokens": resp.Usage.OutputTokens,
			"total_tokens":      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}

	out, _ := json.Marshal(openAI)
	return out
}

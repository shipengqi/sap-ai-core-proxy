package openai

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/catalogue"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
	"github.com/shipengqi/sap-ai-core-proxy/internal/stream"
	"github.com/shipengqi/sap-ai-core-proxy/internal/transform"
)

// ChatCompletions proxies POST /openai/v1/chat/completions to SAP AI Core.
// Routes Claude models via Bedrock (/invoke) and GPT models via OpenAI (/chat/completions).
func (h *Handler) ChatCompletions(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorBody("read request body: "+err.Error()))
		return
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		c.JSON(http.StatusBadRequest, errorBody("invalid JSON: "+err.Error()))
		return
	}

	var modelStr string
	if m, ok := raw["model"]; ok {
		_ = json.Unmarshal(m, &modelStr)
	}
	if modelStr == "" {
		c.JSON(http.StatusBadRequest, errorBody("missing required field: model"))
		return
	}
	c.Set("model", modelStr)

	if _, ok := raw["messages"]; !ok {
		c.JSON(http.StatusBadRequest, errorBody("missing required field: messages"))
		return
	}

	var streaming bool
	if s, ok := raw["stream"]; ok {
		_ = json.Unmarshal(s, &streaming)
	}

	// Route Claude models via Bedrock, Gemini models via Gemini API, all others via OpenAI endpoint.
	if catalogue.IsAnthropic(modelStr) {
		h.chatCompletionsAnthropic(c, modelStr, raw, streaming)
		return
	}
	if catalogue.IsGemini(modelStr) {
		h.chatCompletionsGemini(c, modelStr, raw, streaming)
		return
	}

	sapBody := buildSAPChatBody(raw, streaming)

	if streaming {
		dep, err := h.deployments.GetDeployment(c.Request.Context(), modelStr)
		if err != nil {
			c.JSON(http.StatusNotFound, errorBody(err.Error()))
			return
		}
		slog.Info("calling openai model", "model", modelStr, "streaming", true, "deployment_id", dep.ID)
		upstream, err := h.client.DoStreaming(c.Request.Context(), http.MethodPost,
			dep.DeployedURL+"/chat/completions?api-version=2024-02-01", bytes.NewReader(sapBody), nil)
		if err != nil {
			c.JSON(http.StatusBadGateway, errorBody(err.Error()))
			return
		}
		c.Status(http.StatusOK)
		stream.PipeOpenAI(c, upstream)
		return
	}

	slog.Info("calling openai model", "model", modelStr, "streaming", false)
	status, respBody, err := h.deployments.FindAndCall(
		c.Request.Context(), modelStr, 5,
		func(dep *sapclient.Deployment) (int, []byte, error) {
			slog.Debug("trying deployment", "deployment_id", dep.ID, "model", modelStr)
			resp, err := h.client.Do(c.Request.Context(), http.MethodPost,
				dep.DeployedURL+"/chat/completions?api-version=2024-02-01", bytes.NewReader(sapBody), nil)
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
	c.Data(status, "application/json", respBody)
}

// chatCompletionsAnthropic handles Claude models via Bedrock /invoke (non-streaming)
// or /invoke-with-response-stream (streaming), converting between OpenAI and Bedrock
// wire formats.
func (h *Handler) chatCompletionsAnthropic(c *gin.Context, modelStr string, raw map[string]json.RawMessage, streaming bool) {
	bedrockBody := openAIToBedrockBody(raw)

	if streaming {
		dep, err := h.deployments.GetDeployment(c.Request.Context(), modelStr)
		if err != nil {
			c.JSON(http.StatusNotFound, errorBody(err.Error()))
			return
		}
		slog.Info("calling anthropic model via openai surface", "model", modelStr, "streaming", true, "deployment_id", dep.ID)
		upstream, err := h.client.DoStreaming(c.Request.Context(), http.MethodPost,
			dep.DeployedURL+"/invoke-with-response-stream", bytes.NewReader(bedrockBody), nil)
		if err != nil {
			c.JSON(http.StatusBadGateway, errorBody(err.Error()))
			return
		}
		c.Status(http.StatusOK)
		stream.PipeAnthropicToOpenAI(c, upstream, "chatcmpl-"+dep.ID, modelStr)
		return
	}

	slog.Info("calling anthropic model via openai surface", "model", modelStr, "streaming", false)
	status, respBody, err := h.deployments.FindAndCall(
		c.Request.Context(), modelStr, 5,
		func(dep *sapclient.Deployment) (int, []byte, error) {
			slog.Debug("trying deployment", "deployment_id", dep.ID, "model", modelStr)
			resp, err := h.client.Do(c.Request.Context(), http.MethodPost,
				dep.DeployedURL+"/invoke", bytes.NewReader(bedrockBody), nil)
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

	openAIResp := bedrockToOpenAIResponse(respBody, modelStr)
	c.Data(http.StatusOK, "application/json", openAIResp)
}

// chatCompletionsGemini handles Gemini models via the native Gemini generateContent API,
// converting between OpenAI and Gemini wire formats.
func (h *Handler) chatCompletionsGemini(c *gin.Context, modelStr string, raw map[string]json.RawMessage, streaming bool) {
	geminiBody := openAIToGeminiBody(raw)

	if streaming {
		dep, err := h.deployments.GetDeployment(c.Request.Context(), modelStr)
		if err != nil {
			c.JSON(http.StatusNotFound, errorBody(err.Error()))
			return
		}
		slog.Info("calling gemini model via openai surface", "model", modelStr, "streaming", true, "deployment_id", dep.ID)
		upstreamURL := dep.DeployedURL + "/models/" + modelStr + ":streamGenerateContent"
		upstream, err := h.client.DoStreaming(c.Request.Context(), http.MethodPost, upstreamURL, bytes.NewReader(geminiBody), nil)
		if err != nil {
			c.JSON(http.StatusBadGateway, errorBody(err.Error()))
			return
		}
		c.Status(http.StatusOK)
		stream.PipeGeminiToOpenAI(c, upstream, "chatcmpl-"+dep.ID, modelStr)
		return
	}

	slog.Info("calling gemini model via openai surface", "model", modelStr, "streaming", false)
	status, respBody, err := h.deployments.FindAndCall(
		c.Request.Context(), modelStr, 5,
		func(dep *sapclient.Deployment) (int, []byte, error) {
			slog.Debug("trying deployment", "deployment_id", dep.ID, "model", modelStr)
			upstreamURL := dep.DeployedURL + "/models/" + modelStr + ":generateContent"
			resp, err := h.client.Do(c.Request.Context(), http.MethodPost, upstreamURL, bytes.NewReader(geminiBody), nil)
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

	openAIResp := geminiToOpenAIResponse(respBody, modelStr)
	c.Data(http.StatusOK, "application/json", openAIResp)
}

// openAIToBedrockBody converts an OpenAI chat request to SAP AI Core Bedrock format.
func openAIToBedrockBody(raw map[string]json.RawMessage) []byte {
	filtered := make(map[string]json.RawMessage)
	for k, v := range raw {
		if transform.BedrockAllowedFields[k] {
			filtered[k] = v
		}
	}

	// Map max_tokens / max_completion_tokens.
	if _, ok := filtered["max_tokens"]; !ok {
		if v, ok := raw["max_completion_tokens"]; ok {
			filtered["max_tokens"] = v
		} else {
			v, _ := json.Marshal(8192)
			filtered["max_tokens"] = v
		}
	}

	v, _ := json.Marshal("bedrock-2023-05-31")
	filtered["anthropic_version"] = v

	filtered = transform.PromoteSystemMessages(filtered)
	filtered = transform.StripCacheControl(filtered)
	filtered = transform.FlattenSystem(filtered)

	out, _ := json.Marshal(filtered)
	return out
}

// bedrockToOpenAIResponse converts a Bedrock /invoke response to OpenAI chat completion format.
func bedrockToOpenAIResponse(data []byte, model string) []byte {
	var bedrock struct {
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

	if err := json.Unmarshal(data, &bedrock); err != nil {
		return data
	}

	// Concatenate all text blocks.
	content := ""
	for _, block := range bedrock.Content {
		if block.Type == "text" {
			content += block.Text
		}
	}

	var finishReason string
	switch bedrock.StopReason {
	case "max_tokens":
		finishReason = "length"
	case "tool_use":
		finishReason = "tool_calls"
	default:
		finishReason = "stop"
	}

	openAI := map[string]interface{}{
		"id":     "chatcmpl-" + bedrock.ID,
		"object": "chat.completion",
		"model":  model,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]string{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]int{
			"prompt_tokens":     bedrock.Usage.InputTokens,
			"completion_tokens": bedrock.Usage.OutputTokens,
			"total_tokens":      bedrock.Usage.InputTokens + bedrock.Usage.OutputTokens,
		},
	}

	out, _ := json.Marshal(openAI)
	return out
}

// buildSAPChatBody builds the body for SAP AI Core's OpenAI endpoint.
// "model" is excluded — SAP routes by deployment URL.
// max_tokens is renamed to max_completion_tokens.
func buildSAPChatBody(raw map[string]json.RawMessage, streaming bool) []byte {
	sap := make(map[string]json.RawMessage)

	for _, k := range []string{
		"messages", "temperature", "top_p", "n", "stop",
		"presence_penalty", "frequency_penalty", "logit_bias",
		"user", "tools", "tool_choice", "parallel_tool_calls",
		"response_format", "seed", "logprobs", "top_logprobs",
	} {
		if v, ok := raw[k]; ok {
			sap[k] = v
		}
	}

	sap["stream"], _ = json.Marshal(streaming)
	if streaming {
		sap["stream_options"], _ = json.Marshal(map[string]bool{"include_usage": true})
	}

	if v, ok := raw["max_completion_tokens"]; ok {
		sap["max_completion_tokens"] = v
	} else if v, ok := raw["max_tokens"]; ok {
		sap["max_completion_tokens"] = v
	}

	out, _ := json.Marshal(sap)
	return out
}

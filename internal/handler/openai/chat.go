package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/catalogue"
	"github.com/shipengqi/sap-ai-core-proxy/internal/diagnostics"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
	"github.com/shipengqi/sap-ai-core-proxy/internal/stream"
	"github.com/shipengqi/sap-ai-core-proxy/internal/transform"
	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
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

	// Cache attribution. On this surface the system prompt is a message rather
	// than a top-level field, so the system fingerprint stays empty and the
	// system prompt is hashed as part of the message prefix. That is less
	// precise than the Anthropic surface but still catches the drift that
	// matters.
	diagnostics.ObserveRequest(c, body)

	if _, ok := raw["messages"]; !ok {
		c.JSON(http.StatusBadRequest, errorBody("missing required field: messages"))
		return
	}

	var streaming bool
	if s, ok := raw["stream"]; ok {
		_ = json.Unmarshal(s, &streaming)
	}

	// Route by provider: Claude→Bedrock, Gemini→Gemini API, Qwen→Alibaba,
	// Perplexity→pass-through, GLM→Anthropic /messages, all others→OpenAI endpoint.
	if catalogue.IsAnthropic(modelStr) {
		h.chatCompletionsAnthropic(c, modelStr, raw, streaming)
		return
	}
	if catalogue.IsGemini(modelStr) {
		h.chatCompletionsGemini(c, modelStr, raw, streaming)
		return
	}
	if catalogue.IsQwen(modelStr) {
		h.chatCompletionsQwen(c, modelStr, raw, streaming)
		return
	}
	if catalogue.IsPerplexity(modelStr) {
		h.chatCompletionsPerplexity(c, modelStr, raw, streaming)
		return
	}
	if catalogue.IsGlm(modelStr) {
		h.chatCompletionsGlm(c, modelStr, raw, streaming)
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
		upstream, err := h.chatStream(c.Request.Context(), dep, raw)
		if err != nil {
			c.JSON(http.StatusBadGateway, errorBody(err.Error()))
			return
		}
		// SAP can reject a streaming request before emitting any SSE bytes (for
		// example, 429 token-rate limiting). Preserve that HTTP error instead of
		// turning it into a misleading 200 empty stream that clients report as
		// "user cancelled".
		usage.MarkStream(c)
		usage.MarkUpstreamStatus(c, upstream.StatusCode)
		if upstream.StatusCode < http.StatusOK || upstream.StatusCode >= http.StatusMultipleChoices {
			defer func() { _ = upstream.Body.Close() }()
			copyUpstreamErrorHeaders(c, upstream)
			errBody, _ := io.ReadAll(upstream.Body)
			contentType := upstream.Header.Get("Content-Type")
			if contentType == "" {
				contentType = "application/json"
			}
			c.Data(upstream.StatusCode, contentType, errBody)
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
			body := sapBody
			if IsEffortUnsupported(dep.ID) {
				body = buildSAPChatBody(withoutReasoningEffort(raw), false)
			}
			status, respBody, err := h.chatOnce(c.Request.Context(), dep, body)
			if err != nil {
				return 0, nil, err
			}
			if status != http.StatusBadRequest || !IsUnsupportedEffortError(respBody) || IsEffortUnsupported(dep.ID) {
				return status, respBody, nil
			}
			// The model has no reasoning_effort argument at all. Remember that and
			// retry once without it rather than turning a previously working
			// request into a 400 just because the client now sets the field.
			MarkEffortUnsupported(dep.ID)
			return h.chatOnce(c.Request.Context(), dep, buildSAPChatBody(withoutReasoningEffort(raw), false))
		},
	)
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}
	// Observation only: respBody is forwarded unchanged.
	usage.ObserveJSONBody(c, respBody)
	c.Data(status, "application/json", respBody)
}

// chatPath is the OpenAI-compatible chat endpoint on a deployment URL.
const chatPath = "/chat/completions?api-version=2024-02-01"

// chatOnce posts one chat request and buffers the response.
func (h *Handler) chatOnce(ctx context.Context, dep *sapclient.Deployment, body []byte) (int, []byte, error) {
	resp, err := h.client.Do(ctx, http.MethodPost, dep.DeployedURL+chatPath, bytes.NewReader(body), nil)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}

// chatStream posts one streaming chat request, retrying once without
// reasoning_effort when the model does not know the argument at all. The error
// body is buffered and put back, so the caller's non-2xx handling still sees it.
func (h *Handler) chatStream(ctx context.Context, dep *sapclient.Deployment, raw map[string]json.RawMessage) (*http.Response, error) {
	body := buildSAPChatBody(raw, true)
	if IsEffortUnsupported(dep.ID) {
		body = buildSAPChatBody(withoutReasoningEffort(raw), true)
	}
	resp, err := h.client.DoStreaming(ctx, http.MethodPost, dep.DeployedURL+chatPath, bytes.NewReader(body), nil)
	if err != nil || resp.StatusCode != http.StatusBadRequest || IsEffortUnsupported(dep.ID) {
		return resp, err
	}
	if _, sent := raw["reasoning_effort"]; !sent {
		return resp, nil
	}
	errBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !IsUnsupportedEffortError(errBody) {
		resp.Body = io.NopCloser(bytes.NewReader(errBody))
		return resp, nil
	}
	MarkEffortUnsupported(dep.ID)
	body = buildSAPChatBody(withoutReasoningEffort(raw), true)
	return h.client.DoStreaming(ctx, http.MethodPost, dep.DeployedURL+chatPath, bytes.NewReader(body), nil)
}

// withoutReasoningEffort returns raw minus reasoning_effort. When the field is
// absent the map is returned as-is instead of being copied.
func withoutReasoningEffort(raw map[string]json.RawMessage) map[string]json.RawMessage {
	if _, ok := raw["reasoning_effort"]; !ok {
		return raw
	}
	out := make(map[string]json.RawMessage, len(raw))
	for k, v := range raw {
		if k != "reasoning_effort" {
			out[k] = v
		}
	}
	return out
}

func copyUpstreamErrorHeaders(c *gin.Context, upstream *http.Response) {
	for _, name := range []string{"Retry-After", "RateLimit-Limit", "RateLimit-Remaining", "RateLimit-Reset", "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset", "Request-Id", "X-Request-Id"} {
		for _, value := range upstream.Header.Values(name) {
			c.Writer.Header().Add(name, value)
		}
	}
}

// chatCompletionsAnthropic handles Claude models via Bedrock /invoke (non-streaming)
// or /invoke-with-response-stream (streaming), converting between OpenAI and Bedrock
// wire formats.
func (h *Handler) chatCompletionsAnthropic(c *gin.Context, modelStr string, raw map[string]json.RawMessage, streaming bool) {
	// Get deployment first to pass ID for cache checking
	dep, err := h.deployments.GetDeployment(c.Request.Context(), modelStr)
	if err != nil {
		c.JSON(http.StatusNotFound, errorBody(err.Error()))
		return
	}

	clientProvidedCacheControl := transform.HasCacheControl(raw)
	bedrockBody, markerResult := openAIToBedrockBody(raw, dep.ID, h.anthropicAutoCache, clientProvidedCacheControl)

	// Counted once per request, from the first build: the thinking retries below
	// rebuild the same body and would otherwise inflate the counter. The model
	// label is the resolved deployment model name so it joins /stats by_model.
	if markerResult != "" {
		diagnostics.ObserveAutoMarker(usage.SurfaceOpenAI, dep.EffectiveModelName(), markerResult)
	}

	if streaming {
		slog.Info("calling anthropic model via openai surface", "model", modelStr, "streaming", true, "deployment_id", dep.ID)
		upstream, err := h.client.DoStreaming(c.Request.Context(), http.MethodPost,
			dep.DeployedURL+"/invoke-with-response-stream", bytes.NewReader(bedrockBody), nil)
		if err != nil {
			c.JSON(http.StatusBadGateway, errorBody(err.Error()))
			return
		}

		// Avoid a hidden full non-streaming generation as a capability probe. If
		// the stream endpoint rejects adaptive thinking before emitting SSE, retry
		// the same endpoint once without thinking instead.
		_, sentEffort := raw["reasoning_effort"]
		if upstream.StatusCode == http.StatusBadRequest && sentEffort && !IsThinkingUnsupported(dep.ID) {
			errBody, _ := io.ReadAll(upstream.Body)
			_ = upstream.Body.Close()
			if IsAdaptiveThinkingError(errBody) {
				MarkThinkingUnsupported(dep.ID)
				// Retry only: the auto-marker outcome was already counted above.
				bedrockBody, _ = openAIToBedrockBody(raw, dep.ID, h.anthropicAutoCache, clientProvidedCacheControl)
				upstream, err = h.client.DoStreaming(c.Request.Context(), http.MethodPost,
					dep.DeployedURL+"/invoke-with-response-stream", bytes.NewReader(bedrockBody), nil)
				if err != nil {
					c.JSON(http.StatusBadGateway, errorBody(err.Error()))
					return
				}
			} else {
				usage.MarkUpstreamStatus(c, http.StatusBadRequest)
				c.Data(http.StatusBadRequest, "application/json", errBody)
				return
			}
		}

		// Recorded before c.Status(200) locks the client-visible status.
		usage.MarkUpstreamStatus(c, upstream.StatusCode)
		// Surface upstream failures as real HTTP errors. Once the SSE headers are
		// written the status is locked at 200 and the client would only ever see an
		// empty stream, hiding the actual upstream error.
		if upstream.StatusCode != http.StatusOK {
			defer func() { _ = upstream.Body.Close() }()
			errBody, _ := io.ReadAll(upstream.Body)
			c.Data(upstream.StatusCode, "application/json", errBody)
			return
		}
		c.Status(http.StatusOK)
		stream.PipeAnthropicToOpenAI(c, upstream, "chatcmpl-"+dep.ID, modelStr)
		return
	}

	slog.Info("calling anthropic model via openai surface", "model", modelStr, "streaming", false)

	// First attempt with thinking (if not cached as unsupported)
	resp, err := h.client.Do(c.Request.Context(), http.MethodPost,
		dep.DeployedURL+"/invoke", bytes.NewReader(bedrockBody), nil)
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

		// Retry with thinking disabled (cache will skip thinking now). The
		// auto-marker outcome was already counted from the first build.
		bedrockBody, _ = openAIToBedrockBody(raw, dep.ID, h.anthropicAutoCache, clientProvidedCacheControl)
		resp, err = h.client.Do(c.Request.Context(), http.MethodPost,
			dep.DeployedURL+"/invoke", bytes.NewReader(bedrockBody), nil)
		if err != nil {
			c.JSON(http.StatusBadGateway, errorBody(err.Error()))
			return
		}
		defer func() { _ = resp.Body.Close() }()
		respBody, _ = io.ReadAll(resp.Body)
		status = resp.StatusCode
	}

	if status != http.StatusOK {
		c.Data(status, "application/json", respBody)
		return
	}

	openAIResp := bedrockToOpenAIResponse(respBody, modelStr)
	// The converted body carries the Bedrock usage block restated as
	// prompt_tokens / completion_tokens.
	usage.ObserveJSONBody(c, openAIResp)
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
		// Recorded before c.Status(200) locks the client-visible status.
		usage.MarkUpstreamStatus(c, upstream.StatusCode)
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
	// The converted body carries Gemini's usageMetadata restated as
	// prompt_tokens / completion_tokens.
	usage.ObserveJSONBody(c, openAIResp)
	c.Data(http.StatusOK, "application/json", openAIResp)
}

// reasoningBudgets maps OpenAI reasoning_effort values to Anthropic thinking budget_tokens.
var reasoningBudgets = map[string]int{
	"minimal": 1024,
	"low":     2048,
	"medium":  5000,
	"high":    10000,
	"xhigh":   20000,
}

// openAIToBedrockBody converts an OpenAI chat request to SAP AI Core Bedrock format.
// It also returns the auto cache-marker outcome for the caller to count once per
// request, since this function runs again on every thinking retry.
func openAIToBedrockBody(raw map[string]json.RawMessage, deploymentID string, anthropicAutoCache bool, clientProvidedCacheControl bool) ([]byte, string) {
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

	// Convert OpenAI reasoning_effort to Anthropic thinking block.
	// Remove reasoning_effort from the filtered map (it's not in BedrockAllowedFields)
	// and inject the thinking field instead.
	if effortRaw, ok := raw["reasoning_effort"]; ok {
		// Skip thinking if deployment is known to not support it
		if !IsThinkingUnsupported(deploymentID) {
			var effort string
			_ = json.Unmarshal(effortRaw, &effort)
			if effort != "" && effort != "none" {
				budget, ok := reasoningBudgets[effort]
				if !ok {
					budget = reasoningBudgets["medium"]
				}
				thinking, _ := json.Marshal(map[string]interface{}{
					"type":          "enabled",
					"budget_tokens": budget,
				})
				filtered["thinking"] = thinking

				// Anthropic requires budget_tokens < max_tokens.
				// Auto-raise max_tokens when the budget would exceed it.
				var currentMax int
				if mt, ok := filtered["max_tokens"]; ok {
					_ = json.Unmarshal(mt, &currentMax)
				}
				if currentMax <= budget {
					v, _ := json.Marshal(budget + 1024)
					filtered["max_tokens"] = v
				}
			}
		}
	}

	// parallel_tool_calls is OpenAI-only and not in BedrockAllowedFields; carry it
	// through so the tool conversion can fold it into tool_choice.
	if v, ok := raw["parallel_tool_calls"]; ok {
		filtered["parallel_tool_calls"] = v
	}

	// Translate OpenAI tool-calling shapes into Anthropic content blocks. Without
	// this, a "tool" role or an OpenAI-shaped tools array is forwarded verbatim and
	// rejected upstream.
	filtered = transform.ConvertOpenAIToolMessagesToAnthropic(filtered)
	filtered = transform.ConvertOpenAIToolsToAnthropic(filtered)

	filtered = transform.PromoteSystemMessages(filtered)
	filtered = transform.ConvertImagePartsToAnthropic(filtered)
	filtered = transform.FlattenSystem(filtered)
	_, markerResult := transform.ApplyAnthropicToolCacheMarker(filtered, anthropicAutoCache, clientProvidedCacheControl)

	out, _ := json.Marshal(filtered)
	return out, markerResult
}

// bedrockToOpenAIResponse converts a Bedrock /invoke response to OpenAI chat completion format.
func bedrockToOpenAIResponse(data []byte, model string) []byte {
	var bedrock struct {
		ID      string `json:"id"`
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
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

	// Concatenate all text blocks; skip thinking blocks. tool_use blocks become
	// OpenAI tool_calls so a tool-calling loop can continue.
	content := ""
	var toolCalls []map[string]interface{}
	for _, block := range bedrock.Content {
		switch block.Type {
		case "text":
			content += block.Text
		case "tool_use":
			args := "{}"
			if len(block.Input) > 0 && string(block.Input) != "null" {
				args = string(block.Input)
			}
			toolCalls = append(toolCalls, map[string]interface{}{
				"id":   block.ID,
				"type": "function",
				"function": map[string]string{
					"name":      block.Name,
					"arguments": args,
				},
			})
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
	if finishReason == "stop" && len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}

	message := map[string]interface{}{
		"role":    "assistant",
		"content": content,
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
		// OpenAI reports null content when the turn is purely tool calls.
		if content == "" {
			message["content"] = nil
		}
	}

	openAI := map[string]interface{}{
		"id":     "chatcmpl-" + bedrock.ID,
		"object": "chat.completion",
		"model":  model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       message,
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
		// Forwarded verbatim, never injected. Models that only accept function
		// tools on /responses (gpt-6.x) reject a tool-bearing chat request
		// unless the effort is explicitly "none", and the error names this
		// field in "param". Dropping it made that rejection impossible for the
		// client to avoid — it could not set the one value upstream asked for.
		"reasoning_effort",
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

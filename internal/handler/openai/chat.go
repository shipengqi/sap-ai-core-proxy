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
	"github.com/shipengqi/sap-ai-core-proxy/internal/proxy"
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

	if validationErr := validateChat(raw); validationErr != nil {
		c.JSON(400, errorBody(validationErr.Error()))
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

	if streaming {
		dep, err := h.deployments.GetDeployment(c.Request.Context(), modelStr)
		if err != nil {
			c.JSON(http.StatusNotFound, errorBody(err.Error()))
			return
		}
		slog.Info("calling openai model", "model", modelStr, "streaming", true, "deployment_id", dep.ID)
		var viaResponses bool
		upstream, dep, err := h.deployments.CallStreaming(c.Request.Context(), dep, 5, func(candidate *sapclient.Deployment) (*http.Response, error) {
			resp, via, err := h.chatStream(c.Request.Context(), candidate, raw)
			viaResponses = via
			return resp, err
		})
		if err != nil {
			c.JSON(http.StatusBadGateway, errorBody(err.Error()))
			return
		}
		if !proxy.CheckStream(c, upstream) {
			return
		}
		c.Status(http.StatusOK)
		if viaResponses {
			stream.PipeResponsesToOpenAI(c, upstream, "chatcmpl-"+dep.ID, modelStr, wantsStreamUsage(raw))
			return
		}
		stream.PipeOpenAI(c, upstream)
		return
	}

	slog.Info("calling openai model", "model", modelStr, "streaming", false)
	status, respBody, err := h.completeGPT(c.Request.Context(), modelStr, raw)
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
	resp, err := h.client.Do(ctx, http.MethodPost, dep.DeployedURL+chatPath, bytes.NewReader(body), rgHeaders(dep.ResourceGroupID))
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return 0, nil, readErr
	}
	return resp.StatusCode, b, nil
}

// chatStream posts one streaming chat request and applies the two capability
// fallbacks: dropping reasoning_effort when the model does not know it, and
// bridging to /responses when the model refuses function tools here. The second
// return value reports whether the response comes from the Responses API and
// therefore needs a different stream converter.
//
// A 400 error body is buffered and put back on the response when no fallback
// applies, so the caller's generic non-2xx handling still sees it.
func (h *Handler) chatStream(ctx context.Context, dep *sapclient.Deployment, raw map[string]json.RawMessage) (*http.Response, bool, error) {
	if shouldUseResponses(CapabilityKey(dep, chatPath), raw) {
		resp, err := h.responsesStream(ctx, dep, raw)
		return resp, true, err
	}

	body := buildSAPChatBody(raw, true)
	if IsEffortUnsupported(CapabilityKey(dep, chatPath)) {
		body = buildSAPChatBody(withoutReasoningEffort(raw), true)
	}
	resp, err := h.client.DoStreaming(ctx, http.MethodPost, dep.DeployedURL+chatPath, bytes.NewReader(body), rgHeaders(dep.ResourceGroupID))
	if err != nil || resp.StatusCode != http.StatusBadRequest {
		return resp, false, err
	}

	errBody, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, false, readErr
	}

	if _, sent := raw["reasoning_effort"]; sent && !IsEffortUnsupported(CapabilityKey(dep, chatPath)) && IsUnsupportedEffortError(errBody) {
		MarkEffortUnsupported(CapabilityKey(dep, chatPath))
		body = buildSAPChatBody(withoutReasoningEffort(raw), true)
		resp, err = h.client.DoStreaming(ctx, http.MethodPost, dep.DeployedURL+chatPath, bytes.NewReader(body), rgHeaders(dep.ResourceGroupID))
		if err != nil || resp.StatusCode != http.StatusBadRequest {
			return resp, false, err
		}
		errBody, readErr = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, false, readErr
		}
	}
	if hasTools(raw) && IsResponsesRequiredError(errBody) {
		MarkResponsesRequired(CapabilityKey(dep, chatPath))
		slog.Info("model requires /responses for tools, bridging",
			"deployment_id", dep.ID, "streaming", true)
		r, err := h.responsesStream(ctx, dep, raw)
		return r, true, err
	}

	resp.Body = io.NopCloser(bytes.NewReader(errBody))
	return resp, false, nil
}

// responsesStream posts the bridged request to the deployment's Responses
// endpoint.
func (h *Handler) responsesStream(ctx context.Context, dep *sapclient.Deployment, raw map[string]json.RawMessage) (*http.Response, error) {
	if err := validateResponsesBridge(raw); err != nil {
		b, _ := json.Marshal(errorBody(err.Error()))
		return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(b))}, nil
	}
	return h.client.DoStreaming(ctx, http.MethodPost, dep.DeployedURL+responsesPath,
		bytes.NewReader(chatToResponsesBody(raw)), rgHeaders(dep.ResourceGroupID))
}

// chatViaResponses posts the bridged request and converts the answer back into a
// chat.completion, so the caller keeps the surface it asked for.
func (h *Handler) chatViaResponses(ctx context.Context, dep *sapclient.Deployment, raw map[string]json.RawMessage) (int, []byte, error) {
	if err := validateResponsesBridge(raw); err != nil {
		b, _ := json.Marshal(errorBody(err.Error()))
		return 400, b, nil
	}
	resp, err := h.client.Do(ctx, http.MethodPost, dep.DeployedURL+responsesPath,
		bytes.NewReader(chatToResponsesBody(raw)), rgHeaders(dep.ResourceGroupID))
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return 0, nil, readErr
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, b, nil
	}
	var model string
	_ = json.Unmarshal(raw["model"], &model)
	converted := responsesToChatCompletion(b, model)
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	_ = json.Unmarshal(converted, &envelope)
	if len(envelope.Error) > 0 {
		return http.StatusBadGateway, converted, nil
	}
	return resp.StatusCode, converted, nil
}

// hasTools reports whether the request carries a non-empty tools array. The
// bridge only applies to tool-bearing requests: without tools the chat endpoint
// serves these models fine, with reasoning intact.
func hasTools(raw map[string]json.RawMessage) bool {
	v, ok := raw["tools"]
	if !ok {
		return false
	}
	var tools []json.RawMessage
	if json.Unmarshal(v, &tools) != nil {
		return false
	}
	return len(tools) > 0
}

// The learned restriction concerns tools with reasoning. Explicitly disabling
// reasoning can still use chat and must not be replaced with default reasoning
// by a cached bridge decision.
func shouldUseResponses(deploymentID string, raw map[string]json.RawMessage) bool {
	if !IsResponsesRequired(deploymentID) || !hasTools(raw) {
		return false
	}
	var effort string
	_ = json.Unmarshal(raw["reasoning_effort"], &effort)
	return effort != "none"
}

// wantsStreamUsage reports whether the caller asked for the usage block on the
// stream (stream_options.include_usage).
func wantsStreamUsage(raw map[string]json.RawMessage) bool {
	var opts struct {
		IncludeUsage bool `json:"include_usage"`
	}
	if v, ok := raw["stream_options"]; ok {
		_ = json.Unmarshal(v, &opts)
	}
	return opts.IncludeUsage
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

	thinkingPath := "/invoke"
	if streaming {
		thinkingPath = "/invoke-with-response-stream"
	}
	cacheKey := CapabilityKey(dep, thinkingPath)
	if validationErr := validateClaudeChat(raw, cacheKey); validationErr != nil {
		c.JSON(400, errorBody(validationErr.Error()))
		return
	}
	clientProvidedCacheControl := transform.HasCacheControl(raw)
	_, markerResult := openAIToBedrockBody(raw, cacheKey, h.anthropicAutoCache, clientProvidedCacheControl)

	// Counted once per request, from the first build: the thinking retries below
	// rebuild the same body and would otherwise inflate the counter. The model
	// label is the resolved deployment model name so it joins /stats by_model.
	if markerResult != "" {
		diagnostics.ObserveAutoMarker(usage.SurfaceOpenAI, dep.EffectiveModelName(), markerResult)
	}

	if streaming {
		slog.Info("calling anthropic model via openai surface", "model", modelStr, "streaming", true, "deployment_id", dep.ID)
		_, requested := raw["reasoning_effort"]
		upstream, dep, err := h.deployments.CallStreaming(c.Request.Context(), dep, 5, func(candidate *sapclient.Deployment) (*http.Response, error) {
			key := CapabilityKey(candidate, thinkingPath)
			if err := validateClaudeChat(raw, key); err != nil {
				b, _ := json.Marshal(errorBody(err.Error()))
				return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(b))}, nil
			}
			return h.thinkingStream(c.Request.Context(), candidate, thinkingPath, key, requested, func() []byte {
				body, _ := openAIToBedrockBody(raw, key, h.anthropicAutoCache, clientProvidedCacheControl)
				return body
			})
		})
		if err != nil {
			c.JSON(502, errorBody(err.Error()))
			return
		}

		if !proxy.CheckStream(c, upstream) {
			return
		}
		c.Status(http.StatusOK)
		stream.PipeAnthropicToOpenAI(c, upstream, "chatcmpl-"+dep.ID, modelStr, wantsStreamUsage(raw))
		return
	}

	slog.Info("calling anthropic model via openai surface", "model", modelStr, "streaming", false)

	status, respBody, err := h.deployments.FindAndCall(c.Request.Context(), modelStr, 5, func(candidate *sapclient.Deployment) (int, []byte, error) {
		key := CapabilityKey(candidate, "/invoke")
		if err := validateClaudeChat(raw, key); err != nil {
			b, _ := json.Marshal(errorBody(err.Error()))
			return 400, b, nil
		}
		call := func() (int, []byte, error) {
			body, _ := openAIToBedrockBody(raw, key, h.anthropicAutoCache, clientProvidedCacheControl)
			resp, err := h.client.Do(c.Request.Context(), http.MethodPost, candidate.DeployedURL+"/invoke", bytes.NewReader(body), rgHeaders(candidate.ResourceGroupID))
			if err != nil {
				return 0, nil, err
			}
			defer func() { _ = resp.Body.Close() }()
			proxy.CopyHeaders(c, resp)
			b, err := io.ReadAll(resp.Body)
			return resp.StatusCode, b, err
		}
		status, b, err := call()
		if _, requested := raw["reasoning_effort"]; requested && err == nil && status == 400 && !IsThinkingUnsupported(key) && IsAdaptiveThinkingError(b) {
			MarkThinkingUnsupported(key)
			return call()
		}
		return status, b, err
	})
	if err != nil {
		c.JSON(502, errorBody(err.Error()))
		return
	}

	if status != http.StatusOK {
		c.Data(status, "application/json", respBody)
		return
	}

	openAIResp := bedrockToOpenAIResponse(respBody, modelStr)
	// The converted body carries the Bedrock usage block restated as
	// prompt_tokens / completion_tokens.
	usage.ObserveJSONBody(c, openAIResp)
	var check struct {
		Error json.RawMessage `json:"error"`
	}
	_ = json.Unmarshal(openAIResp, &check)
	if len(check.Error) > 0 {
		c.Data(502, "application/json", openAIResp)
		return
	}
	c.Data(http.StatusOK, "application/json", openAIResp)
}

// chatCompletionsGemini handles Gemini models via the native Gemini generateContent API,
// converting between OpenAI and Gemini wire formats.
func (h *Handler) chatCompletionsGemini(c *gin.Context, modelStr string, raw map[string]json.RawMessage, streaming bool) {
	geminiBody, convertErr := transform.OpenAIToGeminiBody(raw)
	if convertErr != nil {
		c.JSON(400, errorBody(convertErr.Error()))
		return
	}

	if streaming {
		dep, err := h.deployments.GetDeployment(c.Request.Context(), modelStr)
		if err != nil {
			c.JSON(http.StatusNotFound, errorBody(err.Error()))
			return
		}
		slog.Info("calling gemini model via openai surface", "model", modelStr, "streaming", true, "deployment_id", dep.ID)
		upstream, dep, err := h.deployments.CallStreaming(c.Request.Context(), dep, 5, func(candidate *sapclient.Deployment) (*http.Response, error) {
			upstreamURL := candidate.DeployedURL + "/models/" + geminiModelName(candidate) + ":streamGenerateContent"
			return h.client.DoStreaming(c.Request.Context(), http.MethodPost, upstreamURL, bytes.NewReader(geminiBody), rgHeaders(candidate.ResourceGroupID))
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
		stream.PipeGeminiToOpenAI(c, upstream, "chatcmpl-"+dep.ID, modelStr, wantsStreamUsage(raw))
		return
	}

	slog.Info("calling gemini model via openai surface", "model", modelStr, "streaming", false)
	status, respBody, err := h.deployments.FindAndCall(
		c.Request.Context(), modelStr, 5,
		func(dep *sapclient.Deployment) (int, []byte, error) {
			slog.Debug("trying deployment", "deployment_id", dep.ID, "model", modelStr)
			upstreamURL := dep.DeployedURL + "/models/" + geminiModelName(dep) + ":generateContent"
			resp, err := h.client.Do(c.Request.Context(), http.MethodPost, upstreamURL, bytes.NewReader(geminiBody), nil)
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

	openAIResp := geminiToOpenAIResponse(respBody, modelStr)
	// The converted body carries Gemini's usageMetadata restated as
	// prompt_tokens / completion_tokens.
	usage.ObserveJSONBody(c, openAIResp)
	var check struct {
		Error json.RawMessage `json:"error"`
	}
	_ = json.Unmarshal(openAIResp, &check)
	if len(check.Error) > 0 {
		c.Data(502, "application/json", openAIResp)
		return
	}
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

	// max_completion_tokens takes precedence when both limits are supplied.
	if v, ok := raw["max_completion_tokens"]; ok {
		filtered["max_tokens"] = v
	} else if _, ok := filtered["max_tokens"]; !ok {
		filtered["max_tokens"] = json.RawMessage(`8192`)
	}

	if v, ok := raw["stop"]; ok {
		var text string
		if json.Unmarshal(v, &text) == nil {
			filtered["stop_sequences"], _ = json.Marshal([]string{text})
		} else {
			filtered["stop_sequences"] = v
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
				if currentMax <= budget && raw["max_tokens"] == nil && raw["max_completion_tokens"] == nil {
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
		ID      string          `json:"id"`
		Error   json.RawMessage `json:"error"`
		Content []struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			Thinking string          `json:"thinking"`
			ID       string          `json:"id"`
			Name     string          `json:"name"`
			Input    json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens   int `json:"input_tokens"`
			OutputTokens  int `json:"output_tokens"`
			CacheRead     int `json:"cache_read_input_tokens"`
			CacheCreation int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(data, &bedrock); err != nil {
		return bridgeResponseError("invalid upstream Anthropic JSON", nil)
	}

	if len(bedrock.Error) > 0 && string(bedrock.Error) != "null" {
		return data
	}
	if bedrock.ID == "" && len(bedrock.Content) == 0 {
		return bridgeResponseError("upstream Anthropic response contains no message", nil)
	}

	// Concatenate text and reasoning blocks. tool_use blocks become
	// OpenAI tool_calls so a tool-calling loop can continue.
	content, reasoning := "", ""
	var toolCalls []map[string]interface{}
	for _, block := range bedrock.Content {
		switch block.Type {
		case "text":
			content += block.Text
		case "thinking":
			reasoning += block.Thinking
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
	if reasoning != "" {
		message["reasoning_content"] = reasoning
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
		"usage": map[string]interface{}{
			"prompt_tokens":         bedrock.Usage.InputTokens + bedrock.Usage.CacheRead + bedrock.Usage.CacheCreation,
			"completion_tokens":     bedrock.Usage.OutputTokens,
			"total_tokens":          bedrock.Usage.InputTokens + bedrock.Usage.CacheRead + bedrock.Usage.CacheCreation + bedrock.Usage.OutputTokens,
			"prompt_tokens_details": map[string]int{"cached_tokens": bedrock.Usage.CacheRead},
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

package anthropic

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/catalogue"
	"github.com/shipengqi/sap-ai-core-proxy/internal/handler/openai"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
	"github.com/shipengqi/sap-ai-core-proxy/internal/stream"
	"github.com/shipengqi/sap-ai-core-proxy/internal/transform"
)

// Messages proxies POST /anthropic/v1/messages to SAP AI Core.
// Routes Claude models via Bedrock (/invoke) and all other models via OpenAI (/chat/completions).
func (h *Handler) Messages(c *gin.Context) {
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

	var streaming bool
	if s, ok := raw["stream"]; ok {
		_ = json.Unmarshal(s, &streaming)
	}

	if catalogue.IsAnthropic(modelStr) {
		h.messagesBedrock(c, modelStr, raw, streaming)
	} else {
		h.messagesOpenAI(c, modelStr, raw, streaming)
	}
}

// messagesBedrock handles Claude models via SAP AI Core Bedrock /invoke endpoints.
func (h *Handler) messagesBedrock(c *gin.Context, modelName string, raw map[string]json.RawMessage, streaming bool) {
	// Get deployment first for thinking cache check
	dep, err := h.deployments.GetDeployment(c.Request.Context(), modelName)
	if err != nil {
		c.JSON(http.StatusNotFound, errorBody(err.Error()))
		return
	}

	// Build request body with thinking parameter handling
	buildBody := func() ([]byte, error) {
		filtered := make(map[string]json.RawMessage)
		for k, v := range raw {
			// Skip thinking parameter - we'll handle it separately based on cache
			if k == "thinking" {
				continue
			}
			if transform.BedrockAllowedFields[k] {
				filtered[k] = v
			}
		}

		if _, ok := filtered["anthropic_version"]; !ok {
			v, _ := json.Marshal("bedrock-2023-05-31")
			filtered["anthropic_version"] = v
		}
		if _, ok := filtered["max_tokens"]; !ok {
			v, _ := json.Marshal(8192)
			filtered["max_tokens"] = v
		}

		// Handle thinking parameter with cache check
		if thinkingRaw, ok := raw["thinking"]; ok {
			// Only add thinking if deployment supports it (not in unsupported cache)
			if !openai.IsThinkingUnsupported(dep.ID) {
				filtered["thinking"] = thinkingRaw
			}
		}

		filtered = transform.PromoteSystemMessages(filtered)
		filtered = transform.ConvertImagePartsToAnthropic(filtered)
		filtered = transform.FlattenSystem(filtered)

		return json.Marshal(filtered)
	}

	filteredBody, err := buildBody()
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorBody("marshal filtered body: "+err.Error()))
		return
	}

	if streaming {
		// Auto-detect thinking support before streaming if needed
		if _, hasThinking := raw["thinking"]; hasThinking && !openai.IsThinkingUnsupported(dep.ID) {
			slog.Info("probing thinking support before streaming", "model", modelName, "deployment_id", dep.ID)

			// Build minimal probe request (same body as actual request)
			probeResp, err := h.client.Do(c.Request.Context(), http.MethodPost,
				dep.DeployedURL+"/invoke", bytes.NewReader(filteredBody), nil)
			if err != nil {
				c.JSON(http.StatusBadGateway, errorBody(err.Error()))
				return
			}
			probeBody, _ := io.ReadAll(probeResp.Body)
			_ = probeResp.Body.Close()

			// Check if thinking is unsupported
			if probeResp.StatusCode == http.StatusBadRequest && openai.IsAdaptiveThinkingError(probeBody) {
				slog.Info("detected thinking not supported, caching result", "deployment_id", dep.ID)
				openai.MarkThinkingUnsupported(dep.ID)

				// Rebuild body without thinking
				filteredBody, err = buildBody()
				if err != nil {
					c.JSON(http.StatusInternalServerError, errorBody("rebuild body: "+err.Error()))
					return
				}
			} else if probeResp.StatusCode != http.StatusOK {
				// Probe failed for other reasons, return error
				c.Data(probeResp.StatusCode, "application/json", probeBody)
				return
			}
			// Probe succeeded (200 OK), thinking is supported - proceed with streaming
		}

		slog.Info("calling anthropic model", "model", modelName, "streaming", true, "deployment_id", dep.ID)
		upstream, err := h.client.DoStreaming(c.Request.Context(), http.MethodPost,
			dep.DeployedURL+"/invoke-with-response-stream", bytes.NewReader(filteredBody), nil)
		if err != nil {
			c.JSON(http.StatusBadGateway, errorBody(err.Error()))
			return
		}
		c.Status(http.StatusOK)
		stream.PipeAnthropic(c, upstream)
		return
	}

	slog.Info("calling anthropic model", "model", modelName, "streaming", false)

	// First attempt with thinking (if not cached as unsupported)
	resp, err := h.client.Do(c.Request.Context(), http.MethodPost,
		dep.DeployedURL+"/invoke", bytes.NewReader(filteredBody), nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(resp.Body)
	status := resp.StatusCode

	// Check if we need to retry without thinking
	if status == http.StatusBadRequest && openai.IsAdaptiveThinkingError(respBody) {
		slog.Info("deployment doesn't support adaptive thinking, retrying without it",
			"model", modelName, "deployment_id", dep.ID)

		openai.MarkThinkingUnsupported(dep.ID)

		// Retry with thinking disabled (cache will skip thinking now)
		filteredBody, err = buildBody()
		if err != nil {
			c.JSON(http.StatusInternalServerError, errorBody("marshal retry body: "+err.Error()))
			return
		}

		resp, err = h.client.Do(c.Request.Context(), http.MethodPost,
			dep.DeployedURL+"/invoke", bytes.NewReader(filteredBody), nil)
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

// messagesOpenAI handles non-Claude models via SAP AI Core OpenAI-compatible endpoint.
// Converts Anthropic request → OpenAI, calls /chat/completions, converts OpenAI response → Anthropic.
func (h *Handler) messagesOpenAI(c *gin.Context, modelName string, raw map[string]json.RawMessage, streaming bool) {
	if streaming {
		c.JSON(http.StatusBadRequest, errorBody(
			"streaming non-Claude models via /anthropic/v1/messages is not supported; use /openai/v1/chat/completions with stream:true instead",
		))
		return
	}

	sapBody := transform.AnthropicToOpenAIBody(raw, false)

	slog.Info("calling non-anthropic model via anthropic surface", "model", modelName)
	status, respBody, err := h.deployments.FindAndCall(
		c.Request.Context(), modelName, 5,
		func(dep *sapclient.Deployment) (int, []byte, error) {
			slog.Debug("trying deployment", "deployment_id", dep.ID, "model", modelName)
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
	if status != http.StatusOK {
		c.Data(status, "application/json", respBody)
		return
	}

	anthropicResp := transform.OpenAIToAnthropicResponse(respBody, modelName)
	c.Data(http.StatusOK, "application/json", anthropicResp)
}

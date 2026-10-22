package anthropic

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/catalogue"
	"github.com/shipengqi/sap-ai-core-proxy/internal/compress"
	"github.com/shipengqi/sap-ai-core-proxy/internal/diagnostics"
	"github.com/shipengqi/sap-ai-core-proxy/internal/handler/openai"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
	"github.com/shipengqi/sap-ai-core-proxy/internal/stream"
	"github.com/shipengqi/sap-ai-core-proxy/internal/transform"
	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
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

	// Cache attribution: fingerprint the hot zone before any transformation runs,
	// so the comparison reflects what the client sent. Read-only.
	diagnostics.ObserveRequest(c, body)

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
	clientProvidedCacheControl := transform.HasCacheControl(raw)

	// Get deployment first for thinking cache check
	dep, err := h.deployments.GetDeployment(c.Request.Context(), modelName)
	if err != nil {
		c.JSON(http.StatusNotFound, errorBody(err.Error()))
		return
	}

	// Compression runs once, before the body builder, so a retry rebuilds the
	// same bytes instead of compressing twice. It covers every tool_result in
	// the request, history included: the transform is deterministic, so the
	// compressed history is byte-identical on every turn and the provider cache
	// still hits. Compressing only the live zone would leave the next turn's
	// history uncompressed and bust the prefix at that point.
	if compress.Enabled() {
		var report compress.Report
		raw, report = compress.CompressRequest(raw)
		compress.Observe(usage.SurfaceAnthropic, report)
		if report.BlocksCompressed > 0 {
			slog.Debug("compressed tool results", "model", modelName,
				"blocks", report.BlocksCompressed,
				"bytes_before", report.OriginalBytes,
				"bytes_after", report.CompressedBytes,
				"strategy", report.Strategy)
		}
	}

	// The auto-marker counter is per request, not per body build: buildBody runs
	// again when a thinking retry rebuilds the payload, and the same request must
	// not be counted twice. The model label is the resolved deployment model name
	// so it joins /stats by_model instead of fragmenting on the requested alias.
	markerRecorded := false
	markerModel := dep.EffectiveModelName()

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
		_, markerResult := transform.ApplyAnthropicToolCacheMarker(filtered, h.anthropicAutoCache, clientProvidedCacheControl)
		if !markerRecorded && markerResult != "" {
			markerRecorded = true
			diagnostics.ObserveAutoMarker(usage.SurfaceAnthropic, markerModel, markerResult)
		}

		return json.Marshal(filtered)
	}

	filteredBody, err := buildBody()
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorBody("marshal filtered body: "+err.Error()))
		return
	}

	if streaming {
		slog.Info("calling anthropic model", "model", modelName, "streaming", true, "deployment_id", dep.ID)
		upstream, err := h.client.DoStreaming(c.Request.Context(), http.MethodPost,
			dep.DeployedURL+"/invoke-with-response-stream", bytes.NewReader(filteredBody), nil)
		if err != nil {
			c.JSON(http.StatusBadGateway, errorBody(err.Error()))
			return
		}

		// Do not probe thinking support with a full non-streaming generation before
		// opening the stream. Claude Code cancels while waiting for that hidden
		// request to finish. A rejected streaming request has not emitted SSE yet,
		// so it is safe to strip thinking and retry here.
		_, sentThinking := raw["thinking"]
		if upstream.StatusCode == http.StatusBadRequest && sentThinking && !openai.IsThinkingUnsupported(dep.ID) {
			errBody, _ := io.ReadAll(upstream.Body)
			_ = upstream.Body.Close()
			if openai.IsAdaptiveThinkingError(errBody) {
				openai.MarkThinkingUnsupported(dep.ID)
				filteredBody, err = buildBody()
				if err != nil {
					c.JSON(http.StatusInternalServerError, errorBody("rebuild body: "+err.Error()))
					return
				}
				upstream, err = h.client.DoStreaming(c.Request.Context(), http.MethodPost,
					dep.DeployedURL+"/invoke-with-response-stream", bytes.NewReader(filteredBody), nil)
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

		// Preserve the native Anthropic streaming contract: the client-facing status
		// is 200 once stream handling begins, while metrics retain the real upstream
		// status for failures.
		usage.MarkUpstreamStatus(c, upstream.StatusCode)
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

	// Token accounting: the upstream usage block is read out of the response we
	// are about to forward. Observation only — respBody is passed through byte
	// for byte.
	usage.ObserveJSONBody(c, respBody)
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
	// The converted body is what the client receives and it carries the same
	// token counts as the upstream OpenAI answer.
	usage.ObserveJSONBody(c, anthropicResp)
	c.Data(http.StatusOK, "application/json", anthropicResp)
}

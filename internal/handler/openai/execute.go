package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/shipengqi/sap-ai-core-proxy/internal/catalogue"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
	"github.com/shipengqi/sap-ai-core-proxy/internal/transform"
)

func (h *Handler) completeGPT(ctx context.Context, model string, raw map[string]json.RawMessage) (int, []byte, error) {
	sapBody := buildSAPChatBody(raw, false)
	status, respBody, err := h.deployments.FindAndCall(
		ctx, model, 5,
		func(dep *sapclient.Deployment) (int, []byte, error) {
			slog.Debug("trying deployment", "deployment_id", dep.ID, "model", model)
			if shouldUseResponses(CapabilityKey(dep, chatPath), raw) {
				return h.chatViaResponses(ctx, dep, raw)
			}
			body := sapBody
			if IsEffortUnsupported(CapabilityKey(dep, chatPath)) {
				body = buildSAPChatBody(withoutReasoningEffort(raw), false)
			}
			status, respBody, err := h.chatOnce(ctx, dep, body)
			if err != nil {
				return 0, nil, err
			}
			if status != http.StatusBadRequest {
				return status, respBody, nil
			}
			if _, sent := raw["reasoning_effort"]; sent && !IsEffortUnsupported(CapabilityKey(dep, chatPath)) && IsUnsupportedEffortError(respBody) {
				// The model has no reasoning_effort argument at all. Remember that
				// and retry once without it rather than turning a previously
				// working request into a 400 because the client sets the field.
				MarkEffortUnsupported(CapabilityKey(dep, chatPath))
				status, respBody, err = h.chatOnce(ctx, dep, buildSAPChatBody(withoutReasoningEffort(raw), false))
				if err != nil || status != http.StatusBadRequest {
					return status, respBody, err
				}
			}
			if hasTools(raw) && IsResponsesRequiredError(respBody) {
				// The model accepts function tools only on /responses. Bridge the
				// request there and hand the caller a chat.completion back.
				MarkResponsesRequired(CapabilityKey(dep, chatPath))
				slog.Info("model requires /responses for tools, bridging",
					"model", model, "deployment_id", dep.ID)
				return h.chatViaResponses(ctx, dep, raw)
			}
			return status, respBody, nil
		},
	)
	if err == nil && status >= 200 && status < 300 {
		var envelope struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(respBody, &envelope) != nil {
			return 502, bridgeResponseError("invalid upstream Chat JSON", nil), nil
		}
		if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
			return 502, bridgeResponseError("upstream Chat error", envelope.Error), nil
		}
	}
	return status, respBody, err
}

// Complete lets other client surfaces reuse provider routing and capability fallbacks.
func (h *Handler) Complete(ctx context.Context, raw map[string]json.RawMessage) (int, []byte, error) {
	if err := validateChat(raw); err != nil {
		b, _ := json.Marshal(errorBody(err.Error()))
		return 400, b, nil
	}
	var model string
	_ = json.Unmarshal(raw["model"], &model)
	if model == "" {
		b, _ := json.Marshal(errorBody("missing required field: model"))
		return 400, b, nil
	}
	if !catalogue.IsGemini(model) && !catalogue.IsQwen(model) && !catalogue.IsGlm(model) && !catalogue.IsPerplexity(model) {
		return h.completeGPT(ctx, model, raw)
	}
	return h.deployments.FindAndCall(ctx, model, 5, func(dep *sapclient.Deployment) (int, []byte, error) {
		var body []byte
		var path string
		switch {
		case catalogue.IsGemini(model):
			var err error
			body, err = transform.OpenAIToGeminiBody(raw)
			if err != nil {
				b, _ := json.Marshal(errorBody(err.Error()))
				return 400, b, nil
			}
			path = "/models/" + geminiModelName(dep) + ":generateContent"
		case catalogue.IsQwen(model):
			body, _ = json.Marshal(applyQwenThinking(raw, CapabilityKey(dep, "/chat/completions")))
			path = "/chat/completions"
		case catalogue.IsGlm(model):
			if err := validateGLMChat(raw); err != nil {
				b, _ := json.Marshal(errorBody(err.Error()))
				return 400, b, nil
			}
			body = openAIToGLMBody(raw, model, false)
			path = "/messages"
		default:
			body, _ = json.Marshal(raw)
			path = "/chat/completions?api-version=2024-12-01-preview"
		}
		resp, err := h.client.Do(ctx, http.MethodPost, dep.DeployedURL+path, bytes.NewReader(body), rgHeaders(dep.ResourceGroupID))
		if err != nil {
			return 0, nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return 0, nil, err
		}
		if resp.StatusCode == 400 && catalogue.IsQwen(model) && IsAdaptiveThinkingError(b) {
			MarkThinkingUnsupported(CapabilityKey(dep, "/chat/completions"))
			body, _ = json.Marshal(applyQwenThinking(raw, CapabilityKey(dep, "/chat/completions")))
			retry, e := h.client.Do(ctx, http.MethodPost, dep.DeployedURL+path, bytes.NewReader(body), rgHeaders(dep.ResourceGroupID))
			if e != nil {
				return 0, nil, e
			}
			defer func() { _ = retry.Body.Close() }()
			b, err = io.ReadAll(retry.Body)
			resp = retry
			if err != nil {
				return 0, nil, err
			}
		}
		if resp.StatusCode != 200 {
			return resp.StatusCode, b, nil
		}
		if catalogue.IsGemini(model) {
			b = geminiToOpenAIResponse(b, model)
		} else if catalogue.IsGlm(model) {
			b = glmAnthropicToOpenAIResponse(b, model)
		}
		var envelope struct {
			Error json.RawMessage `json:"error"`
		}
		_ = json.Unmarshal(b, &envelope)
		if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
			return 502, b, nil
		}
		return resp.StatusCode, b, nil
	})
}

// thinkingStream retries only an explicit parameter rejection received before SSE.
func (h *Handler) thinkingStream(ctx context.Context, dep *sapclient.Deployment, path, key string, requested bool, build func() []byte) (*http.Response, error) {
	call := func() (*http.Response, error) {
		return h.client.DoStreaming(ctx, http.MethodPost, dep.DeployedURL+path, bytes.NewReader(build()), rgHeaders(dep.ResourceGroupID))
	}
	resp, err := call()
	if err != nil || resp.StatusCode != 400 || !requested || IsThinkingUnsupported(key) {
		return resp, err
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if IsAdaptiveThinkingError(body) {
		MarkThinkingUnsupported(key)
		return call()
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

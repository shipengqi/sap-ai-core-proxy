package openai

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
	"github.com/shipengqi/sap-ai-core-proxy/internal/stream"
)

// ChatCompletions proxies POST /openai/v1/chat/completions to SAP AI Core.
// SAP AI Core does NOT accept the "model" field — routing is by deployment URL.
// max_tokens is normalized to max_completion_tokens.
// On 404/Gone the deployment is blacklisted and the next matching one is tried.
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

	if _, ok := raw["messages"]; !ok {
		c.JSON(http.StatusBadRequest, errorBody("missing required field: messages"))
		return
	}

	var streaming bool
	if s, ok := raw["stream"]; ok {
		_ = json.Unmarshal(s, &streaming)
	}

	sapBody := buildSAPChatBody(raw, streaming)

	if streaming {
		dep, err := h.deployments.GetDeployment(c.Request.Context(), modelStr)
		if err != nil {
			c.JSON(http.StatusNotFound, errorBody(err.Error()))
			return
		}
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

	status, respBody, err := h.deployments.FindAndCall(
		c.Request.Context(), modelStr, 5,
		func(dep *sapclient.Deployment) (int, []byte, error) {
			resp, err := h.client.Do(c.Request.Context(), http.MethodPost,
				dep.DeployedURL+"/chat/completions?api-version=2024-02-01", bytes.NewReader(sapBody), nil)
			if err != nil {
				return 0, nil, err
			}
			defer resp.Body.Close()
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

// buildSAPChatBody builds the body for SAP AI Core's OpenAI endpoint.
// "model" is excluded — SAP routes by deployment URL and rejects it.
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

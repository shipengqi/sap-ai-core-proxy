package litellm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/proxy"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
	"github.com/shipengqi/sap-ai-core-proxy/internal/stream"
	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

type Handler struct {
	client      *sapclient.Client
	deployments *sapclient.DeploymentManager
}

func NewHandler(client *sapclient.Client, deployments *sapclient.DeploymentManager) *Handler {
	return &Handler{client: client, deployments: deployments}
}

type modelObj struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

func (h *Handler) ListModels(c *gin.Context) {
	deps, err := h.deployments.ListAll(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}
	var models []modelObj
	for _, d := range deps {
		models = append(models, modelObj{ID: d.EffectiveModelName(), Type: "model"})
	}
	c.JSON(http.StatusOK, gin.H{"data": models, "object": "list"})
}

func (h *Handler) ModelInfo(c *gin.Context) {
	dep, err := h.deployments.GetOrchestrationDeployment(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusNotFound, errorBody(err.Error()))
		return
	}
	upstream, err := h.client.Do(c.Request.Context(), http.MethodGet,
		dep.DeployedURL+"/v2/inference/deployments", nil, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}
	defer func() { _ = upstream.Body.Close() }()
	respBody, readErr := io.ReadAll(upstream.Body)
	if readErr != nil {
		c.JSON(502, errorBody(readErr.Error()))
		return
	}
	c.Data(upstream.StatusCode, "application/json", respBody)
}

func buildOrchestrationBody(model string, raw map[string]json.RawMessage, streaming bool) ([]byte, error) {
	promptObj := map[string]json.RawMessage{"template": raw["messages"]}
	if raw["tools"] != nil {
		promptObj["tools"] = raw["tools"]
	}
	if raw["response_format"] != nil {
		promptObj["response_format"] = raw["response_format"]
	}
	params := make(map[string]json.RawMessage)
	for key, value := range raw {
		if orchestrationModelParameter(key) {
			params[key] = value
		}
	}
	if value, ok := raw["max_completion_tokens"]; ok {
		params["max_completion_tokens"] = value
	} else if value, ok := raw["max_tokens"]; ok {
		params["max_completion_tokens"] = value
	}
	if streaming {
		// Match SAP's SDK: collect usage upstream even when the client doesn't
		// request it. The stream adapter controls client-visible usage separately.
		params["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	}
	modelConfig := map[string]any{"name": model}
	if len(params) > 0 {
		modelConfig["params"] = params
	}
	body := map[string]interface{}{
		"config": map[string]interface{}{
			"modules": map[string]interface{}{
				"prompt_templating": map[string]interface{}{
					"prompt": promptObj,
					"model":  modelConfig,
				},
			},
			"stream": map[string]bool{"enabled": streaming},
		},
	}
	return json.Marshal(body)
}

// These OpenAI fields belong in model.params, not in the prompt template or
// the global Orchestration stream config. Model-specific validation is SAP's.
func orchestrationModelParameter(key string) bool {
	switch key {
	case "temperature", "top_p", "n", "stop", "presence_penalty", "frequency_penalty",
		"logit_bias", "user", "tool_choice", "parallel_tool_calls", "seed", "logprobs",
		"top_logprobs", "reasoning_effort":
		return true
	default:
		return false
	}
}

func (h *Handler) streamOrchestration(c *gin.Context, dep *sapclient.Deployment, reqBody []byte, includeUsage bool) {
	headers := map[string]string{"Accept": "text/event-stream"}
	if dep.ResourceGroupID != "" {
		headers["AI-Resource-Group"] = dep.ResourceGroupID
	}
	upstream, err := h.client.DoStreaming(c.Request.Context(), http.MethodPost,
		dep.DeployedURL+"/v2/completion", bytes.NewReader(reqBody),
		headers)
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}
	if !proxy.CheckStream(c, upstream) {
		return
	}
	c.Status(http.StatusOK)
	stream.PipeOrchestrationToOpenAI(c, upstream, includeUsage)
}

func parseOrchestrationResponse(data []byte) (json.RawMessage, error) {
	var env struct {
		FinalResult json.RawMessage `json:"final_result"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("parse orchestration envelope: %w", err)
	}
	if env.FinalResult != nil {
		return env.FinalResult, nil
	}
	return data, nil
}

func (h *Handler) doOrchestration(c *gin.Context, dep *sapclient.Deployment, reqBody []byte) {
	upstreamURL := dep.DeployedURL + "/v2/completion"
	upstream, err := h.client.Do(c.Request.Context(), http.MethodPost, upstreamURL,
		bytes.NewReader(reqBody), nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}
	defer func() { _ = upstream.Body.Close() }()
	respBody, readErr := io.ReadAll(upstream.Body)
	if readErr != nil {
		c.JSON(502, errorBody(readErr.Error()))
		return
	}

	if upstream.StatusCode != http.StatusOK {
		c.Data(upstream.StatusCode, "application/json", respBody)
		return
	}

	result, err := parseOrchestrationResponse(respBody)
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}
	// The orchestration envelope's final_result is an OpenAI-shaped completion,
	// so the usage block sits at its top level. Observation only.
	usage.ObserveJSONBody(c, result)
	c.Data(http.StatusOK, "application/json", result)
}

func errorBody(msg string) gin.H {
	return gin.H{"error": gin.H{"message": msg, "type": "proxy_error"}}
}

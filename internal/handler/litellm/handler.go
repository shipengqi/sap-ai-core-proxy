package litellm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
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
	defer upstream.Body.Close()
	respBody, _ := io.ReadAll(upstream.Body)
	c.Data(upstream.StatusCode, "application/json", respBody)
}

func buildOrchestrationBody(model string, messages, tools, responseFormat json.RawMessage) ([]byte, error) {
	promptObj := map[string]json.RawMessage{"template": messages}
	if tools != nil {
		promptObj["tools"] = tools
	}
	if responseFormat != nil {
		promptObj["response_format"] = responseFormat
	}
	body := map[string]interface{}{
		"config": map[string]interface{}{
			"modules": map[string]interface{}{
				"prompt_templating": map[string]interface{}{
					"prompt": promptObj,
					"model":  map[string]string{"name": model},
				},
			},
			"stream": map[string]bool{"enabled": false},
		},
	}
	return json.Marshal(body)
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
	defer upstream.Body.Close()
	respBody, _ := io.ReadAll(upstream.Body)

	if upstream.StatusCode != http.StatusOK {
		c.Data(upstream.StatusCode, "application/json", respBody)
		return
	}

	result, err := parseOrchestrationResponse(respBody)
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}
	c.Data(http.StatusOK, "application/json", result)
}

func errorBody(msg string) gin.H {
	return gin.H{"error": gin.H{"message": msg, "type": "proxy_error"}}
}

func rgHeaders(rg string) map[string]string {
	if rg == "" {
		return nil
	}
	return map[string]string{"AI-Resource-Group": rg}
}

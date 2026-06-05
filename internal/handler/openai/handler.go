package openai

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
)

// Handler groups the shared SAP client dependencies for OpenAI-surface endpoints.
type Handler struct {
	client      *sapclient.Client
	deployments *sapclient.DeploymentManager
}

func NewHandler(client *sapclient.Client, deployments *sapclient.DeploymentManager) *Handler {
	return &Handler{client: client, deployments: deployments}
}

type modelObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

type modelsResponse struct {
	Object string        `json:"object"`
	Data   []modelObject `json:"data"`
}

// ListModels returns all RUNNING deployments formatted as an OpenAI models list.
func (h *Handler) ListModels(c *gin.Context) {
	deps, err := h.deployments.ListAll(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}

	var models []modelObject
	for _, d := range deps {
		models = append(models, modelObject{
			ID:      d.EffectiveModelName(),
			Object:  "model",
			OwnedBy: "sap-ai-core",
		})
	}

	c.JSON(http.StatusOK, modelsResponse{Object: "list", Data: models})
}

func errorBody(msg string) gin.H {
	return gin.H{"error": gin.H{"message": msg, "type": "proxy_error"}}
}

// rgHeaders returns an AI-Resource-Group override header when rg is non-empty.
func rgHeaders(rg string) map[string]string {
	if rg == "" {
		return nil
	}
	return map[string]string{"AI-Resource-Group": rg}
}

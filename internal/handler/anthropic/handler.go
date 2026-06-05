package anthropic

import (
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

func (h *Handler) ListModels(c *gin.Context) {
	deps, err := h.deployments.ListAll(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}

	type modelObj struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	type resp struct {
		Data []modelObj `json:"data"`
	}

	var models []modelObj
	for _, d := range deps {
		models = append(models, modelObj{ID: d.EffectiveModelName(), Type: "model"})
	}
	c.JSON(http.StatusOK, resp{Data: models})
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

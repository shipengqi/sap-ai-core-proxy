package gemini

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
)

// Handler groups shared SAP client dependencies for Gemini-surface endpoints.
type Handler struct {
	client      *sapclient.Client
	deployments *sapclient.DeploymentManager
}

func NewHandler(client *sapclient.Client, deployments *sapclient.DeploymentManager) *Handler {
	return &Handler{client: client, deployments: deployments}
}

// ListModels returns all RUNNING deployments as a Gemini-style models list.
func (h *Handler) ListModels(c *gin.Context) {
	deps, err := h.deployments.ListAll(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}

	type modelObj struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	}
	type resp struct {
		Models []modelObj `json:"models"`
	}

	var models []modelObj
	for _, d := range deps {
		models = append(models, modelObj{
			Name:        "models/" + d.EffectiveModelName(),
			DisplayName: d.EffectiveModelName(),
		})
	}
	c.JSON(http.StatusOK, resp{Models: models})
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

package litellm

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

func (h *Handler) Embeddings(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorBody("read request body: "+err.Error()))
		return
	}

	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, errorBody("invalid JSON: "+err.Error()))
		return
	}

	modelName := req.Model

	// Set model in context for logging middleware
	c.Set("model", modelName)

	dep, err := h.deployments.GetDeployment(c.Request.Context(), modelName)
	if err != nil {
		c.JSON(http.StatusNotFound, errorBody(err.Error()))
		return
	}

	slog.Info("calling litellm embeddings", "model", modelName, "deployment_id", dep.ID)
	upstream, err := h.client.Do(c.Request.Context(), http.MethodPost,
		dep.DeployedURL+"/embeddings", bytes.NewReader(body), nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}
	defer upstream.Body.Close()
	respBody, _ := io.ReadAll(upstream.Body)
	c.Data(upstream.StatusCode, "application/json", respBody)
}

package litellm

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

func (h *Handler) ChatCompletions(c *gin.Context) {
	h.handleCompletion(c)
}

func (h *Handler) Completions(c *gin.Context) {
	h.handleCompletion(c)
}

func (h *Handler) handleCompletion(c *gin.Context) {
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

	var model string
	if m, ok := raw["model"]; ok {
		_ = json.Unmarshal(m, &model)
	}

	// Set model in context for logging middleware
	c.Set("model", model)

	dep, err := h.deployments.GetOrchestrationDeployment(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusNotFound, errorBody(err.Error()))
		return
	}

	slog.Info("calling litellm orchestration", "model", model, "deployment_id", dep.ID)
	orchBody, err := buildOrchestrationBody(model, raw["messages"], raw["tools"], raw["response_format"])
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorBody(err.Error()))
		return
	}

	h.doOrchestration(c, dep, orchBody)
}

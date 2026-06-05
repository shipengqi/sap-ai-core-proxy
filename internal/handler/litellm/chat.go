package litellm

import (
	"encoding/json"
	"io"
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

	dep, err := h.deployments.GetOrchestrationDeployment(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusNotFound, errorBody(err.Error()))
		return
	}

	orchBody, err := buildOrchestrationBody(model, raw["messages"], raw["tools"], raw["response_format"])
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorBody(err.Error()))
		return
	}

	h.doOrchestration(c, dep, orchBody)
}

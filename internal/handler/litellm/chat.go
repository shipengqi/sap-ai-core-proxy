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

	if model == "" {
		c.JSON(400, errorBody("missing required field: model"))
		return
	}
	var streaming bool
	if v, ok := raw["stream"]; ok {
		if json.Unmarshal(v, &streaming) != nil {
			c.JSON(400, errorBody("stream must be a boolean"))
			return
		}
	}
	if streaming {
		c.JSON(400, errorBody("streaming SAP Orchestration is not supported by this adapter"))
		return
	}
	if _, ok := raw["prompt"]; ok {
		c.JSON(400, errorBody("legacy completions prompt is not supported; use chat messages"))
		return
	}
	var messages []json.RawMessage
	if json.Unmarshal(raw["messages"], &messages) != nil || len(messages) == 0 {
		c.JSON(400, errorBody("messages must be a non-empty array"))
		return
	}
	for key := range raw {
		switch key {
		case "model", "messages", "tools", "response_format", "stream":
		default:
			c.JSON(400, errorBody("parameter "+key+" is not supported by the Orchestration adapter"))
			return
		}
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

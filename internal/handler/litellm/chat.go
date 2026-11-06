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
		if string(v) == "null" || json.Unmarshal(v, &streaming) != nil {
			c.JSON(400, errorBody("stream must be a boolean"))
			return
		}
	}
	includeUsage := false
	if v, ok := raw["stream_options"]; ok {
		var options map[string]json.RawMessage
		if json.Unmarshal(v, &options) != nil || options == nil {
			c.JSON(400, errorBody("stream_options must be an object"))
			return
		}
		for key, value := range options {
			if key != "include_usage" {
				c.JSON(400, errorBody("stream_options."+key+" is not supported by the Orchestration adapter"))
				return
			}
			if string(value) == "null" || json.Unmarshal(value, &includeUsage) != nil {
				c.JSON(400, errorBody("stream_options.include_usage must be a boolean"))
				return
			}
		}
		if !streaming {
			c.JSON(400, errorBody("stream_options requires stream: true"))
			return
		}
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
		case "model", "messages", "tools", "response_format", "stream", "stream_options", "max_tokens", "max_completion_tokens":
		default:
			if !orchestrationModelParameter(key) {
				c.JSON(400, errorBody("parameter "+key+" is not supported by the Orchestration adapter"))
				return
			}
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
	orchBody, err := buildOrchestrationBody(model, raw, streaming)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorBody(err.Error()))
		return
	}

	if streaming {
		h.streamOrchestration(c, dep, orchBody, includeUsage)
		return
	}
	h.doOrchestration(c, dep, orchBody)
}

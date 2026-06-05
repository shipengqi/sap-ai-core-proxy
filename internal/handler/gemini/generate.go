package gemini

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/stream"
)

func (h *Handler) Generate(c *gin.Context) {
	raw := strings.TrimPrefix(c.Param("model"), "/")

	lastColon := strings.LastIndex(raw, ":")
	if lastColon < 0 {
		c.JSON(http.StatusBadRequest, errorBody("path must be models/{model}:{operation}"))
		return
	}
	modelName := raw[:lastColon]
	operation := raw[lastColon+1:]

	
	dep, err := h.deployments.GetDeployment(c.Request.Context(), modelName)
	if err != nil {
		c.JSON(http.StatusNotFound, errorBody(err.Error()))
		return
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorBody("read request body: "+err.Error()))
		return
	}

	upstreamURL := dep.DeployedURL + "/models/" + modelName + ":" + operation

	if operation == "streamGenerateContent" {
		upstream, err := h.client.DoStreaming(c.Request.Context(), http.MethodPost, upstreamURL, bytes.NewReader(body), nil)
		if err != nil {
			c.JSON(http.StatusBadGateway, errorBody(err.Error()))
			return
		}
		c.Status(http.StatusOK)
		stream.PipeGemini(c, upstream)
		return
	}

	upstream, err := h.client.Do(c.Request.Context(), http.MethodPost, upstreamURL, bytes.NewReader(body), nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}
	defer upstream.Body.Close()
	respBody, _ := io.ReadAll(upstream.Body)
	c.Data(upstream.StatusCode, "application/json", respBody)
}

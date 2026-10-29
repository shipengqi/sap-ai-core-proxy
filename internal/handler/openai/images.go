package openai

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/catalogue"
	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

// ImageGenerations proxies POST /openai/v1/images/generations to SAP AI Core.
// Gemini models use /models/{model}:generateContent with format translation.
// Titan models use /invoke with Titan-format translation.
// All other models use /images/generations?api-version=... with passthrough.
func (h *Handler) ImageGenerations(c *gin.Context) {
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
	c.Set("model", model)

	// Image generation is billed per image, not per token, and none of the three
	// response shapes here carries a token usage block. Declaring that up front
	// keeps these requests out of usage_missing and uninstrumented — and out of
	// the "somebody forgot an Observe call" signal, which must stay meaningful.
	usage.MarkNoUsageExpected(c)

	dep, err := h.deployments.GetDeployment(c.Request.Context(), model)
	if err != nil {
		c.JSON(http.StatusNotFound, errorBody(err.Error()))
		return
	}

	var prompt string
	if p, ok := raw["prompt"]; ok {
		_ = json.Unmarshal(p, &prompt)
	}
	var n int
	if nv, ok := raw["n"]; ok {
		_ = json.Unmarshal(nv, &n)
	}

	if catalogue.IsGemini(model) {
		upstreamBody := openAIToGeminiImageBody(prompt, n)
		upstreamURL := dep.DeployedURL + "/models/" + model + ":generateContent"

		resp, err := h.client.Do(c.Request.Context(), http.MethodPost, upstreamURL, bytes.NewReader(upstreamBody), nil)
		if err != nil {
			c.JSON(http.StatusBadGateway, errorBody(err.Error()))
			return
		}
		defer func() { _ = resp.Body.Close() }()

		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			c.JSON(502, errorBody(readErr.Error()))
			return
		}
		if resp.StatusCode != http.StatusOK {
			c.Data(resp.StatusCode, "application/json", respBody)
			return
		}
		c.Data(http.StatusOK, "application/json", geminiImageToOpenAIResponse(respBody))
		return
	}

	if catalogue.IsTitan(model) {
		var size string
		if sv, ok := raw["size"]; ok {
			_ = json.Unmarshal(sv, &size)
		}

		upstreamBody := openAIToTitanImageBody(prompt, n, size)
		resp, err := h.client.Do(c.Request.Context(), http.MethodPost,
			dep.DeployedURL+"/invoke", bytes.NewReader(upstreamBody), nil)
		if err != nil {
			c.JSON(http.StatusBadGateway, errorBody(err.Error()))
			return
		}
		defer func() { _ = resp.Body.Close() }()

		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			c.JSON(502, errorBody(readErr.Error()))
			return
		}
		if resp.StatusCode != http.StatusOK {
			c.Data(resp.StatusCode, "application/json", respBody)
			return
		}
		c.Data(http.StatusOK, "application/json", titanToOpenAIImageResponse(respBody))
		return
	}

	// DALL-E and other OpenAI-compatible models: forward as-is.
	resp, err := h.client.Do(c.Request.Context(), http.MethodPost,
		dep.DeployedURL+"/images/generations?api-version=2024-12-01-preview", bytes.NewReader(body), nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		c.JSON(502, errorBody(readErr.Error()))
		return
	}
	c.Data(resp.StatusCode, "application/json", respBody)
}

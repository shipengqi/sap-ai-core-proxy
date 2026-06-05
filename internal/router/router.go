package router

import (
	"bytes"
	"io"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/handler/anthropic"
	"github.com/shipengqi/sap-ai-core-proxy/internal/handler/gemini"
	"github.com/shipengqi/sap-ai-core-proxy/internal/handler/litellm"
	openaihandler "github.com/shipengqi/sap-ai-core-proxy/internal/handler/openai"
	"github.com/shipengqi/sap-ai-core-proxy/internal/middleware"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
)

// New creates and returns a configured *gin.Engine with all routes registered.
// When debug is true, diagnostic endpoints are registered.
func New(client *sapclient.Client, deployments *sapclient.DeploymentManager, debug bool) *gin.Engine {
	r := gin.New()
	r.Use(middleware.Logger())
	r.Use(middleware.CORS())

	oa := openaihandler.NewHandler(client, deployments)
	an := anthropic.NewHandler(client, deployments)
	ge := gemini.NewHandler(client, deployments)
	ll := litellm.NewHandler(client, deployments)

	// OpenAI surface
	ov1 := r.Group("/openai/v1")
	{
		ov1.GET("/models", oa.ListModels)
		ov1.POST("/chat/completions", oa.ChatCompletions)
		ov1.POST("/embeddings", oa.Embeddings)
		ov1.POST("/responses", oa.CreateResponse)
		ov1.GET("/responses/:id", oa.GetResponse)
		ov1.DELETE("/responses/:id", oa.DeleteResponse)
	}

	// Anthropic surface
	av1 := r.Group("/anthropic/v1")
	{
		av1.GET("/models", an.ListModels)
		av1.HEAD("/models", func(c *gin.Context) { c.Status(200) })
		av1.POST("/messages", an.Messages)
	}
	// Claude Code connectivity probe: HEAD {ANTHROPIC_BASE_URL}
	r.HEAD("/anthropic", func(c *gin.Context) { c.Status(200) })

	// Gemini surface
	r.GET("/gemini/v1/models", ge.ListModels)
	// Wildcard to capture "gemini-pro:generateContent" including the colon.
	r.POST("/gemini/v1beta/models/*model", ge.Generate)

	// LiteLLM / Orchestration surface
	lv1 := r.Group("/litellm/v1")
	{
		lv1.GET("/models", ll.ListModels)
		lv1.GET("/model/info", ll.ModelInfo)
		lv1.POST("/chat/completions", ll.ChatCompletions)
		lv1.POST("/completions", ll.Completions)
		lv1.POST("/embeddings", ll.Embeddings)
	}

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok", "service": "sap-ai-core-proxy"})
	})

	if debug {
		// Debug: list all deployments matching a model name
		r.GET("/debug/deployment", func(c *gin.Context) {
			model := c.Query("model")
			all, err := deployments.ListAll(c.Request.Context())
			if err != nil {
				c.JSON(500, gin.H{"error": err.Error()})
				return
			}
			type entry struct {
				ID              string `json:"id"`
				ModelName       string `json:"model_name"`
				ScenarioID      string `json:"scenario_id"`
				ResourceGroupID string `json:"resource_group_id"`
				DeployedURL     string `json:"deployed_url"`
			}
			var matches []entry
			for _, d := range all {
				if model == "" || strings.Contains(strings.ToLower(d.EffectiveModelName()), strings.ToLower(model)) {
					matches = append(matches, entry{
						ID:              d.ID,
						ModelName:       d.EffectiveModelName(),
						ScenarioID:      d.ScenarioID,
						ResourceGroupID: d.ResourceGroupID,
						DeployedURL:     d.DeployedURL,
					})
				}
			}
			c.JSON(200, gin.H{"count": len(matches), "matches": matches})
		})

		// Debug: probe a deployment directly
		r.GET("/debug/probe", func(c *gin.Context) {
			depURL := c.Query("url")
			if depURL == "" {
				c.JSON(400, gin.H{"error": "url param required"})
				return
			}
			body := []byte(`{"messages":[{"role":"user","content":"hi"}],"max_completion_tokens":5}`)
			resp, err := client.Do(c.Request.Context(), "POST", depURL, bytes.NewReader(body), nil)
			if err != nil {
				c.JSON(502, gin.H{"error": err.Error()})
				return
			}
			defer resp.Body.Close()
			rb, _ := io.ReadAll(resp.Body)
			c.Data(resp.StatusCode, "application/json", rb)
		})
	}

	return r
}

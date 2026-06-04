package router

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/catalogue"
	providerPkg "github.com/shipengqi/sap-ai-core-proxy/internal/provider"
	"github.com/shipengqi/sap-ai-core-proxy/internal/provider/anthropic"
	"github.com/shipengqi/sap-ai-core-proxy/internal/provider/openai"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
)

// Deps holds all dependencies needed by the routers.
type Deps struct {
	SapClient         *sapclient.SapClient
	DeploymentManager *sapclient.DeploymentManager
}

// RegisterAll mounts all route groups on the Gin engine.
func RegisterAll(r *gin.Engine, deps *Deps) {
	registerHealth(r)
	registerOpenAI(r, deps)
	registerAnthropic(r, deps)
	registerCompat(r)
	registerAdmin(r, deps)
}

// ---- Health ----

func registerHealth(r *gin.Engine) {
	r.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"name":    "sap-ai-core-proxy",
			"version": "2.0.0",
			"status":  "running",
			"endpoints": gin.H{
				"openai":    "/openai/v1/...",
				"anthropic": "/anthropic/v1/...",
				"health":    "/health",
				"admin":     "/admin/...",
			},
		})
	})
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":    "ok",
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})
	})
}

// ---- OpenAI ----

func registerOpenAI(r *gin.Engine, deps *Deps) {
	converseProvider := openai.NewConverseOpenAIProvider(deps.SapClient, deps.DeploymentManager)
	invokeProvider := openai.NewInvokeOpenAIProvider(deps.SapClient, deps.DeploymentManager)
	geminiProvider := openai.NewGeminiProvider(deps.SapClient, deps.DeploymentManager)
	nativeProvider := openai.NewOpenAIChatProvider(deps.SapClient, deps.DeploymentManager)
	embeddingsProvider := openai.NewEmbeddingsProvider(deps.SapClient, deps.DeploymentManager)
	responsesProvider := openai.NewResponsesProvider(deps.SapClient, deps.DeploymentManager)
	audioProvider := openai.NewAudioProvider(deps.SapClient, deps.DeploymentManager)

	claudeDispatcher := providerPkg.NewClaudeDispatcher(
		converseProvider.Handle,
		invokeProvider.Handle,
	)

	g := r.Group("/openai")

	// Models
	g.GET("/v1/models", func(c *gin.Context) { handleListModels(c, deps.DeploymentManager) })
	g.GET("/models", func(c *gin.Context) { handleListModels(c, deps.DeploymentManager) })
	g.GET("/v1/models/:modelId", func(c *gin.Context) { handleGetModel(c, deps.DeploymentManager) })

	// Chat completions — body is read once and restored for downstream handlers
	g.POST("/v1/chat/completions", handleChatDispatch(claudeDispatcher, geminiProvider, nativeProvider))
	g.POST("/chat/completions", handleChatDispatch(claudeDispatcher, geminiProvider, nativeProvider))

	// Embeddings
	g.POST("/v1/embeddings", embeddingsProvider.HandleEmbeddings)

	// Responses API
	g.POST("/v1/responses", responsesProvider.HandleCreate)
	g.GET("/v1/responses/:responseId", responsesProvider.HandleGet)
	g.DELETE("/v1/responses/:responseId", responsesProvider.HandleDelete)

	// Audio transcription
	g.POST("/v1/audio/transcriptions", audioProvider.HandleTranscription)
}

func handleChatDispatch(
	claudeDispatcher *providerPkg.ClaudeDispatcher,
	geminiProvider *openai.GeminiProvider,
	nativeProvider *openai.OpenAIChatProvider,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Read body once and restore it so downstream handlers can re-read
		rawBody, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "cannot read request body", "type": "invalid_request_error"}})
			return
		}
		// Restore body for downstream
		c.Request.Body = io.NopCloser(bytes.NewReader(rawBody))

		// Peek model and messages without consuming the restored body
		var peek struct {
			Model    string `json:"model"`
			Messages []any  `json:"messages"`
		}
		if err := json.Unmarshal(rawBody, &peek); err != nil || peek.Model == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{"message": "Missing required parameter: model", "type": "invalid_request_error", "param": "model", "code": "missing_parameter"},
			})
			return
		}
		if len(peek.Messages) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{"message": "Missing required parameter: messages", "type": "invalid_request_error", "param": "messages", "code": "missing_parameter"},
			})
			return
		}

		// Resolve alias or SAP name → deployment chain (newest-first for -latest aliases)
		chain, err := catalogue.ResolveChain(peek.Model)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{"message": err.Error(), "type": "invalid_request_error", "param": "model", "code": "invalid_value"},
			})
			return
		}
		sapName := chain[0]
		c.Set("sapModelChain", chain)

		provider := catalogue.GetProvider(sapName)
		slog.Info("chat completion", "model", peek.Model, "sapModel", sapName, "provider", provider)

		switch provider {
		case "anthropic":
			claudeDispatcher.Dispatch(sapName, c)
		case "gemini":
			geminiProvider.HandleChatCompletion(c)
		default:
			nativeProvider.HandleChatCompletion(c)
		}
	}
}

func handleListModels(c *gin.Context, dm *sapclient.DeploymentManager) {
	ctx := context.Background()
	deps, err := dm.GetDeployments(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{"message": err.Error(), "type": "api_error", "param": nil, "code": "500"},
		})
		return
	}

	models := make([]gin.H, 0, len(deps))
	for _, dep := range deps {
		name := dep.ModelName()
		models = append(models, gin.H{
			"id":       name,
			"object":   "model",
			"created":  parseCreatedAt(dep.CreatedAt),
			"owned_by": catalogue.GetOwner(name),
		})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": models})
}

func handleGetModel(c *gin.Context, dm *sapclient.DeploymentManager) {
	modelID := c.Param("modelId")
	ctx := context.Background()
	deps, err := dm.GetDeployments(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{"message": err.Error(), "type": "api_error", "param": nil, "code": "500"},
		})
		return
	}
	for _, dep := range deps {
		if dep.ModelName() == modelID {
			c.JSON(http.StatusOK, gin.H{
				"id":       dep.ModelName(),
				"object":   "model",
				"created":  parseCreatedAt(dep.CreatedAt),
				"owned_by": catalogue.GetOwner(dep.ModelName()),
			})
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{
		"error": gin.H{"message": "Model " + modelID + " not found", "type": "invalid_request_error", "param": "model", "code": "model_not_found"},
	})
}

func parseCreatedAt(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Now().Unix()
	}
	return t.Unix()
}

// ---- Anthropic ----

func registerAnthropic(r *gin.Engine, deps *Deps) {
	claude := anthropic.NewClaudeAnthropicProvider(deps.SapClient, deps.DeploymentManager)

	g := r.Group("/anthropic")

	g.HEAD("", func(c *gin.Context) { c.Status(http.StatusOK) })
	g.GET("", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"provider": "anthropic", "status": "ok"})
	})

	g.GET("/v1/models", func(c *gin.Context) {
		ctx := context.Background()
		deps2, err := deps.DeploymentManager.GetDeployments(ctx)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		var models []gin.H
		for _, dep := range deps2 {
			if catalogue.GetProvider(dep.ModelName()) == "anthropic" {
				models = append(models, gin.H{
					"id":           dep.ModelName(),
					"display_name": dep.ModelName(),
					"created_at":   dep.CreatedAt,
					"type":         "model",
				})
			}
		}
		if models == nil {
			models = []gin.H{}
		}
		c.JSON(http.StatusOK, gin.H{"data": models, "has_more": false})
	})

	g.POST("/v1/messages", claude.HandleMessages)
	g.POST("/v1/messages/count_tokens", claude.HandleCountTokens)
}

// ---- Claude Code compat ----

func registerCompat(r *gin.Engine) {
	fakeUser := gin.H{
		"id":                   "user_proxy_sap_ai_core",
		"email":                "proxy@sap-ai-core.local",
		"name":                 "SAP AI Core Proxy User",
		"display_name":         "SAP AI Core Proxy",
		"has_claude_pro":       true,
		"has_pro_subscription": true,
		"has_api_access":       true,
	}
	fakeOrg := gin.H{
		"id":              "org_proxy_sap_ai_core",
		"name":            "SAP AI Core",
		"billing_type":    "api_error_counts",
		"rate_limit_tier": "production",
	}

	g := r.Group("/anthropic")

	userHandler := func(c *gin.Context) { c.JSON(http.StatusOK, fakeUser) }
	g.GET("/api/auth/me", userHandler)
	g.GET("/api/user", userHandler)
	g.GET("/api/account", userHandler)
	g.GET("/api/auth/current_user", userHandler)

	g.POST("/oauth/token", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"access_token":  "sk-ant-proxy-auth-bypass-token-for-sap-ai-core",
			"refresh_token": "sk-ant-proxy-refresh-bypass-token",
			"token_type":    "Bearer",
			"expires_in":    315360000,
			"scope":         "user:inference",
		})
	})

	g.GET("/api/organizations", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"organizations": []gin.H{fakeOrg}})
	})
	g.GET("/api/quota", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"usage":    gin.H{"tokens_used": 0, "requests_used": 0},
			"limits":   gin.H{"tokens": 1000000000, "requests": 1000000},
			"reset_at": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		})
	})
	g.GET("/api/user_flags", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"flags": gin.H{
				"has_pro_subscription":         true,
				"claude_ai_mcp_enabled":        true,
				"interleaved_thinking_enabled": true,
			},
		})
	})
	g.GET("/api/billing/subscription", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"plan":               "max_tier",
			"status":             "active",
			"current_period_end": time.Now().Add(365 * 24 * time.Hour).UTC().Format(time.RFC3339),
		})
	})
	g.GET("/api/auth/claude_ai_oauth", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"eligible": true, "user": fakeUser})
	})
	// Catch-all for unknown /anthropic/api/* paths — registered on the engine
	// via NoRoute to avoid Gin radix-tree conflicts with already-registered prefixes.
	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		if len(path) >= len("/anthropic/api/") && path[:len("/anthropic/api/")] == "/anthropic/api/" {
			slog.Debug("Claude Code compat stub", "method", c.Request.Method, "path", path)
			c.JSON(http.StatusOK, gin.H{"ok": true})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	})
}

// ---- Admin ----

func registerAdmin(r *gin.Engine, deps *Deps) {
	g := r.Group("/admin")
	g.POST("/refresh-deployments", func(c *gin.Context) {
		ctx := context.Background()
		summaries, err := deps.DeploymentManager.Refresh(ctx)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"deployments": summaries, "count": len(summaries)})
	})
}

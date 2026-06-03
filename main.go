package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	"github.com/shipengqi/sap-ai-core-proxy/internal/config"
	"github.com/shipengqi/sap-ai-core-proxy/internal/middleware"
	"github.com/shipengqi/sap-ai-core-proxy/internal/router"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
)

func main() {
	// Load .env file if present (ignore error — env vars from shell are fine too)
	_ = godotenv.Load()

	// Configure structured JSON logging
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Load and validate configuration
	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}

	// Create SAP AI Core clients
	auth := sapclient.NewAuthManager(
		cfg.ClientID,
		cfg.ClientSecret,
		cfg.TokenURL,
		cfg.BaseURL,
		cfg.ResourceGroup,
	)
	client := sapclient.NewSapClient(auth)
	dm := sapclient.NewDeploymentManager(auth)

	// Eagerly pre-fetch deployments (non-blocking — warns on failure)
	go dm.WarmUp(context.Background())

	// Build Gin engine
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(middleware.CORS())
	r.Use(middleware.Logger())
	r.Use(gin.Recovery())

	// Register all routes
	router.RegisterAll(r, &router.Deps{
		SapClient:         client,
		DeploymentManager: dm,
	})

	addr := ":" + cfg.Port
	slog.Info("sap-ai-core-proxy starting",
		"port", cfg.Port,
		"baseURL", cfg.BaseURL,
		"resourceGroup", cfg.ResourceGroup,
	)

	if err := r.Run(addr); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

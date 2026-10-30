package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shipengqi/sap-ai-core-proxy/internal/compress"
	"github.com/shipengqi/sap-ai-core-proxy/internal/config"
	"github.com/shipengqi/sap-ai-core-proxy/internal/router"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
	"github.com/shipengqi/sap-ai-core-proxy/internal/tui"
	"github.com/shipengqi/sap-ai-core-proxy/internal/version"
)

func runServer(noTUI bool) error {
	interactiveTUI := !noTUI && tui.Interactive()
	var logBuffer *tui.LogBuffer
	if interactiveTUI {
		logBuffer = tui.NewLogBuffer(500)
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}
	setupLogger(cfg.Server.LogLevel, logBuffer)
	if cfg.Server.LogLevel != "debug" {
		gin.SetMode(gin.ReleaseMode)
	}
	// Preserve the existing global compression switch and lazy SAP setup.
	compress.Configure(cfg.Server.CompressLogs, config.CompressSkipStrategies())
	auth := sapclient.NewAuthManager(cfg.SAPAICore.TokenURL, cfg.SAPAICore.ClientID, cfg.SAPAICore.ClientSecret)
	client := sapclient.NewClient(cfg.SAPAICore.BaseURL, cfg.SAPAICore.ResourceGroup, auth)
	deployments := sapclient.NewDeploymentManager(client)
	r := router.New(client, deployments, cfg.Server.LogLevel == "debug", cfg.Server.Metrics, cfg.Server.AnthropicAutoCache)
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	slog.Info("server starting", "addr", addr, "version", version.Get().Version)
	if interactiveTUI {
		go tui.Run(context.Background(), "http://localhost"+addr, 2*time.Second, logBuffer)
	}
	if err := r.Run(addr); err != nil {
		slog.Error("server error", "err", err)
		return reportedError{err}
	}
	return nil
}

func setupLogger(level string, output *tui.LogBuffer) {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	if output != nil {
		slog.SetDefault(slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: l})))
		return
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})))
}

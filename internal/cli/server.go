package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
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
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()
	server := &http.Server{Addr: addr, Handler: r, ReadHeaderTimeout: 10 * time.Second}
	var panel func() error
	if interactiveTUI {
		panel = func() error {
			return tui.Run(ctx, cancel, "http://localhost"+addr, 2*time.Second, logBuffer)
		}
	}
	if err := runHTTPServer(ctx, cancel, server, 5*time.Second, panel); err != nil {
		// The panel has stopped; errors must no longer disappear into its buffer.
		if interactiveTUI {
			setupLogger(cfg.Server.LogLevel, nil)
		}
		slog.Error("server error", "err", err)
		return reportedError{err}
	}
	return nil
}

// runHTTPServer owns shutdown and waits for terminal restoration before returning.
func runHTTPServer(ctx context.Context, cancel context.CancelFunc, server *http.Server, timeout time.Duration, panel func() error) error {
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ListenAndServe() }()
	var panelDone chan error
	if panel != nil {
		panelDone = make(chan error, 1)
		go func() { panelDone <- panel() }()
	}
	var serverErr, panelErr error
	serverFinished, panelFinished := false, panel == nil
	select {
	case <-ctx.Done():
	case serverErr = <-serverDone:
		serverFinished = true
	case panelErr = <-panelDone:
		panelFinished = true
	}
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), timeout)
	defer stop()
	shutdownErr := server.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, server.Close())
	}
	if !serverFinished {
		serverErr = <-serverDone
	}
	if !panelFinished {
		panelErr = <-panelDone
	}
	if errors.Is(serverErr, http.ErrServerClosed) {
		serverErr = nil
	}
	return errors.Join(serverErr, panelErr, shutdownErr)
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

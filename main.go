package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/shipengqi/sap-ai-core-proxy/internal/tui"

	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/compress"
	"github.com/shipengqi/sap-ai-core-proxy/internal/config"
	"github.com/shipengqi/sap-ai-core-proxy/internal/router"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
	"github.com/shipengqi/sap-ai-core-proxy/internal/version"
)

func main() {
	noTUI := flag.Bool("no-tui", false, "run only the Gin server")
	showVersion := flag.Bool("version", false, "print version information and exit")
	flag.Parse()

	// -version is answered before the terminal probe and, more importantly,
	// before config.Load. This flag is most useful on a host that has no
	// config.json yet — a fresh install or a container health check — and
	// loading config there would exit non-zero instead of printing the version.
	if *showVersion {
		fmt.Println(version.String())
		return
	}

	interactiveTUI := !*noTUI && tui.Interactive()
	var logBuffer *tui.LogBuffer
	if interactiveTUI {
		logBuffer = tui.NewLogBuffer(500)
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	setupLogger(cfg.Server.LogLevel, logBuffer)

	if cfg.Server.LogLevel != "debug" {
		gin.SetMode(gin.ReleaseMode)
	}

	// The kill switch is global: when it is off, the compression path is never
	// entered, not even to count.
	compress.Configure(cfg.Server.CompressLogs, config.CompressSkipStrategies())

	auth := sapclient.NewAuthManager(cfg.SAPAICore.TokenURL, cfg.SAPAICore.ClientID, cfg.SAPAICore.ClientSecret)
	client := sapclient.NewClient(cfg.SAPAICore.BaseURL, cfg.SAPAICore.ResourceGroup, auth)
	deployments := sapclient.NewDeploymentManager(client)

	r := router.New(client, deployments, cfg.Server.LogLevel == "debug", cfg.Server.Metrics, cfg.Server.AnthropicAutoCache)

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	// The version belongs in the first log line: matching a behaviour report to
	// a release usually starts by reading the logs of a machine nobody can log
	// into interactively.
	slog.Info("server starting", "addr", addr, "version", version.Get().Version)
	if interactiveTUI {
		go tui.Run(context.Background(), "http://localhost"+addr, 2*time.Second, logBuffer)
	}
	if err := r.Run(addr); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
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
	var writer = os.Stdout
	if output != nil {
		slog.SetDefault(slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: l})))
		return
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: l})))
}

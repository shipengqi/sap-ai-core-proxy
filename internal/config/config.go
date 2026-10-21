package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type SAPAICore struct {
	BaseURL       string `json:"base_url"`
	TokenURL      string `json:"token_url"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	ResourceGroup string `json:"resource_group"`
}

type Server struct {
	Port     int    `json:"port"`
	LogLevel string `json:"log_level"`
	// Metrics enables the /metrics and /stats observation endpoints. It defaults
	// to true: token accounting is the point of P0, and the endpoints expose no
	// request content — only counters.
	Metrics bool `json:"metrics"`
	// CompressLogs enables live-zone log compression: the tool output in the
	// last user message is shrunk before the request goes upstream.
	//
	// It defaults to false on purpose. The saving only materialises when the
	// text really is a log, and a wrong guess changes what the model sees, so
	// it stays opt-in until the measurements say otherwise.
	CompressLogs bool `json:"compress_logs"`
}

type Config struct {
	SAPAICore SAPAICore `json:"sap_ai_core"`
	Server    Server    `json:"server"`
}

func Load() (*Config, error) {
	cfg := &Config{
		SAPAICore: SAPAICore{ResourceGroup: "default"},
		// Metrics is set before decoding so a config file that predates the field
		// keeps the endpoints enabled instead of silently turning them off.
		Server: Server{Port: 3001, LogLevel: "info", Metrics: true},
	}

	path := filepath.Join(homeDir(), ".aicoreproxy", "config.json")
	if v := os.Getenv("AICOREPROXY_CONFIG_FILE"); v != "" {
		path = v
	}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}

	overrideFromEnv(cfg)

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func overrideFromEnv(cfg *Config) {
	if v := os.Getenv("SAP_AI_CORE_BASE_URL"); v != "" {
		cfg.SAPAICore.BaseURL = v
	}
	if v := os.Getenv("SAP_AI_CORE_TOKEN_URL"); v != "" {
		cfg.SAPAICore.TokenURL = v
	}
	if v := os.Getenv("SAP_AI_CORE_CLIENT_ID"); v != "" {
		cfg.SAPAICore.ClientID = v
	}
	if v := os.Getenv("SAP_AI_CORE_CLIENT_SECRET"); v != "" {
		cfg.SAPAICore.ClientSecret = v
	}
	if v := os.Getenv("SAP_AI_CORE_RESOURCE_GROUP"); v != "" {
		cfg.SAPAICore.ResourceGroup = v
	}
	if v := os.Getenv("PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Server.Port = p
		}
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.Server.LogLevel = v
	}
	if v := os.Getenv("METRICS_ENABLED"); v != "" {
		cfg.Server.Metrics = parseBool(v, cfg.Server.Metrics)
	}
	if v := os.Getenv("COMPRESS_LOGS"); v != "" {
		cfg.Server.CompressLogs = parseBool(v, cfg.Server.CompressLogs)
	}
}

// CompressSkipStrategies returns the strategies the escape valve turns off, from
// COMPRESS_LOGS_SKIP (comma separated). An empty env var means nothing is
// skipped, which is the default.
func CompressSkipStrategies() []string {
	v := os.Getenv("COMPRESS_LOGS_SKIP")
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}

// parseBool accepts the usual truthy/falsy spellings. Unrecognised values keep
// the current setting rather than failing startup: a typo in an env var must not
// take the proxy down.
func parseBool(v string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "t", "true", "y", "yes", "on", "enabled":
		return true
	case "0", "f", "false", "n", "no", "off", "disabled":
		return false
	}
	return fallback
}

func (c *Config) validate() error {
	if c.SAPAICore.BaseURL == "" {
		return fmt.Errorf("sap_ai_core.base_url is required")
	}
	if c.SAPAICore.TokenURL == "" {
		return fmt.Errorf("sap_ai_core.token_url is required")
	}
	if c.SAPAICore.ClientID == "" {
		return fmt.Errorf("sap_ai_core.client_id is required")
	}
	if c.SAPAICore.ClientSecret == "" {
		return fmt.Errorf("sap_ai_core.client_secret is required")
	}
	return nil
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}

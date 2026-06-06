package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
}

type Config struct {
	SAPAICore SAPAICore `json:"sap_ai_core"`
	Server    Server    `json:"server"`
}

func Load() (*Config, error) {
	cfg := &Config{
		SAPAICore: SAPAICore{ResourceGroup: "default"},
		Server:    Server{Port: 3001, LogLevel: "info"},
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

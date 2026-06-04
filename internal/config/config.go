package config

import (
	"fmt"
	"os"
	"strings"
)

// Config holds all runtime configuration loaded from environment variables.
type Config struct {
	ClientID      string
	ClientSecret  string
	TokenURL      string
	BaseURL       string
	ResourceGroup string
	Port          string
}

// Load reads environment variables, validates required ones, and returns Config.
// Returns an error listing all missing required variables.
func Load() (*Config, error) {
	cfg := &Config{
		ClientID:      os.Getenv("SAP_AI_CORE_CLIENT_ID"),
		ClientSecret:  os.Getenv("SAP_AI_CORE_CLIENT_SECRET"),
		TokenURL:      os.Getenv("SAP_AI_CORE_TOKEN_URL"),
		BaseURL:       os.Getenv("SAP_AI_CORE_BASE_URL"),
		ResourceGroup: os.Getenv("SAP_AI_CORE_RESOURCE_GROUP"),
		Port:          os.Getenv("PORT"),
	}

	if cfg.ResourceGroup == "" {
		cfg.ResourceGroup = "default"
	}
	if cfg.Port == "" {
		cfg.Port = "3001"
	}

	var missing []string
	if cfg.ClientID == "" {
		missing = append(missing, "SAP_AI_CORE_CLIENT_ID")
	}
	if cfg.ClientSecret == "" {
		missing = append(missing, "SAP_AI_CORE_CLIENT_SECRET")
	}
	if cfg.TokenURL == "" {
		missing = append(missing, "SAP_AI_CORE_TOKEN_URL")
	}
	if cfg.BaseURL == "" {
		missing = append(missing, "SAP_AI_CORE_BASE_URL")
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required environment variables: %s\n\nPlease set:\n  SAP_AI_CORE_CLIENT_ID     - OAuth client ID\n  SAP_AI_CORE_CLIENT_SECRET - OAuth client secret\n  SAP_AI_CORE_TOKEN_URL     - OAuth token URL\n  SAP_AI_CORE_BASE_URL      - SAP AI Core API base URL\n  SAP_AI_CORE_RESOURCE_GROUP - Resource group (optional, default: \"default\")",
			strings.Join(missing, ", "))
	}

	return cfg, nil
}

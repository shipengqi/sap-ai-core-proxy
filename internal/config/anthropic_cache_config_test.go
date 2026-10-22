package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnthropicAutoCacheConfiguration(t *testing.T) {
	tests := []struct {
		name       string
		configBody string
		envValue   string
		want       bool
	}{
		{name: "default disabled", envValue: "", want: false},
		{name: "config enables", configBody: `{"server":{"anthropic_auto_cache":true}}`, want: true},
		{name: "environment enables", envValue: "true", want: true},
		{name: "environment overrides config", configBody: `{"server":{"anthropic_auto_cache":true}}`, envValue: "false", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			qaCredEnv(t)
			path := filepath.Join(t.TempDir(), "config.json")
			if tc.configBody != "" {
				if err := os.WriteFile(path, []byte(tc.configBody), 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			}
			t.Setenv("AICOREPROXY_CONFIG_FILE", path)
			t.Setenv("ANTHROPIC_AUTO_CACHE", tc.envValue)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Server.AnthropicAutoCache != tc.want {
				t.Errorf("AnthropicAutoCache = %v, want %v", cfg.Server.AnthropicAutoCache, tc.want)
			}
		})
	}
}

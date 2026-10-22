package config

import (
	"os"
	"path/filepath"
	"testing"
)

// qaCredEnv sets the four required SAP credentials so Load() reaches the point
// where the metrics toggle is decided.
func qaCredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SAP_AI_CORE_BASE_URL", "https://example.invalid")
	t.Setenv("SAP_AI_CORE_TOKEN_URL", "https://example.invalid/oauth/token")
	t.Setenv("SAP_AI_CORE_CLIENT_ID", "id")
	t.Setenv("SAP_AI_CORE_CLIENT_SECRET", "secret")
}

// TestQA_MetricsEnabledFromEnv pins the METRICS_ENABLED spellings. A wrong
// default here would silently disable token accounting in production.
func TestQA_MetricsEnabledFromEnv(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "unset keeps the default", value: "", want: true},
		{name: "false", value: "false", want: false},
		{name: "FALSE", value: "FALSE", want: false},
		{name: "zero", value: "0", want: false},
		{name: "off", value: "off", want: false},
		{name: "disabled", value: "disabled", want: false},
		{name: "no", value: "no", want: false},
		{name: "true", value: "true", want: true},
		{name: "one", value: "1", want: true},
		{name: "on", value: "on", want: true},
		{name: "typo keeps the default", value: "ture", want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			qaCredEnv(t)
			t.Setenv("AICOREPROXY_CONFIG_FILE", filepath.Join(t.TempDir(), "absent.json"))
			t.Setenv("METRICS_ENABLED", tc.value)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Server.Metrics != tc.want {
				t.Errorf("Metrics = %v, want %v for METRICS_ENABLED=%q", cfg.Server.Metrics, tc.want, tc.value)
			}
		})
	}
}

// TestQA_MetricsFromConfigFile covers the file path, including a config file that
// predates the field: it must leave the endpoints enabled.
func TestQA_MetricsFromConfigFile(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "field absent keeps the endpoints on",
			content: `{"server":{"port":3001,"log_level":"info"}}`,
			want:    true,
		},
		{
			name:    "field false disables them",
			content: `{"server":{"port":3001,"log_level":"info","metrics":false}}`,
			want:    false,
		},
		{
			name:    "field true enables them",
			content: `{"server":{"port":3001,"log_level":"info","metrics":true}}`,
			want:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			qaCredEnv(t)
			t.Setenv("METRICS_ENABLED", "")

			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			t.Setenv("AICOREPROXY_CONFIG_FILE", path)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Server.Metrics != tc.want {
				t.Errorf("Metrics = %v, want %v", cfg.Server.Metrics, tc.want)
			}
		})
	}
}

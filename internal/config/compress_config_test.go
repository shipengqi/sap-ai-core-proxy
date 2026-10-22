package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestQA_CompressLogsDefaultsToOff pins the single most important property of
// the switch: it is off unless somebody turned it on. A wrong default here would
// rewrite every coding session's tool output on day one.
func TestQA_CompressLogsDefaultsToOff(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "unset keeps the default", value: "", want: false},
		{name: "true", value: "true", want: true},
		{name: "TRUE", value: "TRUE", want: true},
		{name: "one", value: "1", want: true},
		{name: "on", value: "on", want: true},
		{name: "enabled", value: "enabled", want: true},
		{name: "false", value: "false", want: false},
		{name: "zero", value: "0", want: false},
		{name: "off", value: "off", want: false},
		{name: "typo keeps the default", value: "ture", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			qaCredEnv(t)
			t.Setenv("AICOREPROXY_CONFIG_FILE", filepath.Join(t.TempDir(), "absent.json"))
			t.Setenv("COMPRESS_LOGS", tc.value)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Server.CompressLogs != tc.want {
				t.Errorf("CompressLogs = %v, want %v for COMPRESS_LOGS=%q", cfg.Server.CompressLogs, tc.want, tc.value)
			}
		})
	}
}

// TestQA_CompressLogsFromConfigFile covers the file path, including a config
// file that predates the field.
func TestQA_CompressLogsFromConfigFile(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "field absent keeps it off",
			content: `{"server":{"port":3001,"log_level":"info"}}`,
			want:    false,
		},
		{
			name:    "field true enables it",
			content: `{"server":{"port":3001,"log_level":"info","compress_logs":true}}`,
			want:    true,
		},
		{
			name:    "field false disables it",
			content: `{"server":{"port":3001,"log_level":"info","compress_logs":false}}`,
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			qaCredEnv(t)
			t.Setenv("COMPRESS_LOGS", "")

			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			t.Setenv("AICOREPROXY_CONFIG_FILE", path)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Server.CompressLogs != tc.want {
				t.Errorf("CompressLogs = %v, want %v", cfg.Server.CompressLogs, tc.want)
			}
		})
	}
}

// TestQA_CompressSkipStrategies covers the escape valve.
func TestQA_CompressSkipStrategies(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  []string
	}{
		{name: "unset", value: "", want: nil},
		{name: "one strategy", value: "log", want: []string{"log"}},
		{name: "several", value: "log,json", want: []string{"log", "json"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("COMPRESS_LOGS_SKIP", tc.value)
			got := CompressSkipStrategies()
			if len(got) != len(tc.want) {
				t.Fatalf("CompressSkipStrategies() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("CompressSkipStrategies()[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPortOnlyPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("AICOREPROXY_CONFIG_FILE", path)
	t.Setenv("PORT", "")

	port, err := LoadPortOnly()
	if err != nil || port != 3001 {
		t.Fatalf("missing file: port=%d err=%v", port, err)
	}

	// A config file with no SAP credentials must still be readable: configure
	// runs before the proxy has ever been configured.
	if err := os.WriteFile(path, []byte(`{"server":{"port":6655,"log_level":"debug"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	port, err = LoadPortOnly()
	if err != nil || port != 6655 {
		t.Fatalf("file port: port=%d err=%v", port, err)
	}

	t.Setenv("PORT", "7777")
	port, err = LoadPortOnly()
	if err != nil || port != 7777 {
		t.Fatalf("env override: port=%d err=%v", port, err)
	}

	t.Setenv("PORT", "")
	if err := os.WriteFile(path, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPortOnly(); err == nil {
		t.Error("malformed config should fail")
	}
}

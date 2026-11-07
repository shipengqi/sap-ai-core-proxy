package agentconfig

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

type codex struct{}

func (codex) ID() string   { return "codex" }
func (codex) Name() string { return "Codex" }
func (codex) Verify() string {
	return "codex config get model_provider  and  codex --strict-config  (then /status)"
}

func (c codex) Targets(env Env) []Target {
	dir := env.codexDir()
	return []Target{
		{Path: filepath.Join(dir, "config.toml"), Mode: 0644},
		{Path: filepath.Join(dir, "auth.json"), Mode: 0600},
	}
}

func (c codex) Warnings(env Env) []string {
	var warnings []string
	project := filepath.Join(env.cwd(), ".codex", "config.toml")
	if data, err := os.ReadFile(project); err == nil && bytes.Contains(data, []byte("model_provider")) {
		warnings = append(warnings, fmt.Sprintf(
			"%s also sets model_provider; Codex ignores it in project config, but remove it if the value looks wrong", project))
	}
	user, err := readIfExists(filepath.Join(env.codexDir(), "config.toml"))
	if err == nil && bytes.Contains(user, []byte(`cli_auth_credentials_store = "keyring"`)) {
		warnings = append(warnings,
			"config.toml uses cli_auth_credentials_store = \"keyring\"; auth.json may be ignored — run codex login --with-api-key instead")
	}
	return warnings
}

func codexSectionHeader() string { return "[model_providers." + CodexProviderID + "]" }

func (c codex) Plan(opts Options, env Env) ([]FileChange, error) {
	dir := env.codexDir()
	configPath := filepath.Join(dir, "config.toml")
	authPath := filepath.Join(dir, "auth.json")

	configOld, err := readIfExists(configPath)
	if err != nil {
		return nil, err
	}
	authOld, err := readIfExists(authPath)
	if err != nil {
		return nil, err
	}

	model := defaultModel(opts, FamilyGPT)
	top := map[string]string{
		"model":          strconv.Quote(model),
		"model_provider": strconv.Quote(CodexProviderID),
	}
	// Codex appends /responses to base_url, and the proxy serves it under
	// /openai/v1 — keep that prefix. wire_api = "responses" is required since
	// Codex 0.95 removed "chat". supports_websockets is intentionally omitted:
	// it is a legacy field and can trip --strict-config on current versions.
	section := fmt.Sprintf("%s\nname = %q\nbase_url = %q\nwire_api = \"responses\"\nrequires_openai_auth = true\n",
		codexSectionHeader(), "AI Core Proxy", opts.BaseURL()+"/openai/v1")
	configNew, err := patchTOML(configOld, top, codexSectionHeader(), section)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}
	// requires_openai_auth reads auth.json; env_key must not be combined with it.
	authNew, err := mergeJSONObject(authOld, map[string]string{
		"OPENAI_API_KEY": PlaceholderKey,
		"auth_mode":      "apikey",
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", authPath, err)
	}
	return []FileChange{
		{Path: configPath, Old: configOld, New: configNew, Mode: 0644},
		{Path: authPath, Old: authOld, New: authNew, Mode: 0600},
	}, nil
}

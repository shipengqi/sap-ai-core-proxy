package agentconfig

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

type openCode struct{}

func (openCode) ID() string   { return "opencode" }
func (openCode) Name() string { return "OpenCode" }
func (openCode) Verify() string {
	return "opencode /reload then /models  (look for the aicore-* providers)"
}

func (o openCode) Targets(env Env) []Target {
	return []Target{{Path: env.openCodeFile(), Mode: 0644}}
}

// Warnings covers the config sources OpenCode ranks above the global file.
func (o openCode) Warnings(env Env) []string {
	var warnings []string
	if env.get("OPENCODE_CONFIG_CONTENT") != "" {
		warnings = append(warnings,
			"OPENCODE_CONFIG_CONTENT is set; inline config overrides the global file we write")
	}
	if dir := env.get("OPENCODE_CONFIG_DIR"); dir != "" {
		warnings = append(warnings, fmt.Sprintf(
			"OPENCODE_CONFIG_DIR=%s is set; that directory is loaded after the global config", dir))
	}
	if dir := env.managedOpenCodeDir(); dir != "" {
		for _, name := range []string{"opencode.json", "opencode.jsonc"} {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			if bytes.Contains(data, []byte("provider.use")) && bytes.Contains(data, []byte("deny")) {
				warnings = append(warnings, fmt.Sprintf(
					"managed config %s denies provider.use; managed policies outrank local allow rules", filepath.Join(dir, name)))
			}
		}
	}
	return warnings
}

func modelEntries(models []Model) map[string]any {
	entries := map[string]any{}
	for _, m := range models {
		context := m.Context
		if context == 0 {
			context = 200000
		}
		output := m.Output
		if output == 0 {
			output = 64000
		}
		entry := map[string]any{
			"limit": map[string]any{"context": context, "input": context, "output": output},
		}
		if m.Name != "" && m.Name != m.ID {
			entry["name"] = m.Name
		}
		if m.ModelID != "" {
			entry["modelID"] = m.ModelID
		}
		entries[m.ID] = entry
	}
	return entries
}

func (o openCode) Plan(opts Options, env Env) ([]FileChange, error) {
	path := env.openCodeFile()
	old, err := readIfExists(path)
	if err != nil {
		return nil, err
	}
	gpt := ModelsFor(opts, FamilyGPT)
	claude := ModelsFor(opts, FamilyClaude)

	root := map[string]string{
		"$schema": strconv.Quote("https://opencode.ai/config.json"),
	}
	if len(gpt) > 0 {
		root["model"] = strconv.Quote(OpenCodeOpenAIID + "/" + gpt[0].ID)
	}
	providers := map[string]string{
		OpenCodeOpenAIID: mustJSON(map[string]any{
			"canonical": "openai",
			"name":      "AI Core Proxy — OpenAI",
			"settings": map[string]any{
				"apiKey":    PlaceholderKey,
				"baseURL":   opts.BaseURL() + "/openai/v1",
				"transport": "http",
			},
			"models": modelEntries(gpt),
		}),
		OpenCodeAnthropicID: mustJSON(map[string]any{
			"canonical": "anthropic",
			"name":      "AI Core Proxy — Anthropic",
			"settings": map[string]any{
				"apiKey":  PlaceholderKey,
				"baseURL": opts.BaseURL() + "/anthropic/v1",
			},
			"models": modelEntries(claude),
		}),
	}
	updated, err := patchJSONC(old, root, providers, []string{OpenCodeOpenAIID, OpenCodeAnthropicID})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return []FileChange{{Path: path, Old: old, New: updated, Mode: 0644}}, nil
}

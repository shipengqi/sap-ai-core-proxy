package agentconfig

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type claudeCode struct{}

func (claudeCode) ID() string   { return "claude-code" }
func (claudeCode) Name() string { return "Claude Code" }
func (claudeCode) Verify() string {
	return "claude /status  (check the base URL and auth token source)"
}

func (c claudeCode) Targets(env Env) []Target {
	return []Target{{Path: filepath.Join(env.claudeDir(), "settings.json"), Mode: 0644}}
}

// Warnings reports project-scoped settings, which outrank the user settings
// file we write. Claude Code precedence is managed > CLI > project local >
// project > user.
func (c claudeCode) Warnings(env Env) []string {
	var warnings []string
	for _, name := range []string{"settings.json", "settings.local.json"} {
		path := filepath.Join(env.cwd(), ".claude", name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if bytes.Contains(data, []byte("ANTHROPIC_BASE_URL")) ||
			bytes.Contains(data, []byte("ANTHROPIC_AUTH_TOKEN")) {
			warnings = append(warnings, fmt.Sprintf(
				"%s sets ANTHROPIC_* env values; project settings override your user settings.json", path))
		}
	}
	return warnings
}

func (c claudeCode) Plan(opts Options, env Env) ([]FileChange, error) {
	path := filepath.Join(env.claudeDir(), "settings.json")
	old, err := readIfExists(path)
	if err != nil {
		return nil, err
	}
	values := map[string]string{
		"ANTHROPIC_BASE_URL":   opts.BaseURL() + "/anthropic",
		"ANTHROPIC_AUTH_TOKEN": PlaceholderKey,
	}
	// Claude Code has no model list: lock the built-in opus/sonnet/haiku slots
	// and ANTHROPIC_MODEL to Claude deployments only. Do NOT set
	// ENABLE_TOOL_SEARCH — a non-first-party base URL disables MCP tool search
	// by default, and this proxy does not forward tool_reference blocks.
	models := ModelsFor(opts, FamilyClaude)
	if len(models) > 0 {
		first := models[0].ID
		values["ANTHROPIC_MODEL"] = first
		values["ANTHROPIC_DEFAULT_OPUS_MODEL"] = first
		values["ANTHROPIC_DEFAULT_SONNET_MODEL"] = first
		values["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = first
		for _, m := range models {
			switch {
			case strings.Contains(m.ID, "opus"):
				values["ANTHROPIC_DEFAULT_OPUS_MODEL"] = m.ID
			case strings.Contains(m.ID, "haiku"):
				values["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = m.ID
			default:
				values["ANTHROPIC_DEFAULT_SONNET_MODEL"] = m.ID
			}
		}
	}
	updated, err := mergeEnvJSON(old, values)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return []FileChange{{Path: path, Old: old, New: updated, Mode: 0644}}, nil
}

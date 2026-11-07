package agentconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func testEnv(t *testing.T) Env {
	t.Helper()
	return Env{
		Home:   t.TempDir(),
		Cwd:    t.TempDir(),
		Getenv: func(string) string { return "" },
	}
}

func write(t *testing.T, path string, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// stripJSONC removes comments and trailing commas so generated JSONC can be
// decoded with encoding/json in assertions.
func stripJSONC(s string) string {
	var out strings.Builder
	inString := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			out.WriteByte(c)
			if c == '\\' && i+1 < len(s) {
				i++
				out.WriteByte(s[i])
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '/' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			if i < len(s) {
				out.WriteByte('\n')
			}
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '*' {
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			i++
			continue
		}
		out.WriteByte(c)
	}
	return trailingComma.ReplaceAllString(out.String(), "$1")
}

var trailingComma = regexp.MustCompile(`,(\s*[}\]])`)

func decodeJSONC(t *testing.T, data string) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal([]byte(stripJSONC(data)), &result); err != nil {
		t.Fatalf("decode JSONC: %v\n%s", err, data)
	}
	return result
}

func TestCodexPlanPreservesExistingTOML(t *testing.T) {
	env := testEnv(t)
	configPath := filepath.Join(env.codexDir(), "config.toml")
	original := "notify = [ \"C:\\\\bin\\\\node.exe\", \"turn-ended\" ]\n\n" +
		"[marketplaces.openai-primary-runtime]\n" +
		"source_type = \"local\"\n" +
		"source = '\\\\?\\C:\\Users\\me\\runtime'\n\n" +
		"[plugins.\"pdf@openai-primary-runtime\"]\n" +
		"enabled = true\n"
	write(t, configPath, original)

	opts := Options{Port: 3001}
	changes, err := (codex{}).Plan(opts, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes=%d, want 2", len(changes))
	}
	configNew := string(changes[0].New)
	if !strings.Contains(configNew, `source = '\\?\C:\Users\me\runtime'`) {
		t.Error("single-quoted Windows path was rewritten")
	}
	if !strings.Contains(configNew, `model_provider = "aicoreproxy"`) {
		t.Error("missing model_provider")
	}
	if strings.Index(configNew, "model_provider") > strings.Index(configNew, "[marketplaces") {
		t.Error("top-level key was placed after the first [table]")
	}
	if !strings.Contains(configNew, `base_url = "http://localhost:3001/openai/v1"`) {
		t.Error("base_url must keep /openai/v1 because Codex appends /responses")
	}
	if !strings.Contains(configNew, `wire_api = "responses"`) ||
		!strings.Contains(configNew, "requires_openai_auth = true") {
		t.Error("missing required provider fields")
	}
	if strings.Contains(configNew, "supports_websockets") {
		t.Error("legacy supports_websockets field should not be written")
	}
	auth := decodeJSONC(t, string(changes[1].New))
	if auth["auth_mode"] != "apikey" || auth["OPENAI_API_KEY"] != PlaceholderKey {
		t.Errorf("unexpected auth.json: %v", auth)
	}
	if changes[1].Mode != 0600 {
		t.Errorf("auth.json mode=%v, want 0600", changes[1].Mode)
	}

	// A second run must be a no-op (idempotence).
	write(t, configPath, configNew)
	write(t, filepath.Join(env.codexDir(), "auth.json"), string(changes[1].New))
	again, err := (codex{}).Plan(opts, env)
	if err != nil {
		t.Fatal(err)
	}
	for i, change := range again {
		if change.Changed() {
			t.Errorf("change %d is not idempotent:\n%s", i, Diff(change.Path, change.Old, change.New))
		}
	}
}

func TestCodexPlanCreatesFilesWhenMissing(t *testing.T) {
	env := testEnv(t)
	changes, err := (codex{}).Plan(Options{Port: 6655}, env)
	if err != nil {
		t.Fatal(err)
	}
	if !changes[0].Created() || !changes[1].Created() {
		t.Error("expected both files to be new")
	}
	if !strings.Contains(string(changes[0].New), `base_url = "http://localhost:6655/openai/v1"`) {
		t.Error("port override not applied")
	}
}

func TestClaudeCodePlanMergesEnv(t *testing.T) {
	env := testEnv(t)
	path := filepath.Join(env.claudeDir(), "settings.json")
	write(t, path, `{"env":{"MY_VAR":"keep"},"theme":"dark"}`)

	changes, err := (claudeCode{}).Plan(Options{Port: 3001}, env)
	if err != nil {
		t.Fatal(err)
	}
	root := decodeJSONC(t, string(changes[0].New))
	if root["theme"] != "dark" {
		t.Error("unrelated top-level key was lost")
	}
	values, ok := root["env"].(map[string]any)
	if !ok {
		t.Fatalf("env is %T", root["env"])
	}
	if values["MY_VAR"] != "keep" {
		t.Error("unrelated env var was lost")
	}
	if values["ANTHROPIC_BASE_URL"] != "http://localhost:3001/anthropic" {
		t.Errorf("base URL=%v", values["ANTHROPIC_BASE_URL"])
	}
	if values["ANTHROPIC_AUTH_TOKEN"] != PlaceholderKey {
		t.Errorf("auth token=%v", values["ANTHROPIC_AUTH_TOKEN"])
	}
	for _, key := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL"} {
		v, _ := values[key].(string)
		if !strings.HasPrefix(v, "claude-") {
			t.Errorf("%s=%q is not a Claude model", key, v)
		}
	}
	if _, ok := values["ENABLE_TOOL_SEARCH"]; ok {
		t.Error("ENABLE_TOOL_SEARCH must not be set for this proxy")
	}
	write(t, path, string(changes[0].New))
	again, err := (claudeCode{}).Plan(Options{Port: 3001}, env)
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Changed() {
		t.Errorf("not idempotent:\n%s", Diff(path, again[0].Old, again[0].New))
	}
}

func TestOpenCodePlanCreatesMinimalProviders(t *testing.T) {
	env := testEnv(t)
	env.Getenv = func(key string) string {
		if key == "XDG_CONFIG_HOME" {
			return filepath.Join(env.Home, "xdg")
		}
		return ""
	}
	changes, err := (openCode{}).Plan(Options{Port: 3001}, env)
	if err != nil {
		t.Fatal(err)
	}
	if !changes[0].Created() {
		t.Error("expected a new config file")
	}
	root := decodeJSONC(t, string(changes[0].New))
	providers := root["providers"].(map[string]any)
	if len(providers) != 2 {
		t.Errorf("providers=%v, want exactly 2", providers)
	}
	for _, id := range []string{OpenCodeOpenAIID, OpenCodeAnthropicID} {
		p, ok := providers[id].(map[string]any)
		if !ok {
			t.Fatalf("missing provider %s", id)
		}
		models := p["models"].(map[string]any)
		if len(models) != 3 {
			t.Errorf("%s models=%d, want 3", id, len(models))
		}
	}
	if strings.Contains(string(changes[0].New), "gemini") || strings.Contains(string(changes[0].New), "litellm") {
		t.Error("gemini/litellm must not be generated")
	}
	if root["model"] != OpenCodeOpenAIID+"/gpt-6.1-sol" {
		t.Errorf("model=%v", root["model"])
	}
	write(t, changes[0].Path, string(changes[0].New))
	again, err := (openCode{}).Plan(Options{Port: 3001}, env)
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Changed() {
		t.Errorf("not idempotent:\n%s", Diff(changes[0].Path, again[0].Old, again[0].New))
	}
}

func TestOpenCodePlanPreservesJSONCAndPolicies(t *testing.T) {
	env := testEnv(t)
	env.Getenv = func(key string) string {
		if key == "OPENCODE_CONFIG" {
			return filepath.Join(env.Home, "custom", "opencode.jsonc")
		}
		return ""
	}
	path := env.openCodeFile()
	original := `{
  // keep this comment
  "$schema": "https://opencode.ai/config.json",
  "theme": "dark",
  "providers": {
    "my-own": { "name": "Mine", },
  },
  "policies": [
    { "action": "provider.use", "resource": "*", "effect": "deny" },
  ],
}
`
	write(t, path, original)
	changes, err := (openCode{}).Plan(Options{Port: 3001}, env)
	if err != nil {
		t.Fatal(err)
	}
	updated := string(changes[0].New)
	if !strings.Contains(updated, "// keep this comment") {
		t.Error("comment was lost")
	}
	root := decodeJSONC(t, updated)
	providers := root["providers"].(map[string]any)
	if _, ok := providers["my-own"]; !ok {
		t.Error("user-defined provider was lost")
	}
	for _, id := range []string{OpenCodeOpenAIID, OpenCodeAnthropicID} {
		if _, ok := providers[id]; !ok {
			t.Errorf("missing provider %s", id)
		}
	}
	policies, _ := json.Marshal(root["policies"])
	if !strings.Contains(string(policies), `"resource":"aicore-openai"`) ||
		!strings.Contains(string(policies), `"resource":"aicore-anthropic"`) {
		t.Errorf("allow rules missing: %s", policies)
	}
	write(t, path, updated)
	again, err := (openCode{}).Plan(Options{Port: 3001}, env)
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Changed() {
		t.Errorf("not idempotent:\n%s", Diff(path, again[0].Old, again[0].New))
	}
}

func TestParseAgentsAndModels(t *testing.T) {
	ids, err := ParseAgents("claude-code, opencode")
	if err != nil || strings.Join(ids, ",") != "claude-code,opencode" {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	ids, err = ParseAgents("all")
	if err != nil || len(ids) != 3 {
		t.Fatalf("all=%v err=%v", ids, err)
	}
	if _, err := ParseAgents("bogus"); err == nil {
		t.Error("unknown agent should fail")
	}
	models, err := ParseModels("claude=claude-sonnet-4-6,claude-opus-4-8;gpt=gpt-6.1-sol")
	if err != nil {
		t.Fatal(err)
	}
	if len(models[FamilyClaude]) != 2 || len(models[FamilyGPT]) != 1 {
		t.Fatalf("models=%v", models)
	}
	if _, err := ParseModels("gemini=gemini-3.5-flash"); err == nil {
		t.Error("gemini family must be rejected")
	}
}

func TestModelsForFiltersByFamily(t *testing.T) {
	opts := Options{Port: 3001, Available: map[string]bool{"gpt-6.1-sol": true}}
	if len(ModelsFor(opts, FamilyGPT)) != 1 {
		t.Errorf("gpt=%v", ModelsFor(opts, FamilyGPT))
	}
	opts.Available = nil
	for _, m := range ModelsFor(opts, FamilyClaude) {
		if !strings.HasPrefix(m.ID, "claude-") {
			t.Errorf("non-Claude model in Claude family: %s", m.ID)
		}
	}
	for _, m := range ModelsFor(opts, FamilyGPT) {
		if !strings.HasPrefix(m.ID, "gpt-") {
			t.Errorf("non-GPT model in GPT family: %s", m.ID)
		}
	}
}

func TestDiff(t *testing.T) {
	got := Diff("settings.json", []byte("a\nb\n"), []byte("a\nc\n"))
	if !strings.Contains(got, "- b") || !strings.Contains(got, "+ c") {
		t.Fatalf("diff=%s", got)
	}
	if !strings.Contains(Diff("new.json", nil, []byte("x\n")), "new file") {
		t.Error("new file diff missing")
	}
}

func TestApplyWritesBackupAndFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "settings.json")
	write(t, path, "old\n")
	backups, err := Apply([]FileChange{{Path: path, Old: []byte("old\n"), New: []byte("new\n"), Mode: 0600}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 || read(t, backups[0]) != "old\n" {
		t.Fatalf("backups=%v", backups)
	}
	if read(t, path) != "new\n" {
		t.Errorf("file=%q", read(t, path))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no Unix permission bits; Chmod there only toggles read-only.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Errorf("mode=%v, want 0600", info.Mode().Perm())
	}
}

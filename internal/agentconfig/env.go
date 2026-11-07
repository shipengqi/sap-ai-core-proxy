package agentconfig

import (
	"os"
	"path/filepath"
	"runtime"
)

// Env resolves the config locations of the three agents. Getenv is injectable
// so tests never touch the real home directory.
type Env struct {
	Home   string
	Cwd    string
	Getenv func(string) string
}

func DefaultEnv() Env {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	return Env{Home: home, Cwd: cwd, Getenv: os.Getenv}
}

func (e Env) get(key string) string {
	if e.Getenv != nil {
		return e.Getenv(key)
	}
	return os.Getenv(key)
}

func (e Env) home() string {
	if e.Home != "" {
		return e.Home
	}
	return "."
}

func (e Env) cwd() string {
	if e.Cwd != "" {
		return e.Cwd
	}
	return "."
}

// configDir is the XDG base directory; on Windows it is HOME/.config, which
// is also where OpenCode reads its global config.
func (e Env) configDir() string {
	if v := e.get("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return filepath.Join(e.home(), ".config")
}

func (e Env) claudeDir() string {
	if v := e.get("CLAUDE_CONFIG_DIR"); v != "" {
		return v
	}
	return filepath.Join(e.home(), ".claude")
}

func (e Env) codexDir() string {
	if v := e.get("CODEX_HOME"); v != "" {
		return v
	}
	return filepath.Join(e.home(), ".codex")
}

// openCodeFile prefers an existing file over creating a second one: OpenCode
// reads both .json and .jsonc, and having both would make precedence unclear.
func (e Env) openCodeFile() string {
	if v := e.get("OPENCODE_CONFIG"); v != "" {
		return v
	}
	dir := filepath.Join(e.configDir(), "opencode")
	for _, name := range []string{"opencode.jsonc", "opencode.json"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join(dir, "opencode.jsonc")
}

// managedOpenCodeDir is the machine-wide config directory. Managed settings
// and policies outrank everything we write.
func (e Env) managedOpenCodeDir() string {
	if runtime.GOOS == "windows" {
		if v := e.get("ProgramData"); v != "" {
			return filepath.Join(v, "opencode")
		}
		return ""
	}
	return "/etc/opencode"
}

func readIfExists(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

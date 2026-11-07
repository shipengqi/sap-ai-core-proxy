// Package agentconfig writes proxy settings into the config files of the
// supported coding agents. Plans are pure functions of the existing file
// contents; the caller owns confirmation, backups and the actual writes.
package agentconfig

import (
	"fmt"
	"os"
	"strings"
)

const (
	// PlaceholderKey is a non-empty stand-in. The proxy authenticates to SAP AI
	// Core with its own credentials, so agents only need something to send.
	PlaceholderKey = "aicoreproxy"
	// CodexProviderID is the custom Codex provider table name.
	CodexProviderID = "aicoreproxy"
	// OpenCode provider IDs. These are also the prefixes used in model refs.
	OpenCodeOpenAIID    = "aicore-openai"
	OpenCodeAnthropicID = "aicore-anthropic"
)

// Family groups the small curated model catalog by upstream model family.
type Family string

const (
	FamilyClaude Family = "claude"
	FamilyGPT    Family = "gpt"
)

// Target is one file an agent reads.
type Target struct {
	Path string
	Mode os.FileMode
}

// FileChange is the result of planning one file. Old is nil for a new file.
type FileChange struct {
	Path string
	Old  []byte
	New  []byte
	Mode os.FileMode
}

func (c FileChange) Changed() bool { return string(c.Old) != string(c.New) }
func (c FileChange) Created() bool { return len(c.Old) == 0 }

func (c FileChange) Status() string {
	switch {
	case !c.Changed():
		return "unchanged"
	case c.Created():
		return "new"
	default:
		return "update"
	}
}

// Options drives Plan and the configure command.
type Options struct {
	Port      int
	Agents    []string
	Models    map[Family][]string
	Yes       bool
	DryRun    bool
	Status    bool
	Discover  bool
	Available map[string]bool
}

func (o Options) BaseURL() string { return fmt.Sprintf("http://localhost:%d", o.Port) }

func (o Options) allowed(id string) bool {
	if o.Available == nil {
		return true
	}
	return o.Available[id]
}

// Agent is implemented once per supported coding agent.
type Agent interface {
	ID() string
	Name() string
	Targets(Env) []Target
	Plan(Options, Env) ([]FileChange, error)
	// Warnings reports higher-priority config sources that may override us.
	Warnings(Env) []string
	// Verify is the command a user runs inside the agent to confirm the result.
	Verify() string
}

var constructors = []func() Agent{
	func() Agent { return claudeCode{} },
	func() Agent { return codex{} },
	func() Agent { return openCode{} },
}

func All() []Agent {
	agents := make([]Agent, 0, len(constructors))
	for _, fn := range constructors {
		agents = append(agents, fn())
	}
	return agents
}

func ByID(id string) (Agent, bool) {
	for _, a := range All() {
		if a.ID() == id {
			return a, true
		}
	}
	return nil, false
}

func IDs() []string {
	agents := All()
	ids := make([]string, 0, len(agents))
	for _, a := range agents {
		ids = append(ids, a.ID())
	}
	return ids
}

// ParseAgents accepts comma or space separated IDs or "all".
func ParseAgents(spec string) ([]string, error) {
	spec = strings.TrimSpace(spec)
	if strings.EqualFold(spec, "all") {
		return IDs(), nil
	}
	fields := strings.FieldsFunc(spec, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	seen := map[string]bool{}
	result := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if _, ok := ByID(f); !ok {
			return nil, fmt.Errorf("unknown agent %q (choose from %s)", f, strings.Join(IDs(), ", "))
		}
		if !seen[f] {
			seen[f] = true
			result = append(result, f)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no agents selected")
	}
	return result, nil
}

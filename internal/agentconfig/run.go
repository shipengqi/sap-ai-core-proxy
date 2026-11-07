package agentconfig

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
)

// Run owns the interactive flow: selection, model choice, diff review,
// confirmation and the actual writes. It takes explicit streams so the whole
// command is testable without a terminal or a real home directory.
func Run(in io.Reader, out io.Writer, opts Options) error {
	env := DefaultEnv()
	if opts.Port <= 0 {
		opts.Port = 3001
	}
	if opts.Status {
		return printStatus(out, opts, env)
	}
	if opts.Discover {
		available, err := DiscoverModels(opts.BaseURL())
		if err != nil {
			_, _ = fmt.Fprintf(out, "Model discovery unavailable (%v); using the curated catalog.\n\n", err)
		} else {
			opts.Available = available
			_, _ = fmt.Fprintf(out, "Discovered %d running deployments from %s.\n\n", len(available), opts.BaseURL())
		}
	}

	reader := bufio.NewReader(in)
	terminal := isTerminal(in)
	ids := opts.Agents
	if len(ids) == 0 {
		if !terminal {
			return fmt.Errorf("interactive selection needs a terminal; pass --agents %s --yes", strings.Join(IDs(), ","))
		}
		_, _ = fmt.Fprintf(out, "Proxy URL: %s\n\nSelect agents to configure:\n", opts.BaseURL())
		for i, agent := range All() {
			_, _ = fmt.Fprintf(out, "  [%d] %-12s %s\n", i+1, agent.Name(), targetList(agent.Targets(env)))
		}
		_, _ = fmt.Fprint(out, "Selection (e.g. \"1 3\" or \"all\", empty to cancel): ")
		line, err := readLine(reader)
		if err != nil {
			return err
		}
		ids, err = parseSelection(line)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			_, _ = fmt.Fprintln(out, "Cancelled.")
			return nil
		}
	}

	agents := make([]Agent, 0, len(ids))
	for _, id := range ids {
		agent, ok := ByID(id)
		if !ok {
			return fmt.Errorf("unknown agent %q (choose from %s)", id, strings.Join(IDs(), ", "))
		}
		agents = append(agents, agent)
	}

	if terminal && !opts.Yes {
		chosen, err := chooseModels(reader, out, opts, agents)
		if err != nil {
			return err
		}
		if chosen != nil {
			opts.Models = chosen
		}
	}

	var changes []FileChange
	for _, agent := range agents {
		plan, err := agent.Plan(opts, env)
		if err != nil {
			return err
		}
		for _, w := range agent.Warnings(env) {
			_, _ = fmt.Fprintf(out, "WARNING [%s]: %s\n", agent.Name(), w)
		}
		changes = append(changes, plan...)
	}

	_, _ = fmt.Fprintf(out, "\nProxy URL: %s\n\nPlanned changes:\n", opts.BaseURL())
	for _, change := range changes {
		_, _ = fmt.Fprintf(out, "  %-8s %s\n", change.Status(), change.Path)
	}
	_, _ = fmt.Fprintln(out)
	for _, change := range changes {
		if change.Changed() {
			_, _ = fmt.Fprint(out, Diff(change.Path, change.Old, change.New))
			_, _ = fmt.Fprintln(out)
		}
	}
	if opts.DryRun {
		_, _ = fmt.Fprintln(out, "Dry run: no files were written.")
		return nil
	}
	if !opts.Yes {
		if !terminal {
			return fmt.Errorf("confirmation needs a terminal; pass --yes to apply non-interactively")
		}
		_, _ = fmt.Fprint(out, "Write these files? [y/N]: ")
		answer, err := readLine(reader)
		if err != nil {
			return err
		}
		if !strings.EqualFold(strings.TrimSpace(answer), "y") && !strings.EqualFold(strings.TrimSpace(answer), "yes") {
			_, _ = fmt.Fprintln(out, "Cancelled.")
			return nil
		}
	}

	changed := false
	for _, change := range changes {
		if change.Changed() {
			changed = true
			break
		}
	}
	backups, err := Apply(changes, true)
	if err != nil {
		return err
	}
	if changed {
		_, _ = fmt.Fprintln(out, "Configuration written.")
	} else {
		_, _ = fmt.Fprintln(out, "Already configured; nothing to change.")
	}
	if len(backups) > 0 {
		_, _ = fmt.Fprintln(out, "\nBackups:")
		for _, path := range backups {
			_, _ = fmt.Fprintf(out, "  %s\n", path)
		}
	}
	_, _ = fmt.Fprintln(out, "\nVerify in each agent:")
	for _, agent := range agents {
		_, _ = fmt.Fprintf(out, "  %-12s %s\n", agent.Name(), agent.Verify())
	}
	_, _ = fmt.Fprintf(out, "\nProxy health: curl %s/health\n", opts.BaseURL())
	_, _ = fmt.Fprintf(out, "Available models: curl %s/openai/v1/models\n", opts.BaseURL())
	return nil
}

func printStatus(out io.Writer, opts Options, env Env) error {
	_, _ = fmt.Fprintf(out, "Proxy URL: %s\n", opts.BaseURL())
	for _, agent := range All() {
		_, _ = fmt.Fprintf(out, "\n%s (%s)\n", agent.Name(), agent.ID())
		plan, err := agent.Plan(opts, env)
		if err != nil {
			return err
		}
		for _, change := range plan {
			state := change.Status()
			if bytes.Contains(change.Old, managedMarker(agent.ID())) {
				state += ", already configured"
			}
			_, _ = fmt.Fprintf(out, "  %s: %s\n", change.Path, state)
		}
		for _, w := range agent.Warnings(env) {
			_, _ = fmt.Fprintf(out, "  WARNING: %s\n", w)
		}
	}
	return nil
}

func managedMarker(id string) []byte {
	switch id {
	case "claude-code":
		return []byte("ANTHROPIC_BASE_URL")
	case "codex":
		return []byte("[model_providers.aicoreproxy]")
	default:
		return []byte(OpenCodeOpenAIID)
	}
}

func targetList(targets []Target) string {
	paths := make([]string, 0, len(targets))
	for _, t := range targets {
		paths = append(paths, t.Path)
	}
	return strings.Join(paths, ", ")
}

func isTerminal(in io.Reader) bool {
	file, ok := in.(*os.File)
	if !ok {
		return false
	}
	stat, err := file.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice != 0
}

func readLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// parseSelection accepts numbers, comma/space separated, or "all".
func parseSelection(line string) ([]string, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, nil
	}
	if strings.EqualFold(line, "all") {
		return IDs(), nil
	}
	all := IDs()
	var selected []string
	for _, field := range strings.FieldsFunc(line, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	}) {
		var n int
		if _, err := fmt.Sscanf(field, "%d", &n); err != nil || n < 1 || n > len(all) {
			return nil, fmt.Errorf("invalid selection %q (choose 1-%d or all)", field, len(all))
		}
		selected = append(selected, all[n-1])
	}
	return selected, nil
}

// chooseModels lets each agent pick from its own families. Returning nil keeps
// the curated catalog (the default when the user just presses Enter).
func chooseModels(reader *bufio.Reader, out io.Writer, opts Options, agents []Agent) (map[Family][]string, error) {
	var families []Family
	seen := map[Family]bool{}
	for _, agent := range agents {
		for _, family := range agentFamilies(agent.ID()) {
			if !seen[family] {
				seen[family] = true
				families = append(families, family)
			}
		}
	}
	type entry struct {
		family Family
		id     string
	}
	var entries []entry
	_, _ = fmt.Fprintln(out, "\nModels:")
	for _, family := range families {
		for _, m := range ModelsFor(opts, family) {
			entries = append(entries, entry{family, m.ID})
			_, _ = fmt.Fprintf(out, "  [%d] %s\n", len(entries), m.ID)
		}
	}
	_, _ = fmt.Fprint(out, "Selection (Enter = keep the defaults, e.g. \"1 3\"): ")
	line, err := readLine(reader)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(line) == "" {
		return nil, nil
	}
	chosen := map[Family][]string{}
	for _, field := range strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		var n int
		if _, err := fmt.Sscanf(field, "%d", &n); err != nil || n < 1 || n > len(entries) {
			return nil, fmt.Errorf("invalid model selection %q", field)
		}
		e := entries[n-1]
		chosen[e.family] = append(chosen[e.family], e.id)
	}
	if len(chosen) == 0 {
		return nil, nil
	}
	return chosen, nil
}

func agentFamilies(id string) []Family {
	switch id {
	case "claude-code":
		return []Family{FamilyClaude}
	case "codex":
		return []Family{FamilyGPT}
	default:
		return []Family{FamilyGPT, FamilyClaude}
	}
}

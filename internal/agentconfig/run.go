package agentconfig

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/huh/v2"
)

// Run owns the interactive flow: selection, model choice, diff review,
// confirmation and the actual writes. It takes explicit streams so the whole
// command is testable without a terminal or a real home directory.
//
// Interactions are driven by charm.land/huh/v2 forms. When the injected input
// is not a terminal (a buffer, a pipe, Docker/CI) the forms automatically fall
// back to huh's line-based "accessible" prompts instead of starting a Bubble
// Tea program, so scripts and unit tests can still drive every step through
// in/out. In that fallback the input is wrapped once in a line-delimited reader
// shared by every form, because huh rebuilds a bufio.Scanner per prompt and
// would otherwise drain and discard the answers meant for later forms. The
// fully non-interactive path (--yes with --agents/--models) builds no form at all.
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

	// A Bubble Tea program needs a terminal. For every other reader we degrade
	// to huh's accessible prompts while still honouring the injected streams.
	//
	// isTerminal must be called on the original reader (wrapping turns it into a
	// non-*os.File). In accessible mode wrap in ONCE with a line-delimited reader
	// shared by all three runForm calls: huh's PromptString builds a fresh
	// bufio.Scanner on every question, and that Scanner greedily reads a 4096
	// buffer then discards whatever it did not consume. Without a shared wrapper
	// the first form's first prompt swallows the input for every later form, so
	// they hit EOF and Confirm falls back to its default "N". TTY input is left
	// untouched as a raw byte stream for tea.WithInput.
	accessible := !isTerminal(in)
	if accessible {
		in = newLineDelimitedReader(in)
	}

	ids := opts.Agents
	if len(ids) == 0 {
		_, _ = fmt.Fprintf(out, "Proxy URL: %s\n\n", opts.BaseURL())
		var selected []string
		field := huh.NewMultiSelect[string]().
			Title("Select agents to configure").
			Options(agentOptions(env)...).
			Value(&selected)
		aborted, err := runForm(in, out, accessible, field)
		if err != nil {
			return err
		}
		// An explicit abort, or confirming an empty selection, is a cancel.
		if aborted || len(selected) == 0 {
			_, _ = fmt.Fprintln(out, "Cancelled.")
			return nil
		}
		ids = selected
	}

	agents := make([]Agent, 0, len(ids))
	for _, id := range ids {
		agent, ok := ByID(id)
		if !ok {
			return fmt.Errorf("unknown agent %q (choose from %s)", id, strings.Join(IDs(), ", "))
		}
		agents = append(agents, agent)
	}

	if !opts.Yes {
		options, byValue := modelOptions(opts, agents)
		var selected []string
		field := huh.NewMultiSelect[string]().
			Title("Select models").
			Description("Enter keeps the curated defaults").
			Options(options...).
			Value(&selected)
		aborted, err := runForm(in, out, accessible, field)
		if err != nil {
			return err
		}
		if aborted {
			_, _ = fmt.Fprintln(out, "Cancelled.")
			return nil
		}
		// An empty selection keeps the curated catalog; otherwise the chosen
		// models replace it, grouped by family.
		if chosen := groupModelChoices(selected, byValue); chosen != nil {
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
		var confirmed bool
		field := huh.NewConfirm().
			Title("Write these files?").
			Affirmative("Yes").
			Negative("No").
			Value(&confirmed)
		aborted, err := runForm(in, out, accessible, field)
		if err != nil {
			return err
		}
		if aborted || !confirmed {
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

// runForm builds a huh form over the injected streams and runs it. Aborts are
// normalised to a boolean so each interactive step can map them to the same
// "Cancelled." message. WithInput/WithOutput guarantee the form never reaches
// for os.Stdin/os.Stdout directly.
func runForm(in io.Reader, out io.Writer, accessible bool, fields ...huh.Field) (bool, error) {
	form := huh.NewForm(huh.NewGroup(fields...)).
		WithInput(in).
		WithOutput(out).
		WithAccessible(accessible).
		WithShowHelp(false)
	err := form.Run()
	if isUserAbort(err) {
		return true, nil
	}
	return false, err
}

// lineDelimitedReader adapts a non-TTY input stream so that each Read returns
// at most one line (including its trailing '\n').
//
// huh v2's accessible mode builds a fresh bufio.Scanner on every prompt
// (internal/accessibility/accessibility.go:PromptString). A Scanner fills a
// 4096-byte buffer with one greedy Read and then throws away the short-lived
// Scanner together with any bytes that the current question did not consume.
// When Run shares one raw reader across several independent forms, the first
// form's first prompt therefore swallows the answers intended for all later
// forms, leaving them at EOF and making Confirm fall back to its default "N".
//
// A single long-lived bufio.Reader takes the stream line by line, while each
// Read here hands out only one line, so every Scanner consumes exactly one
// answer and the rest stays buffered for the next question. The instance MUST
// be shared across all forms. It is for the accessible fallback only; TTY input
// is passed to tea.WithInput as a raw byte stream and must never be wrapped.
type lineDelimitedReader struct {
	source  *bufio.Reader
	pending []byte
	done    bool
	err     error
}

func newLineDelimitedReader(in io.Reader) *lineDelimitedReader {
	return &lineDelimitedReader{source: bufio.NewReader(in)}
}

func (r *lineDelimitedReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.pending) == 0 && !r.done {
		line, err := r.source.ReadBytes('\n')
		// ReadBytes returns a whole line (possibly longer than the Scanner's
		// 4096-byte buffer), and also the final line even without a '\n'.
		r.pending = line
		if err != nil {
			// Remember the error but do NOT return it on a Read that still
			// carries data: emit the pending bytes first and defer the
			// EOF/error to the next Read once everything has been delivered.
			r.done = true
			r.err = err
		}
	}
	if len(r.pending) > 0 {
		n := copy(p, r.pending)
		r.pending = r.pending[n:]
		return n, nil
	}
	return 0, r.err
}

// isUserAbort reports whether err represents a huh user abort (Ctrl+C in the
// Bubble Tea renderer). Accessible mode never produces it, but the mapping is
// kept in one place so every interactive step stays consistent and testable.
func isUserAbort(err error) bool {
	return errors.Is(err, huh.ErrUserAborted)
}

// agentOptions renders the agent list for the selection form. The option key is
// the human-readable label; the option value is the agent ID that gets stored.
func agentOptions(env Env) []huh.Option[string] {
	all := All()
	options := make([]huh.Option[string], 0, len(all))
	for _, agent := range all {
		label := fmt.Sprintf("%-12s %s", agent.Name(), targetList(agent.Targets(env)))
		options = append(options, huh.NewOption(label, agent.ID()))
	}
	return options
}

// modelChoice remembers which family a selectable model belongs to.
type modelChoice struct {
	family Family
	id     string
}

// modelOptions builds the flat, numbered list of selectable models for the
// chosen agents (mirroring the old curated catalog) plus a lookup keyed by a
// unique encoded value so selected IDs can be regrouped by family afterwards.
func modelOptions(opts Options, agents []Agent) ([]huh.Option[string], map[string]modelChoice) {
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
	var options []huh.Option[string]
	byValue := map[string]modelChoice{}
	for _, family := range families {
		for _, m := range ModelsFor(opts, family) {
			value := string(family) + "\x00" + m.ID
			byValue[value] = modelChoice{family: family, id: m.ID}
			options = append(options, huh.NewOption(m.ID, value))
		}
	}
	return options, byValue
}

// groupModelChoices turns the selected encoded values back into a per-family
// model map. An empty selection returns nil so Run keeps the curated defaults.
func groupModelChoices(selected []string, byValue map[string]modelChoice) map[Family][]string {
	grouped := map[Family][]string{}
	for _, value := range selected {
		if choice, ok := byValue[value]; ok {
			grouped[choice.family] = append(grouped[choice.family], choice.id)
		}
	}
	if len(grouped) == 0 {
		return nil
	}
	return grouped
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

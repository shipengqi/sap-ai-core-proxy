// Package cli defines the shared commands for the proxy and standalone tools.
package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/shipengqi/sap-ai-core-proxy/internal/cacheprobe"
	"github.com/shipengqi/sap-ai-core-proxy/internal/dashboard"
	"github.com/shipengqi/sap-ai-core-proxy/internal/version"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type usageError struct{ error }
type reportedError struct{ error }

// Execute owns diagnostics and exit codes; commands never exit the process.
// Like the old flag-based executables, syntax errors exit 2 and runtime errors 1.
func Execute(cmd *cobra.Command, args []string, out, errOut io.Writer) int {
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs(legacyArgs(cmd, args))
	if err := cmd.Execute(); err != nil {
		var reported reportedError
		if !errors.As(err, &reported) {
			_, _ = fmt.Fprintln(errOut, err)
		}
		var syntax usageError
		if errors.As(err, &syntax) {
			return 2
		}
		return 1
	}
	return 0
}

func command(use, short string) *cobra.Command {
	cmd := &cobra.Command{Use: use, Short: short, SilenceUsage: true, SilenceErrors: true,
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.NoArgs(cmd, args); err != nil {
				return usageError{err}
			}
			return nil
		},
	}
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	return cmd
}

// NewRootCommand preserves no-argument server startup for Docker and systemd.
func NewRootCommand() *cobra.Command { return newRootCommand(runServer) }

func newRootCommand(serve func(bool) error) *cobra.Command {
	root := command("aicoreproxy", "Proxy Anthropic, OpenAI and Gemini requests to SAP AI Core")
	root.CompletionOptions.DisableDefaultCmd = true
	addServerFlags(root, serve)
	server := command("serve", "Start the proxy (also the default command)")
	addServerFlags(server, serve)
	v := command("version", "Print version and build information")
	v.RunE = func(cmd *cobra.Command, _ []string) error {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), version.String())
		return err
	}
	root.AddCommand(server, NewTUICommand(), NewCacheProbeCommand(), v)
	return root
}

func addServerFlags(cmd *cobra.Command, serve func(bool) error) {
	var noTUI, showVersion bool
	cmd.Flags().BoolVar(&noTUI, "no-tui", false, "run only the Gin server")
	cmd.Flags().BoolVar(&showVersion, "version", false, "print version information and exit")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		// This must precede terminal probing, config loading and all server setup.
		if showVersion {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), version.String())
			return err
		}
		return serve(noTUI)
	}
}

// NewTUICommand is shared by the subcommand and the standalone executable.
func NewTUICommand() *cobra.Command {
	opts := dashboard.Options{}
	cmd := command("tui", "Show a dashboard for an existing proxy")
	cmd.Flags().StringVar(&opts.URL, "url", "http://localhost:3001", "proxy base URL")
	cmd.Flags().DurationVar(&opts.Interval, "interval", 2*time.Second, "refresh interval")
	cmd.Flags().BoolVar(&opts.Plain, "plain", false, "print one snapshot and exit")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return dashboard.Run(cmd.Context(), cmd.OutOrStdout(), opts)
	}
	return cmd
}

// NewCacheProbeCommand sends the same direct SAP probes as the standalone tool.
func NewCacheProbeCommand() *cobra.Command {
	opts := cacheprobe.Options{}
	cmd := command("cacheprobe", "Probe SAP prompt-cache capabilities (sends billable model requests)")
	cmd.Flags().StringVar(&opts.Models, "models", "gpt-5.6-terra,gpt-4o", "comma-separated deployment model names")
	cmd.Flags().DurationVar(&opts.Timeout, "timeout", 120*time.Second, "per-request timeout")
	cmd.Flags().StringVar(&opts.Salt, "salt", "", "prefix nonce; defaults to the current time so runs do not share cache state")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return cacheprobe.Run(cmd.Context(), cmd.OutOrStdout(), opts)
	}
	return cmd
}

// legacyArgs accepts only known single-dash long flags, not arbitrary rewrites.
// Flag values and everything following -- are kept byte-for-byte intact.
func legacyArgs(root *cobra.Command, args []string) []string {
	// Cobra treats nil args as os.Args[1:], so even an empty copy must be non-nil.
	result := make([]string, len(args))
	copy(result, args)
	cmd := root
	for i := 0; i < len(result); i++ {
		token := result[i]
		if token == "--" {
			break
		}
		if !strings.HasPrefix(token, "-") {
			for _, child := range cmd.Commands() {
				if token == child.Name() {
					cmd = child
					break
				}
			}
			continue
		}
		name, _, assigned := strings.Cut(strings.TrimLeft(token, "-"), "=")
		var flag *pflag.Flag
		if name == "help" {
			flag = &pflag.Flag{NoOptDefVal: "true"}
		} else {
			flag = cmd.Flags().Lookup(name)
		}
		if flag == nil {
			continue
		}
		if strings.HasPrefix(token, "-") && !strings.HasPrefix(token, "--") && len(name) > 1 {
			result[i] = "-" + token
		}
		if !assigned && flag.NoOptDefVal == "" {
			i++
		}
	}
	return result
}

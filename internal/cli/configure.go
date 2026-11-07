package cli

import (
	"github.com/shipengqi/sap-ai-core-proxy/internal/agentconfig"
	"github.com/shipengqi/sap-ai-core-proxy/internal/config"
	"github.com/spf13/cobra"
)

// NewConfigureCommand configures the three supported coding agents. Like
// version/help it never loads SAP credentials and never starts the server.
func NewConfigureCommand() *cobra.Command {
	var agents, models string
	var port int
	var yes, dryRun, status, discover bool

	cmd := command("configure", "Configure Claude Code, Codex and OpenCode to use this proxy")
	cmd.Flags().StringVar(&agents, "agents", "", "comma-separated agents: claude-code,codex,opencode (default: interactive multi-select)")
	cmd.Flags().IntVar(&port, "port", 0, "proxy port; defaults to server.port from config.json, then PORT env, then 3001")
	cmd.Flags().StringVar(&models, "models", "", "override curated models, e.g. \"claude=claude-sonnet-4-6;gpt=gpt-6.1-sol,gpt-5.4\"")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the interactive confirmation (required for non-interactive use)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the planned diff without writing any file")
	cmd.Flags().BoolVar(&status, "status", false, "show the current configuration state and exit")
	cmd.Flags().BoolVar(&discover, "discover", false, "query a running proxy's /openai/v1/models and keep only available models")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		opts := agentconfig.Options{
			Port:     port,
			Yes:      yes,
			DryRun:   dryRun,
			Status:   status,
			Discover: discover,
		}
		if opts.Port == 0 {
			p, err := config.LoadPortOnly()
			if err != nil {
				return err
			}
			opts.Port = p
		}
		if agents != "" {
			ids, err := agentconfig.ParseAgents(agents)
			if err != nil {
				return err
			}
			opts.Agents = ids
		}
		if models != "" {
			parsed, err := agentconfig.ParseModels(models)
			if err != nil {
				return err
			}
			opts.Models = parsed
		}
		return agentconfig.Run(cmd.InOrStdin(), cmd.OutOrStdout(), opts)
	}
	return cmd
}

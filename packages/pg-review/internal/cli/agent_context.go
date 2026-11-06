package cli

import "github.com/spf13/cobra"

func init() { registerCommand(groupAgent, newAgentContextCmd) }

func newAgentContextCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "context",
		Short: "Print the run context: lens, scope, window and known topics",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return notImplemented() },
	}
}

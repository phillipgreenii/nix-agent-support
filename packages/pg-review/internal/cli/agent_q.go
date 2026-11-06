package cli

import "github.com/spf13/cobra"

func init() { registerCommand(groupAgent, newAgentQCmd) }

func newAgentQCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "q <provider> [args...]",
		Short: "Run a read-only evidence query through the broker",
		Args:  cobra.MinimumNArgs(1),
		RunE:  func(*cobra.Command, []string) error { return notImplemented() },
	}
}

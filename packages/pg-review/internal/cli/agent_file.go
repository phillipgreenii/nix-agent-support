package cli

import "github.com/spf13/cobra"

func init() { registerCommand(groupAgent, newAgentFileCmd) }

func newAgentFileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "file <findings.json>",
		Short: "Validate and file a findings document",
		Args:  cobra.ExactArgs(1),
		RunE:  func(*cobra.Command, []string) error { return notImplemented() },
	}
	cmd.Flags().Bool("dry-run", false, "validate and report without filing")
	cmd.Flags().String("run", "", "run id (required for an interactive run; taken from agent context for a ccpool run)")
	return cmd
}

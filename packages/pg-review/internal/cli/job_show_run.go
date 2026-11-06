package cli

import "github.com/spf13/cobra"

func init() { registerCommand(groupJob, newJobShowRunCmd) }

func newJobShowRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show-run <id>",
		Short: "Show one run",
		Args:  cobra.ExactArgs(1),
		RunE:  func(*cobra.Command, []string) error { return notImplemented() },
	}
}

package cli

import "github.com/spf13/cobra"

func init() { registerCommand(groupHuman, newHumanShowCmd) }

func newHumanShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>...",
		Short: "Show one or more review items (records a view of each)",
		Args:  cobra.MinimumNArgs(1),
		RunE:  func(*cobra.Command, []string) error { return notImplemented() },
	}
	cmd.Flags().Bool("evidence", false, "include the evidence behind each item")
	return cmd
}

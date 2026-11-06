package cli

import "github.com/spf13/cobra"

func init() { registerCommand(groupHuman, newHumanPendingCmd) }

func newHumanPendingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pending",
		Short: "List review items awaiting a ruling",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return notImplemented() },
	}
	cmd.Flags().Bool("decided", false, "list decided items instead of pending ones")
	return cmd
}

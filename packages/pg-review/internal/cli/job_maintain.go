package cli

import "github.com/spf13/cobra"

func init() { registerCommand(groupJob, newJobMaintainCmd) }

func newJobMaintainCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "maintain",
		Short: "Reconcile state, retry filing and write the maintain heartbeat",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return notImplemented() },
	}
}

package cli

import "github.com/spf13/cobra"

func init() { registerCommand(groupJob, newJobStatusCmd) }

func newJobStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show run, queue and heartbeat status",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return notImplemented() },
	}
	cmd.Flags().Bool("json", false, "print JSON")
	return cmd
}

package cli

import "github.com/spf13/cobra"

func init() { registerCommand(groupJob, newJobResumeCmd) }

func newJobResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <lens>",
		Short: "Resume a paused lens",
		Args:  cobra.ExactArgs(1),
		RunE:  func(*cobra.Command, []string) error { return notImplemented() },
	}
}

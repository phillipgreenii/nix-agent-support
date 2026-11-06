package cli

import "github.com/spf13/cobra"

func init() { registerCommand(groupJob, newJobDoctorCmd) }

func newJobDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the configuration and the environment",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return notImplemented() },
	}
	cmd.Flags().Bool("config-only", false, "validate config.toml only")
	return cmd
}

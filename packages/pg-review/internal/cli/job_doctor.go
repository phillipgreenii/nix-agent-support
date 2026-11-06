package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/builtin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/exitcode"
)

// assetsFS supplies the embedded assets the config check looks prompt files up
// in; tests replace it.
var assetsFS = builtin.FS

func init() { registerCommand(groupJob, newJobDoctorCmd) }

func newJobDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the configuration and the environment",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if only, _ := cmd.Flags().GetBool("config-only"); only {
				return doctorConfigOnly(cmd)
			}
			return notImplemented()
		},
	}
	cmd.Flags().Bool("config-only", false, "validate config.toml only")
	return cmd
}

// doctorConfigOnly validates the effective configuration (built-ins merged
// with config.toml) and nothing else: no router configuration is read. It
// prints one line per problem and exits 4 when there is any.
func doctorConfigOnly(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	eff, err := config.Load(config.EnvFromOS(), assetsFS())
	if err != nil {
		fmt.Fprintf(out, "config %q: %v\n", "config.toml", err)
		return ExitErrorf(exitcode.ValidationRejected, "configuration is invalid")
	}
	probs := eff.Validate()
	for _, p := range probs {
		fmt.Fprintln(out, p.String())
	}
	if len(probs) > 0 {
		return ExitErrorf(exitcode.ValidationRejected, "configuration is invalid: %d problem(s)", len(probs))
	}
	for _, w := range eff.Warnings() {
		fmt.Fprintln(out, w)
	}
	fmt.Fprintln(out, "configuration is valid")
	return nil
}

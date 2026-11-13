package cli

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/builtin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ccpool"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/doctor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
	"github.com/phillipgreenii/x/exitcode"
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
			skip, _ := cmd.Flags().GetBool("skip-session-check")
			return doctorFull(cmd, skip)
		},
	}
	cmd.Flags().Bool("config-only", false, "validate config.toml only")
	cmd.Flags().Bool("skip-session-check", false, "skip the in-session check, which starts one minimal ccpool session")
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
		return ExitErrorf(exitcode.Config, "configuration is invalid")
	}
	probs := eff.Validate()
	for _, p := range probs {
		fmt.Fprintln(out, p.String())
	}
	if len(probs) > 0 {
		return ExitErrorf(exitcode.Config, "configuration is invalid: %d problem(s)", len(probs))
	}
	for _, w := range eff.Warnings() {
		fmt.Fprintln(out, w)
	}
	fmt.Fprintln(out, "configuration is valid")
	return nil
}

// doctorDeps builds the dependencies of the full doctor run; tests replace it
// with fakes.
var doctorDeps = realDoctorDeps

func realDoctorDeps(ctx context.Context, skipSession bool) *doctor.Deps {
	d := &doctor.Deps{
		LookPath: exec.LookPath,
		Exec:     doctor.OSExec,
		OpenLedger: func(ctx context.Context) (doctor.IntegrityLedger, error) {
			return ledger.Open(ctx)
		},
		Now: time.Now,
		Sleep: func(ctx context.Context, d time.Duration) {
			select {
			case <-ctx.Done():
			case <-time.After(d):
			}
		},
		SkipSession: skipSession,
	}
	d.Config, d.ConfigErr = config.Load(config.EnvFromOS(), assetsFS())
	if d.Config != nil {
		d.Ccpool = ccpool.CLI{PoolDir: d.Config.Ccpool.PoolDir}
	}
	return d
}

// doctorFull runs every environment check, prints one line per check and
// exits with the highest failing code (4 configuration, 6 ledger, 7 provider,
// recorder, ccpool, hook or session).
func doctorFull(cmd *cobra.Command, skipSession bool) error {
	out := cmd.OutOrStdout()
	results := doctor.Run(cmd.Context(), doctorDeps(cmd.Context(), skipSession))
	for _, r := range results {
		fmt.Fprintln(out, r.String())
	}
	if code := doctor.ExitCode(results); code != exitcode.OK {
		return ExitErrorf(code, "doctor found problems")
	}
	return nil
}

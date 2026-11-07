package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/exitcode"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/runner"
)

func init() { registerCommand(groupJob, newJobMaintainCmd) }

func newJobMaintainCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "maintain",
		Short: "Run the periodic housekeeping steps and write the maintain heartbeat",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			cfg, err := config.Load(config.EnvFromOS(), assetsFS())
			if err != nil {
				return err
			}
			l, err := ledger.Open(ctx)
			if err != nil {
				return ExitErrorf(exitcode.Ledger, "%v", err)
			}
			defer l.Close()
			d := &runner.Deps{Ledger: l, Cfg: cfg, Stdout: cmd.OutOrStdout()}
			runner.RegisterCcpool(d)
			if runnerHook != nil {
				runnerHook(d)
			}
			res := runner.Maintain(ctx, d, runner.MaintainOptions{Filer: beadFiler(cfg)})
			out := cmd.OutOrStdout()
			for _, s := range res.Steps {
				if s.Err == nil {
					fmt.Fprintf(out, "%s: ok %s\n", s.Name, s.Detail)
				}
			}
			failed := res.Failed()
			for _, s := range failed {
				fmt.Fprintf(cmd.ErrOrStderr(), "maintain step %s failed: %v\n", s.Name, s.Err)
			}
			if len(failed) > 0 {
				return ExitErrorf(exitcode.Failure, "maintain: %d step(s) failed", len(failed))
			}
			return nil
		},
	}
}

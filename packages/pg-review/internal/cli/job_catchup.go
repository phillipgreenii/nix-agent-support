package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/appcode"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/runner"
	"github.com/phillipgreenii/x/exitcode"
)

func init() { registerCommand(groupJob, newJobCatchUpCmd) }

func newJobCatchUpCmd() *cobra.Command {
	var scope string
	var force bool
	cmd := &cobra.Command{
		Use:   "catch-up [lens]",
		Short: "Re-run one scheduled window that ended Skipped and never Filed",
		Long: `Re-runs the window of a scheduled run that ended Skipped (for example for ccpool
capacity) and has no Filed run, with the same window, run key and schedule id, so a
Filed result stops further catch-up. At most one run per invocation. A window is
retried once at least 55 minutes after its last attempt, at most 24 times, and only
until one more window length has passed after its end. Analyst failures are not
retried. --force (needs a lens) ignores those limits and also re-runs a Failed window.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := runner.CatchUpParams{Scope: scope, Force: force}
			if len(args) == 1 {
				p.Lens = args[0]
			}
			return catchUp(cmd, p)
		},
	}
	f := cmd.Flags()
	f.StringVar(&scope, "scope", "", "only windows of this scope")
	f.BoolVar(&force, "force", false, "ignore the cooldown, attempt cap and expiry; also re-run a Failed window")
	return cmd
}

func catchUp(cmd *cobra.Command, p runner.CatchUpParams) error {
	ctx := cmd.Context()
	cfg, err := config.Load(config.EnvFromOS(), assetsFS())
	if err != nil {
		return err
	}
	l, err := ledger.Open(ctx)
	if err != nil {
		return ExitErrorf(appcode.Ledger, "%v", err)
	}
	defer func() { _ = l.Close() }()
	d := &runner.Deps{Ledger: l, Cfg: cfg, Stdout: cmd.OutOrStdout(), Log: cmd.ErrOrStderr(), Evidence: runner.EvidenceStage{}}
	runner.RegisterCcpool(d)
	if runnerHook != nil {
		runnerHook(d)
	}
	res, err := runner.CatchUp(ctx, d, p)
	var ue *runner.UsageError
	var sk *runner.SkippedError
	var af *runner.AlreadyFiledError
	var pe *runner.ProviderError
	var nothing *runner.NothingToCatchUpError
	switch {
	case err == nil:
		fmt.Fprintf(cmd.OutOrStdout(), "run %d %s\n", res.Run.ID, res.Run.State)
		return nil
	case errors.As(err, &nothing):
		fmt.Fprintln(cmd.OutOrStdout(), nothing.Error())
		return ExitErrorf(appcode.Skipped, "%v", nothing)
	case errors.As(err, &ue):
		return usageErrorf("%s", ue.Msg)
	case errors.As(err, &af):
		return ExitErrorf(appcode.Skipped, "%v", af)
	case errors.As(err, &sk):
		return ExitErrorf(appcode.Skipped, "run %d skipped: %s", sk.RunID, sk.Reason)
	case errors.As(err, &pe):
		return ExitErrorf(appcode.Provider, "%v", pe)
	case errors.Is(err, ledger.ErrLockBusy):
		return ExitErrorf(exitcode.Busy, "another run holds the lock for a window being caught up")
	}
	return mapFileError(err)
}

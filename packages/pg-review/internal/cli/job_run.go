package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/exitcode"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/runner"
)

func init() { registerCommand(groupJob, newJobRunCmd) }

// RunOptions holds the values of the job run flags. The run-lifecycle code
// reads it; this file only registers the flags and stores their values.
type RunOptions struct {
	Lens         string
	Scope        string
	From         string
	To           string
	Duration     string
	At           string
	Profile      string
	Analyst      string
	EvidenceOnly bool
	RunKey       string
	ScheduleID   string
}

// runJob executes the run lifecycle; later work extends runner.Deps through
// runnerHook (evidence stage, analyst backends).
var runJob = defaultRunJob

// runnerHook lets later packets add stages and analysts to the deps of a run.
var runnerHook func(*runner.Deps)

func defaultRunJob(cmd *cobra.Command, o *RunOptions) error {
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
	if runnerHook != nil {
		runnerHook(d)
	}
	res, err := runner.Run(ctx, d, runner.Params{
		Lens: o.Lens, Scope: o.Scope, From: o.From, To: o.To, Duration: o.Duration, At: o.At,
		Profile: o.Profile, Analyst: o.Analyst, EvidenceOnly: o.EvidenceOnly,
		RunKey: o.RunKey, ScheduleID: o.ScheduleID,
	})
	var ue *runner.UsageError
	var sk *runner.SkippedError
	var af *runner.AlreadyFiledError
	switch {
	case err == nil:
		fmt.Fprintf(cmd.OutOrStdout(), "run %d %s\n", res.Run.ID, res.Run.State)
		return nil
	case errors.As(err, &ue):
		return usageErrorf("%s", ue.Msg)
	case errors.As(err, &af):
		return ExitErrorf(exitcode.Skipped, "%v", af)
	case errors.As(err, &sk):
		return ExitErrorf(exitcode.Skipped, "run %d skipped: %s", sk.RunID, sk.Reason)
	case errors.Is(err, ledger.ErrLockBusy):
		return ExitErrorf(exitcode.Busy, "another run holds the lock for lens %s and scope %s", o.Lens, o.Scope)
	}
	return mapFileError(err)
}

func newJobRunCmd() *cobra.Command {
	opts := &RunOptions{}
	cmd := &cobra.Command{
		Use:   "run <lens>",
		Short: "Run one review of a lens over a scope and window",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Lens = args[0]
			if opts.Scope == "" {
				return usageErrorf("required flag --scope not set")
			}
			return runJob(cmd, opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.Scope, "scope", "", "scope name to review")
	f.StringVar(&opts.From, "from", "", "window start (RFC 3339)")
	f.StringVar(&opts.To, "to", "", "window end (RFC 3339)")
	f.StringVar(&opts.Duration, "duration", "", "window length ending at --at (or now)")
	f.StringVar(&opts.At, "at", "", "window end for --duration (RFC 3339; default now)")
	f.StringVar(&opts.Profile, "profile", "", "analyst profile")
	f.StringVar(&opts.Analyst, "analyst", "", "analyst backend: ccpool, interactive or none (overrides the profile)")
	f.BoolVar(&opts.EvidenceOnly, "evidence-only", false, "collect evidence only; no analyst")
	f.StringVar(&opts.RunKey, "run-key", "", "idempotency key, stored verbatim (opaque label supplied by the caller)")
	f.StringVar(&opts.ScheduleID, "schedule-id", "", "schedule id (opaque label supplied by the caller)")
	return cmd
}

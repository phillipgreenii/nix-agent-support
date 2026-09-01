package executor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/prompt"
	"github.com/phillipgreenii/pr-pool/internal/report"
)

// ErrBusy is the sentinel a command role's exit code 9 maps to (Task 2.3,
// pg2-84o3m.22): commandRun.run classifies an *exec.ExitError with
// ExitCode()==9 and wraps it into the error returned through
// Executor.Dispatch, so errors.Is on that error resolves to this sentinel
// through the existing %w chain — no production interface change.
// roleListener.Offer maps it to eventqueue.DeclineBusy: a graceful "not right
// now" PRE-ACCEPT decline (INV-CONC-1), never a delivery failure.
var ErrBusy = errors.New("executor: command exited busy (exit code 9)")

// busyExitCode is the command role's operator-facing "I am busy, retry me"
// signal (perf-F2 in the review digest independently proposes the core reply
// exit 9 on its OWN accept semaphore — the same code, the same meaning).
const busyExitCode = 9

type commandExecutor struct{}

func (commandExecutor) Dispatch(ctx context.Context, d discover.DispatchContext, deps Deps) (report.Result, error) {
	r := &commandRun{deps: deps}
	return report.Result{}, r.run(ctx, d)
}

type commandRun struct{ deps Deps }

// run dispatches a command role: render its argv, run it once, success iff
// exit 0. No ccpool/watchdog. (No built-in command role exists; this path is
// exercised by explicit config.)
func (r *commandRun) run(ctx context.Context, d discover.DispatchContext) error {
	argv, err := r.renderArgv(d.Role.Command.Argv, d)
	if err != nil {
		return fmt.Errorf("command role %q: render argv: %w", d.Role.Name, err)
	}
	if _, err := r.deps.commander().Run(ctx, argv); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == busyExitCode {
			return fmt.Errorf("command role %q item %s: %w: %w", d.Role.Name, d.Item.ID, ErrBusy, err)
		}
		return fmt.Errorf("command role %q item %s: %w", d.Role.Name, d.Item.ID, err)
	}
	return nil
}

// renderArgv interpolates each argv element through the prompt template engine, so a
// command role can reference {{.BeadID}} etc. An element with no template actions
// renders to itself.
func (r *commandRun) renderArgv(argv []string, d discover.DispatchContext) ([]string, error) {
	pctx := prompt.Context{Item: d.Item, WorktreeDir: r.deps.Cfg.WorktreeDir, SelfLogin: r.deps.Cfg.SelfLogin, RepoRoot: r.deps.Cfg.RepoRoot}
	out := make([]string, 0, len(argv))
	for _, a := range argv {
		t, err := prompt.Parse("argv", a)
		if err != nil {
			return nil, err
		}
		s, err := prompt.Render(t, pctx)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

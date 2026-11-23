// Package query carries the narrow argv-execution seam the moved
// ccpool/command executor logic needs: Commander plus its default OS
// implementation. This is NOT a copy of packages/pg-router/internal/query:
// that package is pg-router's full typed-union of event SOURCES (beads
// queries, command queries, triggers) and stays in-core, unreachable from
// here (Go's internal-package visibility rule — docs/adr/0065's Addendum).
// Only the tiny Commander interface travels — the moved code's one remaining
// need from it (workforestIsolation's `pn workspace` invocations, and the
// executor's own default argv-runner seam).
package query

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
)

// CancelGrace is how long a cancelled RunEnv payload's process group has between
// SIGTERM and SIGKILL (the same 5 s the core uses for its own commands).
const CancelGrace = 5 * time.Second

// Commander runs an executable and returns its stdout (one-method interface,
// like beads.Runner / ccpool.Runner — not a bare func field). Mirrors
// packages/pg-router/internal/query.Commander exactly.
type Commander interface {
	Run(ctx context.Context, argv []string) ([]byte, error)
}

// EnvCommander is a Commander that can also run an argv with an explicit
// environment, in its own process group. The command executor uses it for a
// command role's payload; a Commander that is not an EnvCommander (a test
// double) is run through Run, without the environment or the process group.
type EnvCommander interface {
	Commander
	RunEnv(ctx context.Context, argv []string, env []string) ([]byte, error)
}

// FileCommander is an EnvCommander that can also pass open files to the payload
// as extra descriptors (fd 3 onward, os/exec Cmd.ExtraFiles). The command
// executor uses it to hand the payload the per-(role, event) dispatch lock, so
// the lock outlives the adapter process for as long as the payload group lives.
type FileCommander interface {
	EnvCommander
	RunEnvFiles(ctx context.Context, argv []string, env []string, files []*os.File) ([]byte, error)
}

// commandTimeout is the backstop bound on one command run through x/cmdrun.
// The caller's context is the normal limit; this bounds a call whose caller
// passed a context with none.
const commandTimeout = 5 * time.Minute

// ExitCodeError is the error a command run through x/cmdrun that ran but did
// not succeed produces: it exited non-zero (Code) or was ended by a signal
// (Signal, Code -1). Stderr is the child's stderr.
type ExitCodeError struct {
	Code   int
	Signal string
	Stderr []byte
}

// ExitCode is the exit status, or -1 for a signal, as *exec.ExitError reports.
func (e *ExitCodeError) ExitCode() int { return e.Code }

func (e *ExitCodeError) Error() string {
	if e.Signal != "" {
		return "signal: " + e.Signal
	}
	return fmt.Sprintf("exit status %d", e.Code)
}

// ExitDetail reports the exit status and stderr of a command that ran and
// failed, whether it ran through x/cmdrun (*ExitCodeError) or through os/exec
// (*exec.ExitError). ok is false for any other error.
func ExitDetail(err error) (code int, stderr []byte, ok bool) {
	var ce *ExitCodeError
	if errors.As(err, &ce) {
		return ce.Code, ce.Stderr, true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), ee.Stderr, true
	}
	return 0, nil, false
}

// OSCommander is the default Commander: it runs the command through
// x/cmdrun.
type OSCommander struct {
	// Exec runs the call; nil means cmdrun.Exec. Tests inject cmdruntest.Fake.
	Exec cmdrun.Runner
}

func (o OSCommander) Run(ctx context.Context, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty argv")
	}
	return o.runCall(ctx, cmdrun.Call{Name: argv[0], Args: argv[1:], Timeout: commandTimeout})
}

// RunEnv runs argv with exactly env as its environment (os.Environ() form),
// in its own process group. A cancelled ctx sends SIGTERM to the whole group,
// then SIGKILL after CancelGrace, so a non-exec child (a timeout layer, a shell
// script) cannot orphan its payload. Stdout is returned; on failure the
// *ExitCodeError carries the stderr, as *exec.ExitError does for Run.
func (o OSCommander) RunEnv(ctx context.Context, argv []string, env []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty argv")
	}
	return o.runCall(ctx, cmdrun.Call{
		Name: argv[0], Args: argv[1:], Env: env, EnvMode: cmdrun.EnvExact,
		OwnProcGroup: true, TermGrace: CancelGrace, Timeout: commandTimeout,
	})
}

func (o OSCommander) runCall(ctx context.Context, call cmdrun.Call) ([]byte, error) {
	run := o.Exec
	if run == nil {
		run = cmdrun.Exec
	}
	res := run(ctx, call)
	switch {
	case res.StartErr != nil:
		return nil, res.StartErr
	case res.TimedOut:
		return res.Stdout, fmt.Errorf("timed out after %s", commandTimeout)
	case res.CtxErr != nil:
		return res.Stdout, res.CtxErr
	case res.Signal != "" || res.Code != 0:
		return res.Stdout, &ExitCodeError{Code: res.Code, Signal: res.Signal, Stderr: res.Stderr}
	}
	return res.Stdout, nil
}

// RunEnvFiles is RunEnv with files inherited by the payload as fd 3 onward.
// With no files it is RunEnv. With files it runs through x/cmdrun with
// ExtraFiles: the command executor hands the payload the per-(role, event)
// dispatch lock that way, so the lock outlives the adapter for as long as the
// payload group lives. Unlike RunEnv it has no Timeout (the dispatch root is
// uncancelled by design, so a payload ends on its own or with the adapter), but
// a cancelled ctx signals the group the same way, and ReapGroupAfterCancel ends
// any descendant that ignored SIGTERM before returning (ADR 0130).
func (o OSCommander) RunEnvFiles(ctx context.Context, argv []string, env []string, files []*os.File) ([]byte, error) {
	if len(files) == 0 {
		return o.RunEnv(ctx, argv, env)
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty argv")
	}
	//cmdrun:unbounded the dispatch root is uncancelled by design; a payload holding the dispatch lock ends on its own or with the adapter
	return o.runCall(ctx, cmdrun.Call{
		Name: argv[0], Args: argv[1:], Env: env, EnvMode: cmdrun.EnvExact,
		ExtraFiles: files, OwnProcGroup: true, TermGrace: CancelGrace,
		ReapGroupAfterCancel: true,
	})
}

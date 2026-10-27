// runner.go: the package-local command-runner seam. The shape (Call, Result,
// Runner, ExecRunner) deliberately mirrors
// packages/pg-decider/internal/prchange/exec.go so a later merge onto a shared
// x runner package is mechanical; the one addition is Call.WaitDelay, which
// runInvoke needs (see its doc comment). Tests answer the Runner in process
// instead of re-executing the test binary (a race-built child sleeps 1 s at
// exit).
package scriptout

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Call is one subprocess the umbrella runs.
type Call struct {
	Name string   // resolved on $PATH
	Args []string // argv after the name
	// Stdin, when not empty, is the child's standard input.
	Stdin string
	// Env entries (KEY=value) are appended to the process environment.
	Env []string
	// WaitDelay, when positive, is exec.Cmd.WaitDelay: how long after the
	// context ends (or the child exits) the runner waits for the child's
	// output pipes to close before it closes them itself, so a grandchild
	// that inherited stdout cannot hold the call open forever.
	WaitDelay time.Duration
}

// Result is what a Call did.
type Result struct {
	Stdout, Stderr string
	// Code is the exit code; -1 when the child was ended by a signal or the
	// context.
	Code int
	// Signaled is true when the child was ended by a signal (or the context
	// ended).
	Signaled bool
	// StartErr is non-nil when the child could not be started at all.
	StartErr error
}

// Runner runs a Call. The production runner is ExecRunner; tests hand in a
// double.
type Runner func(ctx context.Context, c Call) Result

// ExecRunner runs the call with os/exec.
func ExecRunner(ctx context.Context, c Call) Result {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.WaitDelay = c.WaitDelay
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	if c.Stdin != "" {
		cmd.Stdin = bytes.NewReader([]byte(c.Stdin))
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := Result{Stdout: out.String(), Stderr: errb.String()}
	if err == nil {
		return res
	}
	// The child itself exited 0 but a grandchild kept an output pipe open
	// past WaitDelay: what was captured is all there is, and the exit code
	// is the child's own, 0.
	if errors.Is(err, exec.ErrWaitDelay) {
		return res
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.Code, res.Signaled = -1, true
			return res
		}
		if ctx.Err() != nil {
			res.Code, res.Signaled = -1, true
			return res
		}
		res.Code = ee.ExitCode()
		return res
	}
	res.Code = -1
	res.StartErr = err
	return res
}

// do executes c through r, falling back to ExecRunner when r is nil.
func (r Runner) do(ctx context.Context, c Call) Result {
	if r == nil {
		r = ExecRunner
	}
	return r(ctx, c)
}

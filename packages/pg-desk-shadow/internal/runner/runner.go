// Package runner starts child processes the only way the collector may: under
// the sandbox, with the allowlisted environment re-verified first.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/safety"
)

// Runner runs sandboxed children.
type Runner struct {
	Policy      safety.Policy
	Env         []string
	SandboxExec string
	WorkDir     string
	// Unsandboxed disables the sandbox wrapper (tests only; the collector
	// never sets it).
	Unsandboxed bool
}

// Result is one child's outcome.
type Result struct {
	Stdout, Stderr string
	Exit           int
	// Denied is true when the child's output shows a sandbox denial.
	Denied bool
	Dur    time.Duration
}

// ErrSafety wraps a refusal raised before the child started.
var ErrSafety = errors.New("safety refusal")

// Run executes argv. A non-zero exit is a Result, not an error; the error is
// non-nil only when the child could not be started, the deadline passed, or a
// safety check refused.
func (r *Runner) Run(ctx context.Context, timeout time.Duration, argv ...string) (Result, error) {
	if err := safety.Verify(r.Env, r.Policy); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrSafety, err)
	}
	full := argv
	if !r.Unsandboxed {
		full = safety.Wrap(r.SandboxExec, r.Policy.Scratch, argv)
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	cmd.Env = r.Env
	cmd.Dir = r.WorkDir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.WaitDelay = 5 * time.Second
	start := time.Now()
	err := cmd.Run()
	res := Result{Stdout: out.String(), Stderr: errb.String(), Dur: time.Since(start)}
	res.Denied = safety.DenialInOutput(res.Stderr)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ctx.Err() == nil {
			res.Exit = ee.ExitCode()
			return res, nil
		}
		if ctx.Err() != nil {
			return res, fmt.Errorf("deadline: %w", ctx.Err())
		}
		return res, err
	}
	return res, nil
}

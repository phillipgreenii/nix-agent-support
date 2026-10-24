package backfill

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Call is one subprocess the driver runs.
type Call struct {
	Name string   // resolved on PATH, or an absolute path
	Args []string // argv after the name
	// Env entries (KEY=value) are appended to the process environment.
	Env []string
	Dir string
}

// Result is what a Call did.
type Result struct {
	Stdout, Stderr string
	// Code is the exit code; -1 when the child was ended by a signal or the
	// context.
	Code int
	// StartErr is non-nil when the child could not be started at all.
	StartErr error
}

// Runner runs a Call. Production uses ExecRunner; a test may hand in a double.
type Runner func(ctx context.Context, c Call) Result

// ExecRunner runs the call with os/exec.
func ExecRunner(ctx context.Context, c Call) Result {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Env = append(os.Environ(), c.Env...)
	cmd.Dir = c.Dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := Result{Stdout: out.String(), Stderr: errb.String()}
	if err == nil {
		return res
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.Code = -1
			return res
		}
		if ctx.Err() != nil {
			res.Code = -1
			return res
		}
		res.Code = ee.ExitCode()
		return res
	}
	res.Code = -1
	res.StartErr = err
	return res
}

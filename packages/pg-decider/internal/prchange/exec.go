package prchange

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Call is one subprocess the command runs.
type Call struct {
	Name string   // resolved on $PATH
	Args []string // argv after the name
	// Stdin, when not empty, is the child's standard input.
	Stdin string
	// Env entries (KEY=value) are appended to the process environment.
	Env []string
}

// Result is what a Call did.
type Result struct {
	Stdout, Stderr string
	// Code is the exit code; -1 when the child was ended by a signal or the
	// context.
	Code int
	// Signaled is true when the child was ended by a signal (or the context
	// ended): the router reads "signal: killed" as a transient failure.
	Signaled bool
	// StartErr is non-nil when the child could not be started at all.
	StartErr error
}

// Runner runs a Call. The production runner is ExecRunner; tests of the
// composition logic hand in a double.
type Runner func(ctx context.Context, c Call) Result

// ExecRunner runs the call with os/exec.
func ExecRunner(ctx context.Context, c Call) Result {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
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

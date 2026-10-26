package apply

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Call is one subprocess apply runs. The shape matches prchange.Call so the two
// can be merged later without touching a caller; apply defines its own copy
// rather than import prchange (which imports apply).
type Call struct {
	Name string   // resolved on $PATH
	Args []string // argv after the name
	// Stdin, when not empty, is the child's standard input.
	Stdin string
	// Env entries (KEY=value) are appended to the process environment.
	Env []string
}

// CallResult is what a Call did. (prchange calls it Result; apply already has a
// Result, the outcome of a whole run.)
type CallResult struct {
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

// Runner runs a Call. The production runner is ExecRunner; tests answer in
// process, so no test spawns a child (under -race every child costs a flat
// second at exit).
type Runner func(ctx context.Context, c Call) CallResult

// ExecRunner runs the call with os/exec.
func ExecRunner(ctx context.Context, c Call) CallResult {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	if c.Stdin != "" {
		cmd.Stdin = bytes.NewReader([]byte(c.Stdin))
	}
	return runCmd(ctx, cmd)
}

// runCmd runs a prepared command and maps its outcome onto a CallResult. It
// captures stdout and stderr itself, so the caller leaves both unset.
func runCmd(ctx context.Context, cmd *exec.Cmd) CallResult {
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := CallResult{Stdout: out.String(), Stderr: errb.String()}
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

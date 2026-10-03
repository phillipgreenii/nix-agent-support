// Package adapters holds the imperative shell of pg-router-review-escalator:
// the subprocess Runner, the tracker that talks to `pg-connector issue ...`,
// and the notifier that execs the operator's push command. Each is a thin
// translation behind a port from package escalate, so tests drive them with a
// fake Runner and never start a process.
package adapters

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Result is one finished subprocess.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Runner starts a subprocess and waits for it. The error is non-nil only when
// the process could not be started or was killed by the timeout; a non-zero
// exit is reported in Result.ExitCode.
type Runner interface {
	Run(ctx context.Context, name string, args []string, stdin []byte, extraEnv []string) (Result, error)
}

// ExecRunner is the production Runner. Each call is bounded by Timeout.
type ExecRunner struct {
	Timeout time.Duration
}

// Run implements Runner. The child inherits this process's environment (the
// tracker targeting variables, for one, are the caller's to set) plus extraEnv.
func (r ExecRunner) Run(ctx context.Context, name string, args []string, stdin []byte, extraEnv []string) (Result, error) {
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return res, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && ctx.Err() == nil {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	if ctx.Err() != nil {
		return Result{}, fmt.Errorf("%s: %w", name, ctx.Err())
	}
	return Result{}, fmt.Errorf("start %s: %w", name, err)
}

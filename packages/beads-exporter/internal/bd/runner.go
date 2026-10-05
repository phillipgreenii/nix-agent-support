package bd

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// Cmd is one process invocation. Env is the COMPLETE child environment: the
// runner never merges in the ambient environment.
type Cmd struct {
	Path string
	Args []string
	Env  []string
}

// Result is the captured outcome of a finished invocation.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Runner executes a Cmd. A non-zero exit is NOT an error from Run: it is
// reported through Result.ExitCode so the adapter can classify it.
type Runner interface {
	Run(ctx context.Context, c Cmd) (Result, error)
}

// waitDelay bounds how long Run waits for output pipes to close after the
// process group has been killed.
const waitDelay = 2 * time.Second

// ExecRunner is the production Runner using os/exec. On context expiry the
// whole process group is killed, so a forked grandchild cannot outlive the
// timeout.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, c Cmd) (Result, error) {
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	cmd.Env = append([]string{}, c.Env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	configureProcessGroup(cmd)
	cmd.WaitDelay = waitDelay
	err := cmd.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return res, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return res, ctxErr
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}
	return res, err
}

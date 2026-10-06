// Package pgconn runs the pg-connector binary and nothing else.
//
// work-report never imports pg-connector's Go packages: it execs the
// pg-connector binary found on PATH and decodes its JSON output into its own
// small structs (see internal/pull). This package is the one place that
// touches os/exec for that purpose, behind the Runner seam so callers can be
// driven by a fake.
package pgconn

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
)

// Binary is the name looked up on PATH.
const Binary = "pg-connector"

// Runner runs pg-connector with args. err is non-nil only when the binary
// cannot be executed at all (absent from PATH, not executable, context done);
// a non-zero exit with output is data, returned with a nil err.
type Runner interface {
	Run(ctx context.Context, args ...string) (stdout []byte, exitCode int, err error)
}

// Output is everything one pg-connector invocation produced.
type Output struct {
	Stdout, Stderr []byte
	ExitCode       int
}

// DetailedRunner is a Runner that can also report stderr. It is optional:
// callers use it, when available, to put a stderr excerpt into the reason of
// a crashed run.
type DetailedRunner interface {
	Runner
	RunDetailed(ctx context.Context, args ...string) (Output, error)
}

type execRunner struct{}

// NewExec returns the Runner that execs "pg-connector" from PATH. It also
// implements DetailedRunner.
func NewExec() Runner { return execRunner{} }

func (e execRunner) Run(ctx context.Context, args ...string) ([]byte, int, error) {
	out, err := e.RunDetailed(ctx, args...)
	return out.Stdout, out.ExitCode, err
}

func (execRunner) RunDetailed(ctx context.Context, args ...string) (Output, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, Binary, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := Output{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return out, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return out, ctxErr
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		out.ExitCode = exitErr.ExitCode()
		return out, nil
	}
	return out, err
}

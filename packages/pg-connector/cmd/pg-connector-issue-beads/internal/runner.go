// runner.go: the bd-CLI exec seam. Re-homed from
// packages/pg-pr/pkg/beads/runner.go's Runner/CLIRunner (NewCLIRunner) —
// the concrete precedent this packet's bead cites for a bd-CLI-wrapper
// carry-over — with no behavior change: still shell out to `bd`, still
// return stdout plus a wrapped error (with a trimmed stderr tail) on
// failure. This backend's own client.go/backend.go build on top of it
// rather than importing packages/pg-pr, which packages/pg-connector's go.mod
// does not depend on at all [design: §5.2].
package internal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Runner shells out to `bd`. Production code uses CLIRunner; tests inject a
// fake runner that returns canned output without spawning a process —
// mirrors packages/pg-pr/pkg/beads.Runner's identical seam.
type Runner interface {
	// Run invokes `bd args...` and returns stdout. On failure, the returned
	// error wraps the underlying exec error and includes a trimmed stderr
	// tail so callers can surface useful messages. Unlike a typical Go exec
	// wrapper, Run's stdout return value is meaningful EVEN when err != nil:
	// bd's own `--json` flag writes a well-formed JSON error envelope to
	// stdout on many failure paths (not_found chief among them) while also
	// exiting non-zero, so a caller that only checked err would discard a
	// structured error body it could otherwise classify.
	Run(ctx context.Context, args ...string) (stdout string, err error)
}

// CLIRunner is the default Runner. It invokes the `bd` binary from PATH.
type CLIRunner struct {
	// Dir is the working directory bd runs in; bd resolves its workspace
	// relative to this. Optional — empty means inherit cwd (bd auto-discovers
	// its `.beads/` workspace from the process's current working directory,
	// exactly as it would for a human running `bd` interactively in this
	// workspace's own repo).
	Dir string
	// Env overrides the env block. If nil, the process env is used. Tests use
	// this to point a disposable per-test bd workspace without leaking
	// BEADS_DIR/WORKSPACE_ROOT from the outer environment.
	Env []string
}

// NewCLIRunner returns a CLIRunner using the process env and cwd. This
// backend's binding decision is to resolve bd's workspace the same way a
// human `bd` invocation would (cwd auto-discovery) rather than adding a
// backend-specific directory override — bd itself already owns that
// discovery logic.
func NewCLIRunner() *CLIRunner { return &CLIRunner{} }

// Run shells out to `bd`.
func (r *CLIRunner) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "bd", args...)
	if r.Dir != "" {
		cmd.Dir = r.Dir
	}
	if r.Env != nil {
		cmd.Env = r.Env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.String(), fmt.Errorf("bd %s: %w: %s",
				strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return stdout.String(), fmt.Errorf("bd %s: %w (is bd on PATH?)",
			strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}

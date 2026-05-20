package beads

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Runner shells out to `bd`. Production code uses CLIRunner; tests inject a
// fake runner that returns canned output without spawning a process.
type Runner interface {
	// Run invokes `bd args...` and returns stdout. On failure, the error
	// wraps the underlying exec error and includes a trimmed stderr tail
	// so callers can surface useful messages.
	Run(ctx context.Context, args ...string) (stdout string, err error)
}

// CLIRunner is the default Runner. It invokes the `bd` binary from PATH.
type CLIRunner struct {
	// Dir is the working directory bd runs in; bd resolves its workspace
	// relative to this. Optional — empty means inherit cwd.
	Dir string
	// Env overrides the env block. If nil, the process env is used.
	Env []string
}

// NewCLIRunner returns a CLIRunner using the process env and cwd.
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

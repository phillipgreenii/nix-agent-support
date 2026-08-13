// Package beads is pr-pool's local bd client. It copies pg-pr's Runner/CLIRunner
// pattern rather than importing pg-pr's heavy module. The CLIRunner's Dir/Env
// carry the env scrub (the bash's top-level `unset BEADS_DIR WORKSPACE_ROOT`),
// so pr-pool's own bd resolves the monorepo store from Dir, ignoring any ambient
// BEADS_DIR/WORKSPACE_ROOT inherited from a parent shell.
package beads

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Command is the bd binary this package invokes. It is exported so the pieces
// that must NAME the backing command without running it — the pre-runtime
// absent-backing-command validation of a bead-backed event source — share this
// one literal instead of duplicating it.
const Command = "bd"

// Runner shells out to `bd`. Production uses CLIRunner; tests inject a fake.
type Runner interface {
	Run(ctx context.Context, args ...string) (stdout string, err error)
}

// CLIRunner invokes the `bd` binary from PATH.
type CLIRunner struct {
	Dir string   // working dir bd resolves its workspace from ("" = inherit cwd)
	Env []string // env block ("" / nil = inherit process env)
}

// NewCLIRunnerForRepo returns a CLIRunner rooted at dir, with BEADS_DIR and
// WORKSPACE_ROOT scrubbed from the inherited environment.
func NewCLIRunnerForRepo(dir string) *CLIRunner {
	return &CLIRunner{Dir: dir, Env: scrubEnv(os.Environ())}
}

func scrubEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "BEADS_DIR=") || strings.HasPrefix(kv, "WORKSPACE_ROOT=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func (r *CLIRunner) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, Command, args...)
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

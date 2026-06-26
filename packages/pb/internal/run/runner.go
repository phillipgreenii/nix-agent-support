// Package run abstracts external-process execution behind a Runner interface so
// pb's logic is unit-testable with a FakeRunner (no real pn/bd/git). Mirrors
// pg-pr's beads.Runner and repo-base's exec.FakeRunner, generalised to name the
// binary per call.
package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Options controls one invocation.
type Options struct {
	Dir   string   // working directory (empty = inherit)
	Env   []string // full env (nil = inherit os.Environ())
	Stdin string   // stdin contents (empty = none)
}

// Result is the captured outcome of one invocation.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Runner runs `name args...`.
type Runner interface {
	Run(ctx context.Context, name string, args []string, opts Options) (Result, error)
}

// CLIRunner is the production Runner using os/exec.
type CLIRunner struct{}

// Run executes name with args. A non-zero exit returns a non-nil error whose
// message includes a trimmed stderr tail; Result is still populated (ExitCode set).
func (CLIRunner) Run(ctx context.Context, name string, args []string, opts Options) (Result, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	if opts.Env != nil {
		cmd.Env = opts.Env
	}
	if opts.Stdin != "" {
		cmd.Stdin = strings.NewReader(opts.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
			return res, fmt.Errorf("%s %s: exit %d: %s",
				name, strings.Join(args, " "), res.ExitCode, strings.TrimSpace(res.Stderr))
		}
		return res, fmt.Errorf("%s %s: %w (is %s on PATH?)", name, strings.Join(args, " "), err, name)
	}
	return res, nil
}

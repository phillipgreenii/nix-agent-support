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
	"syscall"
	"time"
)

// Options controls one invocation.
type Options struct {
	Dir   string   // working directory (empty = inherit)
	Env   []string // full env (nil = inherit os.Environ())
	Stdin string   // stdin contents (empty = none)
	// Timeout bounds the invocation (0 = unbounded, the historical behavior).
	// When it is set the child runs in its OWN process group and, on expiry (or
	// ctx cancellation), the WHOLE group is killed — a hung `git worktree add`
	// leaves `git reset`/fsmonitor children that killing only the direct child
	// would orphan, still holding the output pipes. Expiry returns an error
	// satisfying errors.Is(err, ErrTimeout).
	Timeout time.Duration
}

// ErrTimeout is wrapped by the error CLIRunner returns when Options.Timeout
// expires before the command finishes.
var ErrTimeout = errors.New("command timed out")

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

// waitDelay caps how long Wait lingers on output pipes after the process group
// is killed (a straggler outside the group may still hold them).
const waitDelay = 2 * time.Second

// Run executes name with args. A non-zero exit returns a non-nil error whose
// message includes the failure detail (stderr and stdout error text, see Detail); Result is still populated (ExitCode set).
func (CLIRunner) Run(ctx context.Context, name string, args []string, opts Options) (Result, error) {
	parent := ctx
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	if opts.Timeout > 0 {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return nil
			}
			// Negative pid = the whole process group (never a pattern kill).
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
				return cmd.Process.Kill()
			}
			return nil
		}
		cmd.WaitDelay = waitDelay
	}
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
		if opts.Timeout > 0 && parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return res, fmt.Errorf("%s %s: %w after %s (process group killed)",
				name, strings.Join(args, " "), ErrTimeout, opts.Timeout)
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
			return res, fmt.Errorf("%s %s: exit %d: %s",
				name, strings.Join(args, " "), res.ExitCode, Detail(name, res))
		}
		return res, fmt.Errorf("%s %s: %w (is %s on PATH?)", name, strings.Join(args, " "), err, name)
	}
	return res, nil
}

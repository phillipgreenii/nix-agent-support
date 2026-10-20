// Package runner starts child processes the only way the collector may: under
// the sandbox, with the allowlisted environment re-verified first.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/safety"
)

// Runner runs sandboxed children.
type Runner struct {
	Policy      safety.Policy
	Env         []string
	SandboxExec string
	WorkDir     string
	// Unsandboxed disables the sandbox wrapper (tests only; the collector
	// never sets it).
	Unsandboxed bool
	// ChildOut receives the stdout and stderr of a child started with Start
	// (nil discards them). Pass an *os.File so the child writes the file
	// directly.
	ChildOut io.Writer
}

// Result is one child's outcome.
type Result struct {
	Stdout, Stderr string
	Exit           int
	// Denied is true when the child's output shows a sandbox denial.
	Denied bool
	Dur    time.Duration
}

// ErrSafety wraps a refusal raised before the child started.
var ErrSafety = errors.New("safety refusal")

// Run executes argv. A non-zero exit is a Result, not an error; the error is
// non-nil only when the child could not be started, the deadline passed, or a
// safety check refused.
func (r *Runner) Run(ctx context.Context, timeout time.Duration, argv ...string) (Result, error) {
	return r.RunStdin(ctx, timeout, "", nil, argv...)
}

// RunStdin is Run with stdin fed to the child and extraEnv appended to the
// child environment AFTER the verification of the base environment (tests use
// it for stand-in variables; the real run passes nil).
func (r *Runner) RunStdin(ctx context.Context, timeout time.Duration, stdin string, extraEnv []string, argv ...string) (Result, error) {
	full, err := r.prepare(argv)
	if err != nil {
		return Result{}, err
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	cmd.Env = append(append([]string(nil), r.Env...), extraEnv...)
	cmd.Dir = r.WorkDir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.WaitDelay = 5 * time.Second
	start := time.Now()
	err = cmd.Run()
	res := Result{Stdout: out.String(), Stderr: errb.String(), Dur: time.Since(start)}
	res.Denied = safety.DenialInOutput(res.Stderr)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ctx.Err() == nil {
			res.Exit = ee.ExitCode()
			return res, nil
		}
		if ctx.Err() != nil {
			return res, fmt.Errorf("deadline: %w", ctx.Err())
		}
		return res, err
	}
	return res, nil
}

// prepare verifies the base environment and returns the (sandbox-wrapped)
// argv: the one gate every child, short or long-lived, passes through.
func (r *Runner) prepare(argv []string) ([]string, error) {
	if len(argv) == 0 {
		return nil, errors.New("runner: no command")
	}
	if err := safety.Verify(r.Env, r.Policy); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSafety, err)
	}
	if r.Unsandboxed {
		return argv, nil
	}
	return safety.Wrap(r.SandboxExec, r.Policy.Scratch, argv), nil
}

// Child is a long-lived child started with Start.
type Child struct {
	cmd  *exec.Cmd
	done chan struct{}

	mu      sync.Mutex
	exit    int
	waitErr error
}

// Start launches argv as a long-lived child under the same sandbox and the
// same verified environment as Run, in its own process group, with its output
// going to r.ChildOut. The child's life is bounded by Stop, not by ctx (ctx is
// only checked before the launch): the owner MUST call Stop.
func (r *Runner) Start(ctx context.Context, argv ...string) (*Child, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	full, err := r.prepare(argv)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(full[0], full[1:]...)
	cmd.Env = r.Env
	cmd.Dir = r.WorkDir
	cmd.Stdout, cmd.Stderr = r.ChildOut, r.ChildOut
	// Its own process group: Stop signals the whole group, so a wrapper's
	// own children cannot be orphaned.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &Child{cmd: cmd, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		c.mu.Lock()
		c.waitErr = err
		c.exit = cmd.ProcessState.ExitCode()
		c.mu.Unlock()
		close(c.done)
	}()
	return c, nil
}

// Done is closed once the child process has exited.
func (c *Child) Done() <-chan struct{} { return c.done }

// Pid is the child's process id.
func (c *Child) Pid() int { return c.cmd.Process.Pid }

// ExitCode is the child's exit code (-1 when a signal ended it); it is only
// meaningful after Done is closed.
func (c *Child) ExitCode() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exit
}

// Stop asks the child to end with SIGTERM and waits up to grace; a child still
// running then is killed (SIGKILL to its process group) and Stop returns an
// error saying so. Stopping an already-exited child, or calling Stop twice,
// returns nil.
func (c *Child) Stop(grace time.Duration) error {
	select {
	case <-c.done:
		return nil
	default:
	}
	pgid := c.cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-c.done:
		c.reap()
		return nil
	case <-t.C:
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	select {
	case <-c.done:
	case <-time.After(10 * time.Second):
		return fmt.Errorf("pid %d did not exit after SIGKILL", pgid)
	}
	return fmt.Errorf("pid %d did not exit within %s of SIGTERM and was killed", pgid, grace)
}

// reap signals whatever is left of the child's process group right after the
// leader exited on SIGTERM (a wrapper's own children outliving it); a group
// with no members is the normal case, and the pgid cannot be recycled yet.
func (c *Child) reap() { _ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL) }

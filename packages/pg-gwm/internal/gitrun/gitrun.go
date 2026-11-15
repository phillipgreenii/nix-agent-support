// Package gitrun runs git as a bounded child process: every call carries a
// timeout, a SIGTERM-then-SIGKILL cancel, and an environment stripped of any
// inherited variable that would redirect git to another repository.
package gitrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
)

// termGrace is how long a cancelled git gets after SIGTERM before SIGKILL.
const termGrace = 30 * time.Second

// ErrTimedOut reports that a git call outlived its timeout.
var ErrTimedOut = errors.New("git timed out")

// ErrStaleLock reports a git failure caused by an existing lock file.
var ErrStaleLock = errors.New("git lock file exists")

// Git runs git through Run. The zero Run is cmdrun.Exec and the zero Bin is
// "git".
type Git struct {
	Run      cmdrun.Runner
	Bin      string
	ExtraEnv []string
}

// GitError is a git invocation that exited non-zero (or did not start).
type GitError struct {
	Args   []string
	Code   int
	Stderr string
}

func (e *GitError) Error() string {
	return fmt.Sprintf("git %s: exit %d: %s", strings.Join(e.Args, " "), e.Code, strings.TrimSpace(e.Stderr))
}

// Exec runs git -C dir args... under timeout and returns its stdout. A
// timeout returns ErrTimedOut; a failure whose stderr contains
// ".lock': File exists" returns an error matching ErrStaleLock (and
// *GitError); any other failure returns *GitError.
func (g Git) Exec(ctx context.Context, dir string, timeout time.Duration, args ...string) (string, error) {
	run := g.Run
	if run == nil {
		run = cmdrun.Exec
	}
	bin := g.Bin
	if bin == "" {
		bin = "git"
	}
	full := append([]string{"-C", dir}, args...)
	res := run(ctx, cmdrun.Call{
		Name:         bin,
		Args:         full,
		OwnProcGroup: true,
		TermGrace:    termGrace,
		Timeout:      timeout,
		Env:          g.env(),
		EnvMode:      cmdrun.EnvExact,
	})
	if res.TimedOut {
		return "", fmt.Errorf("git %s: %w after %s", strings.Join(args, " "), ErrTimedOut, timeout)
	}
	if res.OK() {
		return string(res.Stdout), nil
	}
	gerr := &GitError{Args: args, Code: res.Code, Stderr: string(res.Stderr)}
	switch {
	case res.StartErr != nil:
		gerr.Stderr = res.StartErr.Error()
	case res.CtxErr != nil:
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), res.CtxErr)
	}
	if strings.Contains(gerr.Stderr, ".lock': File exists") {
		return "", &lockError{gerr}
	}
	return "", gerr
}

// lockError is a *GitError that also matches ErrStaleLock.
type lockError struct{ *GitError }

func (e *lockError) Unwrap() []error { return []error{ErrStaleLock, e.GitError} }

func (g Git) env() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	return append(env, g.ExtraEnv...)
}

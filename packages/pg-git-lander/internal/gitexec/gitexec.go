// Package gitexec is the one layer every git command of pg-git-lander goes
// through: pinned options, the scrubbed environment, its own process group
// and the fixed timeout.
package gitexec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/x/cmdrun"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
)

// Timeout is the fixed per-command timeout in production.
const Timeout = 10 * time.Minute

// PinnedOptions are carried by every git command, whatever the user's
// configuration says.
var PinnedOptions = []string{
	"-c", "rerere.enabled=false", "-c", "rerere.autoUpdate=false",
	"-c", "rebase.backend=merge", "-c", "rebase.autoStash=false",
	"-c", "rebase.updateRefs=false", "-c", "rebase.autoSquash=false",
	"-c", "core.editor=true", "-c", "sequence.editor=true",
	"-c", "gc.auto=0", "-c", "maintenance.auto=false",
}

// Git runs git commands.
type Git struct {
	Runner seams.Runner
	Env    seams.GitEnv
	// Timeout overrides the production Timeout when positive (tests only).
	Timeout time.Duration
}

// New returns a Git using the production Runner and environment.
func New() *Git {
	return &Git{Runner: seams.ProductionRunner(), Env: seams.ProductionGitEnv()}
}

// Result is one finished git command.
type Result struct {
	cmdrun.Result
	// TimedOut is true when the timeout ended the command and its process
	// group was killed.
	TimedOut bool
}

// Run runs git with args in dir.
func (g *Git) Run(ctx context.Context, dir string, args ...string) Result {
	d := g.Timeout
	if d <= 0 {
		d = Timeout
	}
	cctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()

	full := append(append([]string{}, PinnedOptions...), args...)
	res := g.Runner(cctx, cmdrun.Call{
		Name:         "git",
		Args:         full,
		Dir:          dir,
		Env:          g.Env.Environ(),
		EnvMode:      cmdrun.EnvExact,
		OwnProcGroup: true,
	})
	timedOut := ctx.Err() == nil && errors.Is(res.CtxErr, context.DeadlineExceeded)
	return Result{Result: res, TimedOut: timedOut}
}

// ErrTimeout is returned by the helpers when a command timed out.
var ErrTimeout = errors.New("git command timed out")

func (g *Git) must(ctx context.Context, dir string, args ...string) (string, error) {
	r := g.Run(ctx, dir, args...)
	switch {
	case r.TimedOut:
		return "", fmt.Errorf("git %s: %w", args[0], ErrTimeout)
	case !r.OK():
		return "", fmt.Errorf("git %s: exit %d: %s%v", args[0], r.Code, strings.TrimSpace(string(r.Stderr)), r.StartErr)
	}
	return string(r.Stdout), nil
}

// CommonDir returns the absolute git common dir of the repository at dir.
func (g *Git) CommonDir(ctx context.Context, dir string) (string, error) {
	out, err := g.must(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	return strings.TrimSpace(out), err
}

// Worktree is one entry of git worktree list --porcelain.
type Worktree struct {
	Path     string
	Head     string
	Branch   string // full ref, empty when detached
	Detached bool
	Bare     bool
	Locked   bool
	Prunable bool
}

// Worktrees lists the worktrees of the repository at dir.
func (g *Git) Worktrees(ctx context.Context, dir string) ([]Worktree, error) {
	out, err := g.must(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return ParseWorktrees(out), nil
}

// ParseWorktrees parses git worktree list --porcelain output.
func ParseWorktrees(out string) []Worktree {
	var wts []Worktree
	var cur *Worktree
	for _, line := range strings.Split(out, "\n") {
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			wts = append(wts, Worktree{Path: val})
			cur = &wts[len(wts)-1]
		case "HEAD":
			if cur != nil {
				cur.Head = val
			}
		case "branch":
			if cur != nil {
				cur.Branch = val
			}
		case "detached":
			if cur != nil {
				cur.Detached = true
			}
		case "bare":
			if cur != nil {
				cur.Bare = true
			}
		case "locked":
			if cur != nil {
				cur.Locked = true
			}
		case "prunable":
			if cur != nil {
				cur.Prunable = true
			}
		}
	}
	return wts
}

// RunInput is Run with stdin.
func (g *Git) RunInput(ctx context.Context, dir string, stdin []byte, args ...string) Result {
	d := g.Timeout
	if d <= 0 {
		d = Timeout
	}
	cctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()

	full := append(append([]string{}, PinnedOptions...), args...)
	res := g.Runner(cctx, cmdrun.Call{
		Name:         "git",
		Args:         full,
		Dir:          dir,
		Stdin:        stdin,
		Env:          g.Env.Environ(),
		EnvMode:      cmdrun.EnvExact,
		OwnProcGroup: true,
	})
	timedOut := ctx.Err() == nil && errors.Is(res.CtxErr, context.DeadlineExceeded)
	return Result{Result: res, TimedOut: timedOut}
}

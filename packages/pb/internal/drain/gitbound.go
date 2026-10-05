package drain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/pb/internal/run"
)

// DefaultGitTimeout bounds each git call Isolate makes. A healthy `git
// worktree add` finishes in seconds even on a large checkout; the stall this
// guards against (pg2-luvwe) was 72 MINUTES with no output.
const DefaultGitTimeout = 5 * time.Minute

// maxCleanupTimeout caps the per-call bound on the post-timeout cleanup, so a
// cleanup that itself hits the contention cannot add many minutes more.
const maxCleanupTimeout = time.Minute

// ErrGitTimeout: a git call made by Isolate did not finish within the bound
// and its whole process group was killed. Wrapped (errors.Is) by the returned
// error; the message names the likely cause. CLI exit 1.
var ErrGitTimeout = errors.New("git timed out")

// fsmonitorOffEnv returns base (nil = os.Environ()) extended with the per-call
// environment that turns core.fsmonitor off for the child git ONLY:
// GIT_CONFIG_COUNT/KEY_n/VALUE_n sit at "command" scope, above every config
// file, and are never persisted — the operator ruling is that fsmonitor stays
// ON in the repos' config and hangs are mitigated tool-side. An existing
// GIT_CONFIG_COUNT is honoured: the new pair is appended after it.
func fsmonitorOffEnv(base []string) []string {
	if base == nil {
		base = os.Environ()
	}
	n := 0
	out := make([]string, 0, len(base)+3)
	for _, kv := range base {
		if v, ok := strings.CutPrefix(kv, "GIT_CONFIG_COUNT="); ok {
			if c, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && c > 0 {
				n = c
			}
			continue
		}
		out = append(out, kv)
	}
	return append(
		out,
		"GIT_CONFIG_COUNT="+strconv.Itoa(n+1),
		fmt.Sprintf("GIT_CONFIG_KEY_%d=core.fsmonitor", n),
		fmt.Sprintf("GIT_CONFIG_VALUE_%d=false", n),
	)
}

// gitBound decorates a Runner so every `git` call gets the fsmonitor-off
// environment and a timeout (which also puts the call in its own process
// group, killed whole on expiry). Non-git commands (pg-hooks) pass through
// untouched. A timeout is rewrapped as ErrGitTimeout with the diagnosis.
type gitBound struct {
	inner   run.Runner
	timeout time.Duration
}

func (g gitBound) Run(ctx context.Context, name string, args []string, opts run.Options) (run.Result, error) {
	if name != "git" {
		return g.inner.Run(ctx, name, args, opts)
	}
	opts.Env = fsmonitorOffEnv(opts.Env)
	opts.Timeout = g.timeout
	res, err := g.inner.Run(ctx, name, args, opts)
	if errors.Is(err, run.ErrTimeout) {
		return res, fmt.Errorf("%w: git %s did not finish within %s and its process group was "+
			"killed — this is the signature of fsmonitor/fseventsd contention on the machine "+
			"(leaked `git fsmonitor--daemon` processes, fseventsd at high CPU, very high load); "+
			"check `pgrep -fl fsmonitor--daemon` and `uptime`: %w",
			ErrGitTimeout, strings.Join(args, " "), g.timeout, err)
	}
	return res, err
}

// cleanupCreated removes ONLY what this Isolate call itself created after its
// `git worktree add` timed out: the (half-created, possibly locked) worktree
// at wt and, when createdBranch is set, the branch the same call created with
// -b. It never touches any other worktree or branch. Best effort; returns a
// human-readable account of what was and was not cleaned.
func cleanupCreated(ctx context.Context, r run.Runner, timeout time.Duration, repo, wt, branch string, createdBranch bool) string {
	if timeout > maxCleanupTimeout {
		timeout = maxCleanupTimeout
	}
	g := gitBound{inner: r, timeout: timeout}
	git := func(args ...string) error {
		_, err := g.Run(ctx, "git", append([]string{"-C", repo}, args...), run.Options{})
		return err
	}
	var problems []string
	// -f -f: the killed `worktree add` leaves the entry locked ("initializing").
	if err := git("worktree", "remove", "--force", "--force", wt); err != nil {
		_ = git("worktree", "unlock", wt)
		if rmErr := os.RemoveAll(wt); rmErr != nil {
			problems = append(problems, fmt.Sprintf("could not remove %s: %v", wt, rmErr))
		}
		if err := git("worktree", "prune"); err != nil {
			problems = append(problems, fmt.Sprintf("worktree prune: %v", err))
		}
	}
	if _, statErr := os.Lstat(wt); statErr == nil {
		problems = append(problems, fmt.Sprintf("%s still exists", wt))
	}
	if createdBranch {
		if err := git("branch", "-D", branch); err != nil {
			problems = append(problems, fmt.Sprintf("could not delete branch %s: %v", branch, err))
		}
	}
	what := "worktree " + wt
	if createdBranch {
		what += " and branch " + branch
	}
	if len(problems) > 0 {
		return fmt.Sprintf("cleanup of %s INCOMPLETE (%s); remove it manually", what, strings.Join(problems, "; "))
	}
	return "removed the half-created " + what + " (only what this call created)"
}

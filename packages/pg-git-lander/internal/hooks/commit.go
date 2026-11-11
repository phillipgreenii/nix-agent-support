package hooks

import (
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
)

// CommitSpec describes a commit the tool makes on its own behalf (a conflict
// resolution or an autofix commit).
type CommitSpec struct {
	// Dir is the worktree to commit in.
	Dir string
	// Message is passed with -m; empty means none (use Args, e.g. -C <sha>).
	Message string
	// Args are extra git commit arguments (such as -C <sha>, --allow-empty).
	Args []string
	// StageTracked runs git add -u before the first try (autofix).
	StageTracked bool
}

// CommitResult is the result of Commit.
type CommitResult struct {
	// Tries is how many commit attempts were made.
	Tries int
	// Stop is outcome.StopCommitRejected when every try was rejected.
	Stop *outcome.StopReason
	// LogTail is the last output of the final rejected try, without hint lines.
	LogTail string
	// Err is an unexpected failure of git itself (not a hook rejection).
	Err error
}

// Commit makes a commit. When a git commit hook rejects it and leaves tracked
// changes behind (a formatter), the changes are staged again and the same
// commit is retried, up to AutofixAttempts tries in all, then the result is
// commit-rejected. A rejection that leaves nothing behind stops at once.
// run_git_hooks=false passes --no-verify.
func (r *Runner) Commit(ctx context.Context, s CommitSpec) CommitResult {
	attempts := r.AutofixAttempts
	if attempts < 1 {
		attempts = 1
	}
	if s.StageTracked {
		if g := r.Git.Run(ctx, s.Dir, "add", "-u"); !g.OK() {
			return CommitResult{Err: gitErr("add -u", g)}
		}
	}
	args := []string{"commit"}
	if !r.RunGitHooks {
		args = append(args, "--no-verify")
	}
	if s.Message != "" {
		args = append(args, "-m", s.Message)
	}
	args = append(args, s.Args...)

	var cr CommitResult
	for cr.Tries < attempts {
		cr.Tries++
		g := r.Git.Run(ctx, s.Dir, args...)
		if g.TimedOut {
			cr.Err = gitErr("commit", g)
			return cr
		}
		if g.OK() {
			return cr
		}
		if ctx.Err() != nil {
			cr.Err = ctx.Err()
			return cr
		}
		cr.LogTail = LogTail(string(g.Stdout), string(g.Stderr))
		changed, _, err := r.status(ctx, s.Dir, true)
		if err != nil {
			cr.Err = err
			return cr
		}
		if !changed {
			break
		}
		if a := r.Git.Run(ctx, s.Dir, "add", "-u"); !a.OK() {
			cr.Err = gitErr("add -u", a)
			return cr
		}
	}
	st := outcome.StopCommitRejected
	cr.Stop = &st
	return cr
}

func gitErr(what string, g gitexec.Result) error {
	return fmt.Errorf("git %s: exit %d: %s%v", what, g.Code, strings.TrimSpace(string(g.Stderr)), g.StartErr)
}

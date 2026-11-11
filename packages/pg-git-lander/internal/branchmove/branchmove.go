// Package branchmove moves a kept BRANCH to the result: always for a
// rebase, and for a land with --keep-source-branch (design sections 11.3 and
// 8.8). A BRANCH checked out nowhere is moved by a compare-and-swap
// git update-ref in a transaction that also verifies the lock records; a
// BRANCH checked out in a worktree other than the work worktree is moved by
// git reset --keep run alone in that worktree, never by update-ref first.
package branchmove

import (
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/landmove"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/worktrees"
)

// Status is the kind of a Result.
type Status int

// Statuses.
const (
	// Moved: BRANCH equals the result.
	Moved Status = iota
	// NotMoved: a land left BRANCH at its old tip; the outcome stays landed
	// and carries the warning branch-not-moved.
	NotMoved
	// Stopped: Stop and Detail say why (rebase only, plus lock-lost and
	// git-error for both).
	Stopped
)

// Result is the outcome of Move.
type Result struct {
	Status Status
	// Warning is branch-not-moved when Status is NotMoved.
	Warning outcome.Warning
	Stop    outcome.StopReason
	Detail  string
}

// Input is everything Move needs.
type Input struct {
	// Operation is worktrees.Land or worktrees.Rebase.
	Operation worktrees.Operation
	Branch    string
	// TipsBranch is BRANCH's tip the operation started from (tips.branch).
	TipsBranch string
	// Result is the work branch's tip.
	Result string
	// Dir is any worktree of the repository, to run git in.
	Dir string
	// Holder is the worktree other than the work worktree that has BRANCH
	// checked out, or nil when BRANCH is checked out nowhere.
	Holder *worktrees.Chosen
	// Switch is the work worktree to switch to BRANCH after the move when
	// BRANCH was checked out nowhere. Nil when the source worktree was
	// already removed: the update-ref alone moves BRANCH.
	Switch *worktrees.Chosen
	Fence  landmove.Fence
}

// Mover performs the move.
type Mover struct {
	Git *gitexec.Git
	// BeforeWrite, when non-nil, runs right before the git command that
	// writes BRANCH (tests use it to play a second writer).
	BeforeWrite func()
}

func branchRef(b string) string { return "refs/heads/" + b }

// fail is the result of a move that did not happen: the warning for a land,
// the stop with reason for a rebase.
func fail(in Input, rebaseReason outcome.StopReason, format string, a ...any) Result {
	d := fmt.Sprintf(format, a...)
	if in.Operation == worktrees.Rebase {
		return Result{Status: Stopped, Stop: rebaseReason, Detail: d}
	}
	return Result{Status: NotMoved, Warning: outcome.WarnBranchNotMoved, Detail: d}
}

func stop(reason outcome.StopReason, format string, a ...any) Result {
	return Result{Status: Stopped, Stop: reason, Detail: fmt.Sprintf(format, a...)}
}

func (m *Mover) tip(ctx context.Context, dir, branch string) (string, error) {
	r := m.Git.Run(ctx, dir, "rev-parse", "--verify", "--quiet", branchRef(branch)+"^{commit}")
	if r.TimedOut {
		return "", gitexec.ErrTimeout
	}
	if !r.OK() {
		return "", fmt.Errorf("cannot read %s: exit %d %s", branchRef(branch), r.Code, strings.TrimSpace(string(r.Stderr)))
	}
	return strings.TrimSpace(string(r.Stdout)), nil
}

func (m *Mover) transaction(ctx context.Context, dir string, locks []landmove.LockCheck, upd *[3]string) gitexec.Result {
	var b strings.Builder
	b.WriteString("start\n")
	for _, l := range locks {
		fmt.Fprintf(&b, "verify %s %s\n", l.Ref, l.Oid)
	}
	if upd != nil {
		fmt.Fprintf(&b, "update %s %s %s\n", upd[0], upd[1], upd[2])
	}
	b.WriteString("prepare\ncommit\n")
	return m.Git.RunInput(ctx, dir, []byte(b.String()), "update-ref", "--stdin")
}

func (m *Mover) locksChanged(ctx context.Context, dir string, locks []landmove.LockCheck) bool {
	if len(locks) == 0 {
		return false
	}
	return !m.transaction(ctx, dir, locks, nil).OK()
}

func renewalOK(in Input) bool {
	return in.Fence.RenewalFresh == nil || in.Fence.RenewalFresh()
}

// Move moves BRANCH to in.Result.
func (m *Mover) Move(ctx context.Context, in Input) Result {
	dir := in.Dir
	if dir == "" {
		return stop(outcome.StopGitError, "no worktree to run git in")
	}
	cur, err := m.tip(ctx, dir, in.Branch)
	if err != nil {
		return stop(outcome.StopGitError, "%v", err)
	}
	if cur == in.Result && cur == in.TipsBranch {
		return m.finish(ctx, in)
	}
	if cur != in.TipsBranch {
		return fail(in, outcome.StopBranchMoved, "%s moved from %s to %s", in.Branch, in.TipsBranch, cur)
	}
	if !renewalOK(in) {
		return stop(outcome.StopLockLost, "the lock renewal is older than two thirds of the lease")
	}
	if in.Holder != nil {
		return m.moveInHolder(ctx, in)
	}
	return m.moveByUpdateRef(ctx, in)
}

// finish handles BRANCH already equal to the result (nothing to write): the
// work worktree is still switched to it when asked.
func (m *Mover) finish(ctx context.Context, in Input) Result {
	return m.switchWork(ctx, in)
}

func (m *Mover) switchWork(ctx context.Context, in Input) Result {
	if in.Switch != nil && in.Holder == nil {
		r := m.Git.Run(ctx, in.Switch.Path, "switch", in.Branch)
		if r.TimedOut {
			return stop(outcome.StopGitError, "git switch: %v", gitexec.ErrTimeout)
		}
		if !r.OK() {
			return stop(outcome.StopCheckoutChanged, "switching %s to %s after the move: %s", in.Switch.Path, in.Branch, strings.TrimSpace(string(r.Stderr)))
		}
	}
	return Result{Status: Moved}
}

func (m *Mover) moveByUpdateRef(ctx context.Context, in Input) Result {
	if m.BeforeWrite != nil {
		m.BeforeWrite()
	}
	tr := m.transaction(ctx, in.Dir, in.Fence.Locks, &[3]string{branchRef(in.Branch), in.Result, in.TipsBranch})
	if !tr.OK() {
		if tr.TimedOut {
			return stop(outcome.StopGitError, "git update-ref: %v", gitexec.ErrTimeout)
		}
		if cur, err := m.tip(ctx, in.Dir, in.Branch); err == nil && cur != in.TipsBranch {
			return fail(in, outcome.StopBranchMoved, "%s moved from %s to %s", in.Branch, in.TipsBranch, cur)
		}
		if m.locksChanged(ctx, in.Dir, in.Fence.Locks) {
			return stop(outcome.StopLockLost, "a lock record changed")
		}
		return fail(in, outcome.StopCheckoutChanged, "git update-ref failed (exit %d): %s", tr.Code, strings.TrimSpace(string(tr.Stderr)))
	}
	return m.switchWork(ctx, in)
}

func (m *Mover) moveInHolder(ctx context.Context, in Input) Result {
	h := in.Holder
	if m.locksChanged(ctx, in.Dir, in.Fence.Locks) {
		return stop(outcome.StopLockLost, "a lock record changed")
	}
	s := m.Git.Run(ctx, h.Path, "status", "--porcelain", "--untracked-files=no")
	if s.TimedOut {
		return stop(outcome.StopGitError, "git status: %v", gitexec.ErrTimeout)
	}
	if !s.OK() {
		return fail(in, outcome.StopCheckoutChanged, "cannot read the status of %s: %s", h.Path, strings.TrimSpace(string(s.Stderr)))
	}
	if strings.TrimSpace(string(s.Stdout)) != "" {
		return fail(in, outcome.StopCheckoutChanged, "the worktree %s has tracked changes", h.Path)
	}
	if m.BeforeWrite != nil {
		m.BeforeWrite()
	}
	r := m.Git.Run(ctx, h.Path, "reset", "--keep", in.Result)
	if r.TimedOut {
		return stop(outcome.StopGitError, "git reset: %v", gitexec.ErrTimeout)
	}
	if !r.OK() {
		return fail(in, outcome.StopCheckoutChanged, "git reset --keep refused in %s: %s", h.Path, strings.TrimSpace(string(r.Stderr)))
	}
	return Result{Status: Moved}
}

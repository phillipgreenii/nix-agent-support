// Package landmove is the fenced move of a land: it fast-forwards TARGET to
// the result, either by git update-ref in a transaction that also verifies
// the holder's lock records (TARGET checked out nowhere), or through the
// destination worktree with git merge --ff-only (design section 8.8).
package landmove

import (
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/workbranch"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/worktrees"
)

// LockCheck is one lock ref and the record oid it must still hold.
type LockCheck struct {
	Ref string
	Oid string
}

// Fence is what the move verifies before it writes.
type Fence struct {
	// Locks are verified in the same git update-ref --stdin transaction as
	// the move (or in a verify-only transaction before a merge --ff-only).
	// Empty means nothing to verify.
	Locks []LockCheck
	// RenewalFresh reports whether the holder's last successful renewal is
	// less than two thirds of lease_ttl old (monotonic clock). Nil means
	// always fresh. It runs before any ref moves.
	RenewalFresh func() bool
}

// Status is the kind of a Result.
type Status int

// Statuses.
const (
	// Moved: TARGET equals the result.
	Moved Status = iota
	// TargetMoved is the race path: TARGET differs from tips.target. The
	// pipeline retries it up to max_race_retries.
	TargetMoved
	// Stopped: Stop and Detail say why.
	Stopped
)

// Result is the outcome of Move.
type Result struct {
	Status Status
	Stop   outcome.StopReason
	Detail string
}

// Input is everything Move needs.
type Input struct {
	OpID   string
	Target string
	// TipsTarget is TARGET's tip the work branch was built on (tips.target).
	TipsTarget string
	// Result is the work branch's tip.
	Result string
	// DW is the destination worktree; may be nil when TargetCheckedOut is
	// false (then the ancestry proof must hold). WW is the work worktree.
	DW, WW *worktrees.Chosen
	// TargetCheckedOut is the starting-state answer of the worktree
	// selection.
	TargetCheckedOut bool
	Fence            Fence
	// Intent is called with the expected_result before any ref moves, so the
	// caller can write the intent marker into the operation record. A
	// non-nil error stops the move with internal-error.
	Intent func(expectedResult string) error
}

// Mover performs the fenced move.
type Mover struct {
	Git      *gitexec.Git
	Brancher *workbranch.Brancher
	// BeforeWrite, when non-nil, runs right before the git command that
	// writes TARGET (tests use it to play a second writer).
	BeforeWrite func()
}

func targetRef(t string) string { return "refs/heads/" + t }

func stop(reason outcome.StopReason, format string, a ...any) Result {
	return Result{Status: Stopped, Stop: reason, Detail: fmt.Sprintf(format, a...)}
}

func (m *Mover) tip(ctx context.Context, dir, target string) (string, error) {
	r := m.Git.Run(ctx, dir, "rev-parse", "--verify", "--quiet", targetRef(target)+"^{commit}")
	if r.TimedOut {
		return "", gitexec.ErrTimeout
	}
	if !r.OK() {
		return "", fmt.Errorf("cannot read %s: exit %d %s", targetRef(target), r.Code, strings.TrimSpace(string(r.Stderr)))
	}
	return strings.TrimSpace(string(r.Stdout)), nil
}

// transaction runs git update-ref --stdin with the verifies and, when move
// is non-nil, the update of TARGET from old to result.
func (m *Mover) transaction(ctx context.Context, dir string, locks []LockCheck, upd *[3]string) gitexec.Result {
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

// Move fast-forwards TARGET to in.Result.
func (m *Mover) Move(ctx context.Context, in Input) Result {
	if in.Intent != nil {
		if err := in.Intent(in.Result); err != nil {
			return stop(outcome.StopInternalError, "recording the move intent: %v", err)
		}
	}
	dir := m.anyDir(in)
	if dir == "" {
		return stop(outcome.StopGitError, "no worktree to run git in")
	}
	if !in.TargetCheckedOut {
		if res, done := m.moveByUpdateRef(ctx, dir, in); done {
			return res
		}
	}
	return m.moveThroughDW(ctx, dir, in)
}

func (m *Mover) anyDir(in Input) string {
	switch {
	case in.DW != nil:
		return in.DW.Path
	case in.WW != nil:
		return in.WW.Path
	}
	return ""
}

func (m *Mover) renewalOK(in Input) bool {
	return in.Fence.RenewalFresh == nil || in.Fence.RenewalFresh()
}

// moveByUpdateRef is the checked-out-nowhere path. done is false when the
// proof cannot be made and the move must go through the DW.
func (m *Mover) moveByUpdateRef(ctx context.Context, dir string, in Input) (Result, bool) {
	if !m.renewalOK(in) {
		return stop(outcome.StopLockLost, "the lock renewal is older than two thirds of the lease"), true
	}
	old, err := m.tip(ctx, dir, in.Target)
	if err != nil {
		return stop(outcome.StopGitError, "%v", err), true
	}
	if old == in.Result {
		return Result{Status: Moved}, true
	}
	r := m.Git.Run(ctx, dir, "merge-base", "--is-ancestor", old, in.Result)
	if r.TimedOut {
		return stop(outcome.StopGitError, "merge-base: %v", gitexec.ErrTimeout), true
	}
	if !r.OK() {
		// Not provable (or git failed): the DW path decides, and a result
		// that does not descend from TARGET is the race path there.
		return Result{}, false
	}
	if m.BeforeWrite != nil {
		m.BeforeWrite()
	}
	tr := m.transaction(ctx, dir, in.Fence.Locks, &[3]string{targetRef(in.Target), in.Result, old})
	if tr.OK() {
		return m.confirm(ctx, dir, in), true
	}
	return m.classifyFailure(ctx, dir, in, "update-ref", tr, len(in.Fence.Locks) > 0), true
}

// classifyFailure decides what a failed write was: a changed lock record
// (lock-lost), TARGET having moved (race), or a git error.
func (m *Mover) classifyFailure(ctx context.Context, dir string, in Input, what string, tr gitexec.Result, hadLocks bool) Result {
	if tr.TimedOut {
		return stop(outcome.StopGitError, "git %s: %v", what, gitexec.ErrTimeout)
	}
	cur, err := m.tip(ctx, dir, in.Target)
	if err == nil && cur != in.TipsTarget {
		return Result{Status: TargetMoved, Detail: fmt.Sprintf("%s moved from %s to %s", in.Target, in.TipsTarget, cur)}
	}
	if hadLocks && m.locksChanged(ctx, dir, in.Fence.Locks) {
		return stop(outcome.StopLockLost, "a lock record changed")
	}
	return stop(outcome.StopGitError, "git %s failed (exit %d): %s", what, tr.Code, strings.TrimSpace(string(tr.Stderr)))
}

func (m *Mover) locksChanged(ctx context.Context, dir string, locks []LockCheck) bool {
	return m.verifyLocks(ctx, dir, locks) != nil
}

func (m *Mover) verifyLocks(ctx context.Context, dir string, locks []LockCheck) error {
	if len(locks) == 0 {
		return nil
	}
	r := m.transaction(ctx, dir, locks, nil)
	if r.OK() {
		return nil
	}
	return fmt.Errorf("lock verification failed: %s", strings.TrimSpace(string(r.Stderr)))
}

// confirm checks that TARGET equals the result.
func (m *Mover) confirm(ctx context.Context, dir string, in Input) Result {
	cur, err := m.tip(ctx, dir, in.Target)
	if err != nil {
		return stop(outcome.StopMoveUnverified, "%v", err)
	}
	if cur != in.Result {
		return stop(outcome.StopMoveUnverified, "%s is %s after the move, not the result %s", in.Target, cur, in.Result)
	}
	return Result{Status: Moved}
}

// moveThroughDW is steps 1 to 5 of section 8.8.
func (m *Mover) moveThroughDW(ctx context.Context, dir string, in Input) Result {
	dw := in.DW
	if dw == nil {
		return stop(outcome.StopGitError, "TARGET %s needs a destination worktree and none was found", in.Target)
	}
	// Step 1: verify the lock records.
	if err := m.verifyLocks(ctx, dir, in.Fence.Locks); err != nil {
		return stop(outcome.StopLockLost, "%v", err)
	}
	// Step 2: the renewal check.
	if !m.renewalOK(in) {
		return stop(outcome.StopLockLost, "the lock renewal is older than two thirds of the lease")
	}
	// Step 3: the DW's HEAD is TARGET and it has no tracked changes.
	switched := false
	if in.WW != nil && in.WW.Path == dw.Path {
		if err := m.Brancher.SwitchDestinationToTarget(ctx, dw, in.Target); err != nil {
			return m.back(ctx, in, true, stop(outcome.StopCheckoutChanged, "switching the destination worktree to %s: %v", in.Target, err))
		}
		switched = true
	}
	if reason := m.checkDW(ctx, dw.Path, in.Target); reason != "" {
		return m.back(ctx, in, switched, stop(outcome.StopCheckoutChanged, "%s", reason))
	}
	// Step 4: merge --ff-only.
	if m.BeforeWrite != nil {
		m.BeforeWrite()
	}
	mr := m.Git.Run(ctx, dw.Path, "merge", "--ff-only", in.Result)
	if !mr.OK() {
		res := m.classifyMerge(ctx, dw.Path, in, mr)
		return m.back(ctx, in, switched, res)
	}
	// Step 5: confirm.
	res := m.confirm(ctx, dw.Path, in)
	if res.Status != Moved {
		return m.back(ctx, in, switched, res)
	}
	return res
}

func (m *Mover) classifyMerge(ctx context.Context, dir string, in Input, mr gitexec.Result) Result {
	if mr.TimedOut {
		return stop(outcome.StopGitError, "git merge: %v", gitexec.ErrTimeout)
	}
	cur, err := m.tip(ctx, dir, in.Target)
	if err == nil && cur != in.TipsTarget {
		return Result{Status: TargetMoved, Detail: fmt.Sprintf("%s moved from %s to %s", in.Target, in.TipsTarget, cur)}
	}
	if err == nil && m.Git.Run(ctx, dir, "merge-base", "--is-ancestor", cur, in.Result).Code == 1 {
		return Result{Status: TargetMoved, Detail: fmt.Sprintf("the result does not descend from %s at %s", in.Target, cur)}
	}
	return stop(outcome.StopGitError, "git merge --ff-only failed (exit %d): %s", mr.Code, strings.TrimSpace(string(mr.Stderr)))
}

// back switches the DW back to the work branch after a failure, when the
// tool had switched it away.
func (m *Mover) back(ctx context.Context, in Input, switched bool, res Result) Result {
	if !switched {
		return res
	}
	if err := m.Brancher.SwitchDestinationToWork(ctx, in.DW, in.OpID); err != nil {
		res.Detail += fmt.Sprintf(" (switching the destination worktree back to the work branch failed: %v)", err)
	}
	return res
}

// checkDW returns why the DW is not fit for the fast-forward, or "".
func (m *Mover) checkDW(ctx context.Context, dir, target string) string {
	r := m.Git.Run(ctx, dir, "symbolic-ref", "-q", "HEAD")
	if !r.OK() || strings.TrimSpace(string(r.Stdout)) != targetRef(target) {
		return fmt.Sprintf("the destination worktree %s is not on %s", dir, target)
	}
	s := m.Git.Run(ctx, dir, "status", "--porcelain", "--untracked-files=no")
	if !s.OK() {
		return fmt.Sprintf("cannot read the status of %s: %s", dir, strings.TrimSpace(string(s.Stderr)))
	}
	if strings.TrimSpace(string(s.Stdout)) != "" {
		return fmt.Sprintf("the destination worktree %s has tracked changes", dir)
	}
	return ""
}

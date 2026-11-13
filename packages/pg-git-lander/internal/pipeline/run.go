package pipeline

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/branchmove"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/cleanup"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/conflict"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/hooks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/landmove"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/oplog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/oprecord"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/rebase"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/workbranch"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/worktrees"
)

// This file is steps 8 to 13 of the pipeline (design section 7.1): the rebase
// with the conflict loop, the after_rebase and before_move hooks, the fenced
// move with its race return, the after hook, the clean-up and the creation of
// a stopped operation.

// stopSpec is a stop in the making.
type stopSpec struct {
	reason  outcome.StopReason
	message string
	detail  string
	paths   []string
	logTail string
}

// runner is the working state of Run.
type runner struct {
	p *Prepared

	stats     outcome.Stats
	resolved  []outcome.Resolution
	autofix   []outcome.Autofix
	dropped   []string
	entries   []outcome.WarningEntry
	rebased   bool
	baseMoved bool
	moved     bool
	landed    string
	cres      *cleanup.Result
	// targetWait is the time spent taking the second lock phase (TARGET and the
	// destination worktree); targetHeld the time they were held, over every
	// phase this run took; targetSince is when the current hold began (zero
	// when not held).
	targetWait, targetHeld time.Duration
	targetSince            time.Time
	// ctx is the operation's context: a signal cancels it, and lostCheck turns
	// that into an interrupted stop at the next step boundary.
	ctx context.Context
}

// Run completes the operation of steps 8 to 13 from a Prepared that holds its
// locks, and returns the outcome object. Every exit releases or stops the
// operation's records: locks are released on success, and a stop turns the
// BRANCH and worktree records to stopped and deletes the TARGET record. An
// unexpected failure is a stop with reason internal-error, never an error
// return; the error is only for a stop that itself could not be recorded.
func (p *Prepared) Run(ctx context.Context) (*outcome.Outcome, error) {
	// A lease that fails renewal cancels the operation's context at once, so
	// the current step's process group is killed instead of running on until
	// the next step boundary (design section 8.7).
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go func() {
		select {
		case err := <-p.Lost:
			cancel(&lostCause{err: err})
		case <-ctx.Done():
		}
	}()
	s := &runner{p: p, ctx: ctx}
	for _, w := range p.Warnings {
		s.entries = append(s.entries, outcome.WarningEntry{Warning: w})
	}
	defer func() { _ = p.Log.Close() }()

	out, sp, err := s.run(ctx)
	switch {
	case err != nil && errors.Is(err, locks.ErrLockLost):
		sp = &stopSpec{reason: outcome.StopLockLost, message: err.Error()}
	case err != nil && lockLostCause(ctx) != nil:
		sp = &stopSpec{reason: outcome.StopLockLost, message: lockLostCause(ctx).Error()}
	case err != nil && ctx.Err() != nil:
		sp = &stopSpec{reason: outcome.StopInterrupted, message: context.Cause(ctx).Error()}
	case err != nil:
		sp = &stopSpec{reason: outcome.StopInternalError, message: err.Error()}
	}
	if sp != nil {
		return s.stop(ctx, sp)
	}
	return out, nil
}

func (s *runner) run(ctx context.Context) (*outcome.Outcome, *stopSpec, error) {
	p := s.p
	plan := p.plan
	if plan == nil {
		plan = &resumePlan{}
	}
	s.stats.ConflictRounds = plan.conflictRounds
	s.rebased = plan.rebased
	if p.Operation == worktrees.Land && p.Op.Set.ByRef(locks.BranchRef(p.Request.Target)) != nil {
		s.targetSince = p.d.Clock.Now() // taken back by a resume at stage moving or later
	}
	switch plan.entry {
	case entryCleanup:
		s.moved, s.landed = plan.moved, plan.landed
		return s.cleanup(ctx, plan.nothing)
	case entryAfterHook:
		s.moved, s.landed = true, plan.landed
		return s.afterAndCleanup(ctx)
	}
	if p.Done() {
		return s.cleanup(ctx, true)
	}
	for first := true; ; first = false {
		if sp := s.lostCheck(); sp != nil {
			return nil, sp, nil
		}
		entry := entryNormal
		if first {
			entry = plan.entry
		}
		if entry == entryNormal {
			// Hooks and a rebase never run under TARGET: give back what a
			// resume at stage moving took.
			if err := s.releaseTarget(ctx); err != nil {
				return nil, nil, err
			}
		}
		s.baseMoved = entry == entryRebased || entry == entryAfterRebase || (entry == entryBeforeMove && plan.baseMoved)
		switch {
		case entry == entryContinueConflict || entry == entrySkipConflict:
			if sp, err := s.continueRebase(ctx, entry == entrySkipConflict); sp != nil || err != nil {
				return nil, sp, err
			}
		case entry == entryNormal && p.Path == rebase.NeedsRebase:
			if sp, err := s.doRebase(ctx); sp != nil || err != nil {
				return nil, sp, err
			}
		}
		tip, err := s.workTip(ctx)
		if err != nil {
			return nil, nil, err
		}
		if p.Operation == worktrees.Land && tip == p.Tips.Target {
			// The rebase dropped every commit as already upstream: nothing is
			// left to land; BRANCH is retired as for a land that had nothing.
			if err := s.setResult(ctx, tip); err != nil {
				return nil, nil, err
			}
			return s.cleanup(ctx, false)
		}
		if entry != entryMove {
			if s.baseMoved && entry != entryBeforeMove {
				if sp, err := s.hookPoint(ctx, hooks.PointAfterRebase, oprecord.StageAfterRebase); sp != nil || err != nil {
					return nil, sp, err
				}
			}
			if sp, err := s.hookPoint(ctx, hooks.PointBeforeMove, oprecord.StageBeforeMove); sp != nil || err != nil {
				return nil, sp, err
			}
		}
		if p.Operation == worktrees.Land {
			// The second lock phase: only now, with the hooks done, are TARGET
			// and the destination worktree taken.
			act, out, sp, err := s.enterMain(ctx)
			switch {
			case sp != nil || err != nil || out != nil:
				return out, sp, err
			case act == mainRetry:
				continue
			}
		}
		status, sp, err := s.move(ctx)
		if sp != nil || err != nil {
			return nil, sp, err
		}
		if status != landmove.TargetMoved {
			break
		}
		// TARGET moved after the check, under the lock (a writer that does not
		// use this tool): the full return to step 3.
		if out, sp, err, done := s.raceReturn(ctx); done || sp != nil || err != nil {
			return out, sp, err
		}
	}
	return s.afterAndCleanup(ctx)
}

// mainAction is what enterMain decided.
type mainAction int

const (
	// mainMove: TARGET and the destination worktree are held and TARGET is at
	// the recorded base tip (or the result was rebased onto its new tip with
	// the hook result reused): the move may run.
	mainMove mainAction = iota
	// mainRetry: the locks are released and the run starts over at step 3.
	mainRetry
)

// enterMain takes TARGET and the destination worktree (phase two) and
// validates the result against TARGET's present tip. When TARGET still is at
// tips.target the result is ready to fast-forward. When it moved, the revalidate
// setting decides: reuse the hook result (rebase under the locks) or return to
// the rebase with the hooks to run again, outside the lock.
func (s *runner) enterMain(ctx context.Context) (mainAction, *outcome.Outcome, *stopSpec, error) {
	p := s.p
	if sp, err := s.lockTarget(ctx); sp != nil || err != nil {
		return 0, nil, sp, err
	}
	cur, err := p.revParse(ctx, "refs/heads/"+p.Request.Target)
	if err != nil {
		return 0, nil, nil, err
	}
	if cur == p.Tips.Target {
		return mainMove, nil, nil, nil
	}
	mode := p.Config.Revalidate
	if mode != config.RevalidateNone && mode != config.RevalidatePaths {
		mode = config.RevalidateFull
	}
	if mode != config.RevalidateFull {
		reused, out, sp, err := s.reuseHooks(ctx, mode, cur)
		if err != nil || out != nil || sp != nil {
			return 0, out, sp, err
		}
		if reused {
			return mainMove, nil, nil, nil
		}
	}
	out, sp, err, done := s.raceReturn(ctx)
	if done || sp != nil || err != nil {
		return 0, out, sp, err
	}
	return mainRetry, nil, nil, nil
}

// lockTarget takes the second lock phase. A busy or refused lock stops the
// operation with target-busy: it holds only the BRANCH-side locks, so TARGET
// stays free.
func (s *runner) lockTarget(ctx context.Context) (*stopSpec, error) {
	p := s.p
	start := p.d.Clock.Now()
	var held []*locks.Held
	err := p.d.acquireSettled(ctx, p.Layer, p.Pre.Lock.Wait, p.Request.RepoFlag, func(ctx context.Context) error {
		var aerr error
		held, aerr = p.Op.AcquireTarget(ctx)
		return aerr
	})
	s.targetWait += p.d.Clock.Since(start)
	if err != nil {
		var busy *locks.BusyError
		var ref *locks.RefusedError
		switch {
		case errors.As(err, &busy):
			return &stopSpec{reason: outcome.StopTargetBusy, message: s.busyMessage(ctx, busy.Ref, busy.Holder)}, nil
		case errors.As(err, &ref):
			return &stopSpec{reason: outcome.StopTargetBusy, message: s.stoppedHolderMessage(ctx, ref)}, nil
		}
		return nil, err
	}
	if len(held) > 0 {
		s.targetSince = p.d.Clock.Now()
		p.renewMore(ctx, held)
	}
	return nil, nil
}

// busyMessage names the holder of a lock of the second phase with its stage
// and how long it has been in it (Holder.AgeS, see outcome.Holder).
func (s *runner) busyMessage(_ context.Context, ref string, h outcome.Holder) string {
	stage := h.Stage
	if stage == "" {
		stage = "unknown"
	}
	return fmt.Sprintf("%s is held by %s %s (op %s, pid %d on %s) at stage %s for %s; wait for it and run --continue",
		ref, h.Operation, h.Branch, h.OpID, h.PID, h.Host, stage, (time.Duration(h.AgeS) * time.Second).Truncate(time.Second))
}

// stoppedHolderMessage is the target-busy message when a stopped operation
// holds the second-phase lock and needs its owner's decision: it names the
// owner's operation and branch and the commands that resolve it.
func (s *runner) stoppedHolderMessage(ctx context.Context, ref *locks.RefusedError) string {
	h := ref.Holder
	cmds := outcome.NextCommands(h.Operation, h.Branch, s.p.Request.RepoFlag, []string{outcome.FlagContinue, outcome.FlagAbort})
	return fmt.Sprintf("%s (%s): its owner must resolve it with `%s` or `%s`",
		strings.TrimSuffix(s.busyMessage(ctx, ref.Ref, h), "; wait for it and run --continue"), ref.Reason, cmds[0], cmds[1])
}

// releaseTarget gives back the second-phase locks (a no-op when not held) and
// closes the hold-time account.
func (s *runner) releaseTarget(ctx context.Context) error {
	p := s.p
	if p.Operation != worktrees.Land {
		return nil
	}
	held := p.Op.TargetLocks()
	if len(held) == 0 {
		return nil
	}
	p.stopRenewalOf(held)
	err := p.Op.ReleaseTarget(context.WithoutCancel(ctx))
	s.targetEnd()
	return err
}

// targetEnd adds the current hold to the account.
func (s *runner) targetEnd() {
	if !s.targetSince.IsZero() {
		s.targetHeld += s.p.d.Clock.Since(s.targetSince)
		s.targetSince = time.Time{}
	}
}

// raceReturn is the return to step 3 after TARGET moved, with the hooks to
// run again: it counts a race retry (the bound stops with race, with TARGET
// and the destination worktree given back first), releases the second phase,
// re-snapshots and decides the path anew. done is true when the operation
// ended here (a stop, or nothing left to land and the clean-up ran); false
// means the loop starts over.
func (s *runner) raceReturn(ctx context.Context) (*outcome.Outcome, *stopSpec, error, bool) {
	p := s.p
	if s.stats.RaceRetries >= p.Config.MaxRaceRetries {
		if err := s.releaseTarget(ctx); err != nil {
			return nil, nil, err, true
		}
		tip, err := s.workTip(ctx)
		if err != nil {
			return nil, nil, err, true
		}
		if err := p.EnterStage(ctx, oprecord.StageMoving, &tip); err != nil {
			return nil, nil, err, true
		}
		return nil, &stopSpec{
			reason:  outcome.StopRace,
			message: fmt.Sprintf("%s moved %d times while the operation ran", p.Request.Target, s.stats.RaceRetries+1),
		}, nil, true
	}
	if err := s.releaseTarget(ctx); err != nil {
		return nil, nil, err, true
	}
	old := p.Tips.Target
	if err := p.Op.Branch.Update(ctx, func(r *locks.Record) { r.Move = locks.Move{} }); err != nil {
		return nil, nil, err, true
	}
	if err := p.Resnapshot(ctx); err != nil {
		return nil, nil, err, true
	}
	s.stats.RaceRetries++
	if err := p.Events.RaceRetry(ctx, s.stats.RaceRetries, old, p.Tips.Target, p.Config.Revalidate, false); err != nil {
		return nil, nil, err, true
	}
	cur, err := s.workTip(ctx)
	if err != nil {
		return nil, nil, err, true
	}
	if p.Path, err = p.Decide(ctx, cur); err != nil {
		return nil, nil, err, true
	}
	if p.Path == rebase.NothingToLand {
		if err := s.setResult(ctx, cur); err != nil {
			return nil, nil, err, true
		}
		out, sp, err := s.cleanup(ctx, false)
		return out, sp, err, true
	}
	return nil, nil, nil, false
}

// reuseHooks is the revalidate shortcut under the TARGET lock: TARGET moved
// from tips.target to cur while the hooks ran. With revalidate = paths the
// hook result is kept only when the commits TARGET gained touch no file the
// branch changes; with none, whenever the rebase onto cur is clean. The
// rebase runs now, under the locks (a few commits); a conflict is undone and
// the caller takes the full return. out is non-nil when the rebase left
// nothing to land and the clean-up ran.
func (s *runner) reuseHooks(ctx context.Context, mode, cur string) (bool, *outcome.Outcome, *stopSpec, error) {
	p := s.p
	work := p.Selection.Work.Path
	old := p.Tips.Target
	// The hook result is only valid under the same configuration.
	if cfg, err := p.Pre.Load(ctx, cur); err != nil || cfg.Digest != p.Config.Digest {
		return false, nil, nil, nil
	}
	if r := p.d.Git.Run(ctx, p.Repo, "merge-base", "--is-ancestor", old, cur); !r.OK() {
		return false, nil, nil, nil // TARGET was rewritten, not advanced
	}
	tip, err := s.workTip(ctx)
	if err != nil {
		return false, nil, nil, err
	}
	if mode == config.RevalidatePaths {
		gained, err := s.changedFiles(ctx, old, cur)
		if err != nil {
			return false, nil, nil, err
		}
		mine, err := s.changedFiles(ctx, old, tip)
		if err != nil {
			return false, nil, nil, err
		}
		for f := range mine {
			if gained[f] {
				return false, nil, nil, nil
			}
		}
	}
	if err := p.EnterStage(ctx, oprecord.StageRebasing, &tip); err != nil {
		return false, nil, nil, err
	}
	rb := &rebase.Rebaser{Git: p.d.Git}
	res, err := rb.Rebase(ctx, work, cur)
	if err != nil {
		return false, nil, nil, err
	}
	if !res.Finished {
		if in, ierr := rb.InProgress(ctx, work); ierr == nil && in {
			if _, aerr := p.git(ctx, work, "rebase", "--abort"); aerr != nil {
				return false, nil, nil, aerr
			}
		}
		return false, nil, nil, nil
	}
	s.rebased = true
	s.dropped = append(s.dropped, res.Dropped...)
	s.stats.RebasedCommits += len(res.Replayed)
	if err := p.Resnapshot(ctx); err != nil {
		return false, nil, nil, err
	}
	if err := p.Events.RaceRetry(ctx, s.stats.RaceRetries, old, cur, mode, true); err != nil {
		return false, nil, nil, err
	}
	p.Log.Line("REVALIDATE", fmt.Sprintf("%s: %s moved %s to %s, hook result reused", mode, p.Request.Target, old, cur))
	if res.Tip == cur {
		// Every commit is already upstream: nothing is left to land.
		if err := s.setResult(ctx, res.Tip); err != nil {
			return false, nil, nil, err
		}
		out, sp, err := s.cleanup(ctx, false)
		return true, out, sp, err
	}
	if err := s.setResult(ctx, res.Tip); err != nil {
		return false, nil, nil, err
	}
	return true, nil, nil, nil
}

// changedFiles lists the paths that differ between from and to (a rename is a
// delete and an add, so both names count).
func (s *runner) changedFiles(ctx context.Context, from, to string) (map[string]bool, error) {
	out, err := s.p.git(ctx, s.p.Repo, "diff", "--no-ext-diff", "--name-only", "--no-renames", "-z", from, to)
	if err != nil {
		return nil, err
	}
	files := map[string]bool{}
	for _, f := range strings.Split(out, "\x00") {
		if f = strings.TrimSpace(f); f != "" {
			files[f] = true
		}
	}
	return files, nil
}

// afterAndCleanup is step 12, the after hook, and step 13.
func (s *runner) afterAndCleanup(ctx context.Context) (*outcome.Outcome, *stopSpec, error) {
	// Step 12: the after hook; a failure is a warning, the move happened.
	if sp := s.lostCheck(); sp != nil {
		return nil, sp, nil
	}
	f, err := s.runHook(ctx, hooks.PointAfter, oprecord.StageAfter)
	if err != nil {
		return nil, nil, err
	}
	if f != nil {
		s.entries = append(s.entries, outcome.WarningEntry{
			Warning: outcome.WarnAfterHookFailed, Reason: hookDetail(f),
			Detail: fmt.Sprintf("the after hook (entry %d) %s", f.Index, hookWhy(f)),
		})
	}
	return s.cleanup(ctx, false)
}

// lostCause is the cancellation cause of a context ended by a lost lease.
type lostCause struct{ err error }

func (c *lostCause) Error() string { return fmt.Sprintf("lock lost: %v", c.err) }
func (c *lostCause) Unwrap() error { return c.err }

// lockLostCause returns the lost-lease cause of ctx, or nil.
func lockLostCause(ctx context.Context) error {
	var lc *lostCause
	if errors.As(context.Cause(ctx), &lc) {
		return lc
	}
	return nil
}

func (s *runner) lostCheck() *stopSpec {
	if s.ctx != nil {
		if err := lockLostCause(s.ctx); err != nil {
			return &stopSpec{reason: outcome.StopLockLost, message: err.Error()}
		}
	}
	if s.ctx != nil && s.ctx.Err() != nil {
		return &stopSpec{reason: outcome.StopInterrupted, message: context.Cause(s.ctx).Error()}
	}
	select {
	case err := <-s.p.Lost:
		return &stopSpec{reason: outcome.StopLockLost, message: fmt.Sprintf("lock lost: %v", err)}
	default:
		return nil
	}
}

func (s *runner) workTip(ctx context.Context) (string, error) {
	return s.p.git(ctx, s.p.Selection.Work.Path, "rev-parse", "HEAD")
}

// setResult records tips.result when it changed (an autofix commit).
func (s *runner) setResult(ctx context.Context, tip string) error {
	p := s.p
	if p.Tips.Result == tip {
		return nil
	}
	if err := p.Op.SetTips(ctx, func(t *locks.Tips) { v := tip; t.Result = &v }); err != nil {
		return err
	}
	p.Tips.Result, p.Facts.Result = tip, tip
	return nil
}

// fence is what a move verifies before it writes: every held lock's current
// record oid and the age of the last successful renewal. It is rebuilt right
// before each move, after the record writes that change the oids.
func (s *runner) fence() landmove.Fence {
	p := s.p
	held := append([]*locks.Held(nil), p.Op.Set.Locks...)
	checks := func() []landmove.LockCheck {
		var lc []landmove.LockCheck
		for _, h := range held {
			lc = append(lc, landmove.LockCheck{Ref: h.Ref, Oid: h.OID()})
		}
		return lc
	}
	ttl := p.Layer.Config.LeaseTTL
	// Current re-reads the oids at each verification: a renewal rewrites the
	// lock record, so oids taken here go stale while the clean-up removes
	// worktrees (seconds) before it retires BRANCH.
	return landmove.Fence{Locks: checks(), Current: checks, RenewalFresh: func() bool {
		for _, h := range held {
			if h.RenewAge() >= ttl*2/3 {
				return false
			}
		}
		return true
	}}
}

func (s *runner) hookRunner() *hooks.Runner {
	p := s.p
	return &hooks.Runner{Git: p.d.Git, AutofixAttempts: p.Config.AutofixAttempts, RunGitHooks: p.Config.RunGitHooks}
}

// doRebase is step 8, with the conflict loop of section 10.
func (s *runner) doRebase(ctx context.Context) (*stopSpec, error) {
	p := s.p
	work := p.Selection.Work.Path
	tip, err := s.workTip(ctx)
	if err != nil {
		return nil, err
	}
	if err := p.EnterStage(ctx, oprecord.StageRebasing, &tip); err != nil {
		return nil, err
	}
	base := p.Tips.Target
	if p.Operation == worktrees.Rebase {
		base = p.Tips.Onto
	}
	rb := &rebase.Rebaser{Git: p.d.Git}
	res, err := rb.Rebase(ctx, work, base)
	if err != nil {
		return nil, err
	}
	s.rebased = true
	s.dropped = append(s.dropped, res.Dropped...)
	if res.Stop != nil {
		return &stopSpec{reason: res.Stop.Reason, message: "git rebase stopped for a reason other than a conflict", logTail: res.Stop.LogTail}, nil
	}
	if res.Finished {
		s.stats.RebasedCommits += len(res.Replayed)
		s.baseMoved = res.BaseMoved
		return nil, nil
	}

	cr, err := s.resolver(rb).Resolve(ctx, conflict.Input{Dir: work, Rounds: s.stats.ConflictRounds, AutoMerged: res.AutoMerged, Diag: p.Diag})
	return s.afterConflicts(ctx, work, base, cr, err)
}

func (s *runner) resolver(rb *rebase.Rebaser) *conflict.Resolver {
	p := s.p
	return &conflict.Resolver{Git: p.d.Git, Hooks: s.hookRunner(), Rebase: rb, Config: p.Config, Facts: p.Facts}
}

// continueRebase resumes at stage rebasing with git's rebase in progress
// (design section 7.3): the person's resolution is committed (or, with skip,
// the replayed commit is dropped) and the conflict loop goes on.
func (s *runner) continueRebase(ctx context.Context, skip bool) (*stopSpec, error) {
	p := s.p
	work := p.Selection.Work.Path
	base := p.Tips.Target
	if p.Operation == worktrees.Rebase {
		base = p.Tips.Onto
	}
	rb := &rebase.Rebaser{Git: p.d.Git}
	cr, err := s.resolver(rb).Continue(ctx, conflict.Input{Dir: work, Rounds: s.stats.ConflictRounds, Diag: p.Diag}, skip)
	return s.afterConflicts(ctx, work, base, cr, err)
}

// afterConflicts folds the end of a conflict loop into the runner.
func (s *runner) afterConflicts(ctx context.Context, work, base string, cr *conflict.Result, err error) (*stopSpec, error) {
	p := s.p
	if err != nil {
		return nil, err
	}
	s.stats.ConflictRounds = cr.Rounds
	if p.Diag != nil {
		// The conflict loop's attribute checks may have found an attribute that
		// names a merge driver with no definition (design section 9.4).
		for _, w := range p.Diag.Warnings() {
			s.addWarning(w)
		}
	}
	s.resolved = append(s.resolved, cr.Resolved...)
	s.dropped = append(s.dropped, cr.Dropped...)
	if err := s.conflictEvents(ctx, cr); err != nil {
		return nil, err
	}
	if st := cr.Stop; st != nil {
		msg := fmt.Sprintf("the conflict in %s was not resolved", strings.Join(st.Paths, ", "))
		if st.Reason == outcome.StopMaxConflictRounds {
			msg = fmt.Sprintf("more than %d conflict rounds", p.Config.MaxConflictRounds)
		}
		return &stopSpec{reason: st.Reason, message: msg, detail: st.Detail, paths: st.Paths, logTail: st.LogTail}, nil
	}
	out, err := p.git(ctx, work, "rev-list", "--count", base+"..HEAD")
	if err != nil {
		return nil, err
	}
	var n int
	if _, err := fmt.Sscanf(out, "%d", &n); err != nil {
		return nil, fmt.Errorf("rev-list --count: %q", out)
	}
	s.stats.RebasedCommits += n
	s.baseMoved = n > 0
	return nil, nil
}

// conflictEvents emits one conflict event for each commit whose conflict was
// resolved and one for a conflict that stopped the operation.
func (s *runner) conflictEvents(ctx context.Context, cr *conflict.Result) error {
	var order []string
	groups := map[string][]outcome.Resolution{}
	for _, r := range cr.Resolved {
		if _, ok := groups[r.Commit]; !ok {
			order = append(order, r.Commit)
		}
		groups[r.Commit] = append(groups[r.Commit], r)
	}
	for _, c := range order {
		var paths []string
		rule, result := "", ""
		for _, r := range groups[c] {
			paths = append(paths, r.Path)
			if rule == "" {
				rule = r.Rule
				if rule == "" {
					rule = r.Driver
				}
				result = r.Result
			}
		}
		if err := s.p.Events.Conflict(ctx, paths, rule, result); err != nil {
			return err
		}
	}
	if cr.Stop != nil {
		return s.p.Events.Conflict(ctx, cr.Stop.Paths, cr.Stop.Rule, "stopped")
	}
	return nil
}

// hookPoint runs the after_rebase or before_move hook and turns a failure
// into the stop its effect names.
func (s *runner) hookPoint(ctx context.Context, point, stage string) (*stopSpec, error) {
	if sp := s.lostCheck(); sp != nil {
		return sp, nil
	}
	f, err := s.runHook(ctx, point, stage)
	if err != nil || f == nil {
		return nil, err
	}
	eff := f.Effect()
	reason := outcome.StopHookFailed
	if eff.Stop != nil {
		reason = *eff.Stop
	}
	return &stopSpec{
		reason: reason, message: fmt.Sprintf("the %s hook (entry %d) %s", f.Point, f.Index, hookFailWhy(f)),
		detail: hookDetail(f), logTail: f.LogTail,
	}, nil
}

func hookFailWhy(f *hooks.Failure) string {
	switch f.Kind {
	case hooks.AutofixExhausted:
		return "kept changing files after every autofix attempt"
	case hooks.CommitRejected:
		return "left an autofix commit that a git commit hook rejected"
	}
	return hookWhy(f)
}

// runHook enters stage, runs one hook point in the work worktree, logs every
// command, collects the autofix commits and the skipped hooks and returns the
// failure, if any.
func (s *runner) runHook(ctx context.Context, point, stage string) (*hooks.Failure, error) {
	p := s.p
	before, err := s.workTip(ctx)
	if err != nil {
		return nil, err
	}
	if err := p.EnterStage(ctx, stage, &before); err != nil {
		return nil, err
	}
	p.Facts.BaseMoved = s.baseMoved
	hr, err := s.hookRunner().RunPoint(ctx, hooks.PointInput{
		Point: point, Hooks: p.Config.HooksFor(string(p.Operation), point), Facts: p.Facts,
	})
	for _, rec := range hr.Records {
		p.Log.Command("hook", rec)
		_ = p.Events.Hook(context.WithoutCancel(ctx), rec)
	}
	p.SkippedHooks = append(p.SkippedHooks, hr.Skipped...)
	if err != nil {
		return nil, err
	}
	after, err := s.workTip(ctx)
	if err != nil {
		return nil, err
	}
	if after != before {
		if err := s.collectAutofix(ctx, point, before, after, hr.Records); err != nil {
			return nil, err
		}
		if err := s.setResult(ctx, after); err != nil {
			return nil, err
		}
	}
	return hr.Failure, nil
}

func (s *runner) collectAutofix(ctx context.Context, point, before, after string, recs []hooks.CommandRecord) error {
	p := s.p
	work := p.Selection.Work.Path
	list, err := p.git(ctx, work, "rev-list", "--reverse", before+".."+after)
	if err != nil {
		return err
	}
	var names []string
	for _, r := range recs {
		if r.Result == hooks.ResultAutofix {
			names = append(names, fmt.Sprintf("%s[%d]", point, r.Index))
		}
	}
	for i, c := range strings.Fields(list) {
		files, err := p.git(ctx, work, "diff-tree", "--no-commit-id", "--name-only", "-r", c)
		if err != nil {
			return err
		}
		name := point
		if i < len(names) {
			name = names[i]
		}
		s.autofix = append(s.autofix, outcome.Autofix{Hook: name, Commit: c, Files: strings.Fields(files)})
		s.stats.AutofixCommits++
	}
	return nil
}

// move is step 11.
func (s *runner) move(ctx context.Context) (landmove.Status, *stopSpec, error) {
	p := s.p
	tip, err := s.workTip(ctx)
	if err != nil {
		return 0, nil, err
	}
	if err := p.EnterStage(ctx, oprecord.StageMoving, &tip); err != nil {
		return 0, nil, err
	}
	// The intent is written first; the fence is built after it, because the
	// write changes the BRANCH record's oid.
	if err := p.Op.SetMove(ctx, tip); err != nil {
		return 0, nil, err
	}
	if sp := s.lostCheck(); sp != nil {
		return 0, sp, nil
	}
	fence := s.fence()
	sel := p.Selection
	if p.Operation == worktrees.Land {
		mv := &landmove.Mover{Git: p.d.Git, Brancher: &workbranch.Brancher{Git: p.d.Git}}
		// The fenced transaction and the merge take milliseconds: once started
		// they finish, so the move runs detached from a signal's cancellation.
		r := mv.Move(context.WithoutCancel(ctx), landmove.Input{
			OpID: p.OpID, Target: p.Request.Target, TipsTarget: p.Tips.Target, Result: tip,
			DW: sel.Destination, WW: sel.Work, TargetCheckedOut: sel.TargetCheckedOut, Fence: fence,
		})
		switch r.Status {
		case landmove.Moved:
			s.moved, s.landed = true, tip
			return landmove.Moved, nil, nil
		case landmove.TargetMoved:
			return landmove.TargetMoved, nil, nil
		}
		return 0, &stopSpec{reason: r.Stop, message: "the move of " + p.Request.Target + " stopped", detail: r.Detail}, nil
	}

	holder, err := s.holder(ctx)
	if err != nil {
		return 0, nil, err
	}
	r := (&branchmove.Mover{Git: p.d.Git}).Move(context.WithoutCancel(ctx), branchmove.Input{
		Operation: worktrees.Rebase, Branch: p.Request.Branch, TipsBranch: p.Tips.Branch,
		Result: tip, Dir: sel.Work.Path, Holder: holder, Fence: fence,
	})
	if r.Status != branchmove.Moved {
		return 0, &stopSpec{reason: r.Stop, message: "the move of " + p.Request.Branch + " stopped", detail: r.Detail}, nil
	}
	s.moved = true
	return landmove.Moved, nil, nil
}

// holder is the worktree other than the work worktree that has BRANCH checked
// out, or nil.
func (s *runner) holder(ctx context.Context) (*worktrees.Chosen, error) {
	p := s.p
	wts, err := p.d.Git.Worktrees(ctx, p.Repo)
	if err != nil {
		return nil, err
	}
	work := realPath(p.Selection.Work.Path)
	for _, w := range wts {
		if w.Branch == "refs/heads/"+p.Request.Branch && realPath(w.Path) != work {
			return &worktrees.Chosen{Path: w.Path}, nil
		}
	}
	return nil, nil
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// cleanup is step 13, then the release of every lock and the outcome.
// nothing is true when the decided path had nothing to do.
func (s *runner) cleanup(ctx context.Context, nothing bool) (*outcome.Outcome, *stopSpec, error) {
	p := s.p
	if sp := s.lostCheck(); sp != nil {
		return nil, sp, nil
	}
	if p.Operation == worktrees.Land {
		// Step 13 needs TARGET and the destination worktree held.
		if sp, err := s.lockTarget(ctx); sp != nil || err != nil {
			return nil, sp, err
		}
	}
	if err := p.EnterStage(ctx, oprecord.StageCleanup, nil); err != nil {
		return nil, nil, err
	}
	res := (&cleanup.Cleaner{Git: p.d.Git}).Run(ctx, s.cleanupInput(nothing))
	s.cres = &res
	if res.Stop != nil {
		return nil, &stopSpec{reason: res.Stop.Reason, message: "the clean-up stopped", detail: res.Stop.Detail}, nil
	}
	for _, w := range res.Warnings {
		s.entries = append(s.entries, outcome.WarningEntry{Warning: w.Word, Detail: w.Detail})
	}
	if err := p.Release(ctx); err != nil {
		return nil, nil, err
	}
	s.targetEnd()

	word := outcome.Landed
	switch {
	case p.Operation == worktrees.Land && !s.moved:
		word = outcome.NothingToLand
	case p.Operation == worktrees.Rebase && s.moved:
		word = outcome.Rebased
	case p.Operation == worktrees.Rebase:
		word = outcome.UpToDate
	}
	o := outcome.New(word)
	s.fill(o)
	s.opEnd(ctx, o)
	return o, nil, nil
}

// cleanupInput is the recorded facts the clean-up of step 13 works from.
func (s *runner) cleanupInput(nothing bool) cleanup.Input {
	p := s.p
	in := cleanup.Input{
		Operation: p.Operation, NothingToLand: nothing, OpID: p.OpID, Branch: p.Request.Branch,
		Tips: p.Tips, KeepSourceWorktree: p.Request.KeepSourceWorktree, KeepSourceBranch: p.Request.KeepSourceBranch,
		Worktrees: p.Selection.Worktrees, Work: p.Selection.Work, Dir: p.Repo,
		Fence: s.fence(), Chdir: p.d.Chdir,
	}
	if p.Operation == worktrees.Land {
		in.Target = p.Request.Target
	}
	return in
}

// fill sets the fields every outcome of an operation that reached step 8
// carries.
func (s *runner) fill(o *outcome.Outcome) {
	p := s.p
	req := p.Request
	o.OpID, o.Log = p.OpID, p.Log.Path
	o.Operation, o.Branch, o.Repo = string(p.Operation), req.Branch, req.RepoFlag
	if p.Operation == worktrees.Land {
		o.Target = req.Target
	} else {
		o.Onto = req.Onto
	}
	o.WorkBranch = p.WorkBranch
	o.PlaceholderBranch = p.Op.Record().PlaceholderBranch
	t := p.Tips
	o.Tips = &t
	o.PathTaken = s.pathTaken()
	st := s.stats
	st.DurationMS = p.d.Clock.Since(p.Started).Milliseconds()
	o.Stats = &st
	o.Dropped, o.Resolved, o.Autofix = s.dropped, s.resolved, s.autofix
	for _, k := range p.SkippedHooks {
		o.SkippedHooks = append(o.SkippedHooks, outcome.SkippedHook{Point: k.Point, Index: k.Index, Exit: k.Code, StderrTail: k.StderrTail})
	}
	o.Warnings = s.entries
	if sel := p.Selection; sel != nil {
		o.SourceWorktree, o.WorkWorktree = outcomeWT(sel.Source), outcomeWT(sel.Work)
		o.DestinationWorktree = outcomeWT(sel.Destination)
	}
	if p.Operation == worktrees.Land && s.moved {
		o.LandedCommit = s.landed
	}
	if c := s.cres; c != nil {
		o.BranchFate, o.OldBranchTip, o.Restored = c.BranchFate, c.OldBranchTip, c.Restored
		if c.WorktreeFate.Fate != "" {
			f := c.WorktreeFate
			o.WorktreeFate = &f
		}
	}
}

func outcomeWT(c *worktrees.Chosen) *outcome.Worktree {
	if c == nil {
		return nil
	}
	return &outcome.Worktree{Name: c.AdminName, Path: c.Path}
}

func (s *runner) pathTaken() string {
	switch {
	case !s.moved:
		return outcome.PathNothing
	case s.rebased:
		return outcome.PathRebase
	}
	return outcome.PathFastForward
}

// opEnd emits the closing op_end event and the log's OUTCOME line.
func (s *runner) opEnd(ctx context.Context, o *outcome.Outcome) {
	p := s.p
	var words []string
	for _, w := range o.Warnings {
		words = append(words, string(w.Warning))
	}
	st := o.Stats
	if st == nil {
		st = &outcome.Stats{}
	}
	e := oplog.OpEnd{
		Outcome: string(o.Outcome), Reason: o.Reason, Retryable: o.Retryable != nil && *o.Retryable,
		Stage: p.Stage, PathTaken: o.PathTaken,
		Stats: map[string]any{
			"duration_ms": st.DurationMS, "rebased_commits": st.RebasedCommits,
			"autofix_commits": st.AutofixCommits, "conflict_rounds": st.ConflictRounds, "race_retries": st.RaceRetries,
			"target_lock_wait_ms": s.targetWait.Milliseconds(), "target_lock_held_ms": s.targetHeld.Milliseconds(),
		},
		BranchFate: o.BranchFate, Restored: o.Restored, Warnings: words,
	}
	if o.Restored == nil {
		e.Restored = []outcome.Restored{}
	}
	if o.WorktreeFate != nil {
		e.WorktreeFate = o.WorktreeFate.Fate
	}
	_ = p.Events.OpEnd(context.WithoutCancel(ctx), e)
	p.Log.Line("OUTCOME", strings.TrimSpace(string(o.Outcome)+" "+o.Reason))
}

// stop turns the operation into a stopped one: the BRANCH and worktree
// records become stopped and the TARGET record is deleted, in one
// transaction, and the outcome is exit 4 with the stop's reason and next
// commands. A lost lock goes through the lost-lock procedure instead.
func (s *runner) stop(ctx context.Context, sp *stopSpec) (*outcome.Outcome, error) {
	p := s.p
	ctx = context.WithoutCancel(ctx)
	if p.stopRenew != nil {
		p.stopRenew()
	}
	p.released = true // the records stay: a stop never deletes them
	var (
		o    *outcome.Outcome
		serr error
	)
	if sp.reason != outcome.StopLockLost {
		// conflict_rounds bounds the whole operation, so a resume needs it.
		_ = p.Op.SetAttempts(ctx, func(a *locks.Attempts) {
			a.ConflictRounds, a.RaceRetries = s.stats.ConflictRounds, s.stats.RaceRetries
		})
		serr = p.Op.Stop(ctx, oprecord.StopInfo{Reason: sp.reason, Message: sp.message, Paths: sp.paths, MovedAlready: s.moved})
		s.targetEnd() // a stop deletes the TARGET record
		if errors.Is(serr, locks.ErrLockLost) {
			// The records changed hands while this stop was written: that is
			// a lost lock (exit 4), not an internal error.
			sp = &stopSpec{reason: outcome.StopLockLost, message: serr.Error()}
			serr = nil
		}
	}
	if sp.reason == outcome.StopLockLost {
		lr, err := p.Op.HandleLostLock(ctx, oprecord.LostLock{Reason: sp.reason, Repo: p.Request.RepoFlag})
		serr = err
		if lr != nil {
			o = lr.Outcome
		}
	}
	if o == nil {
		o = outcome.NewStopped(sp.reason, string(p.Operation), p.Request.Branch, p.Stage, p.Request.RepoFlag)
		o.Message, o.Detail, o.Paths = sp.message, sp.detail, sp.paths
		if sp.logTail != "" {
			o.LogTail = strings.Split(sp.logTail, "\n")
		}
	}
	m := s.moved
	o.MovedAlready = &m
	s.fill(o)
	s.opEnd(ctx, o)
	return o, serr
}

// resumeEntry is where a resumed operation re-enters steps 8 to 13.
type resumeEntry int

const (
	// entryNormal starts at step 8 (or at the return to step 3 after a race).
	entryNormal resumeEntry = iota
	// entryContinueConflict and entrySkipConflict resume git's rebase that
	// stopped on a conflict.
	entryContinueConflict
	entrySkipConflict
	// entryRebased: the caller finished the rebase by hand; continue at
	// after_rebase.
	entryRebased
	entryAfterRebase
	entryBeforeMove
	// entryMove: the hooks passed before the stop (target-busy); take the
	// second lock phase and move without running them again.
	entryMove
	// entryAfterHook: the move happened; run the after hook, then clean up.
	entryAfterHook
	// entryCleanup runs the clean-up only.
	entryCleanup
)

// resumePlan is what --continue hands to the runner.
type resumePlan struct {
	entry resumeEntry
	// baseMoved says whether the after_rebase hook applied (for the before_move
	// hook's BASE_MOVED).
	baseMoved bool
	// moved, landed and rebased restore the facts of the move that happened.
	moved, rebased bool
	landed         string
	// nothing is the clean-up's NothingToLand input.
	nothing bool
	// conflictRounds is kept across a resume.
	conflictRounds int
}

// addWarning records a warning word once.
func (s *runner) addWarning(w outcome.Warning) {
	for _, e := range s.entries {
		if e.Warning == w {
			return
		}
	}
	s.entries = append(s.entries, outcome.WarningEntry{Warning: w})
}

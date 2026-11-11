package pipeline

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/branchmove"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/cleanup"
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
}

// Run completes the operation of steps 8 to 13 from a Prepared that holds its
// locks, and returns the outcome object. Every exit releases or stops the
// operation's records: locks are released on success, and a stop turns the
// BRANCH and worktree records to stopped and deletes the TARGET record. An
// unexpected failure is a stop with reason internal-error, never an error
// return; the error is only for a stop that itself could not be recorded.
func (p *Prepared) Run(ctx context.Context) (*outcome.Outcome, error) {
	s := &runner{p: p}
	for _, w := range p.Warnings {
		s.entries = append(s.entries, outcome.WarningEntry{Warning: w})
	}
	defer func() { _ = p.Log.Close() }()

	out, sp, err := s.run(ctx)
	switch {
	case err != nil && errors.Is(err, locks.ErrLockLost):
		sp = &stopSpec{reason: outcome.StopLockLost, message: err.Error()}
	case err != nil && ctx.Err() != nil:
		sp = &stopSpec{reason: outcome.StopInterrupted, message: ctx.Err().Error()}
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
	if p.Done() {
		return s.cleanup(ctx, true)
	}
	for {
		if sp := s.lostCheck(); sp != nil {
			return nil, sp, nil
		}
		s.baseMoved = false
		if p.Path == rebase.NeedsRebase {
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
		if s.baseMoved {
			if sp, err := s.hookPoint(ctx, hooks.PointAfterRebase, oprecord.StageAfterRebase); sp != nil || err != nil {
				return nil, sp, err
			}
		}
		if sp, err := s.hookPoint(ctx, hooks.PointBeforeMove, oprecord.StageBeforeMove); sp != nil || err != nil {
			return nil, sp, err
		}
		status, sp, err := s.move(ctx)
		if sp != nil || err != nil {
			return nil, sp, err
		}
		if status != landmove.TargetMoved {
			break
		}
		// TARGET moved since tips.target: return to step 3.
		if s.stats.RaceRetries >= p.Config.MaxRaceRetries {
			return nil, &stopSpec{
				reason:  outcome.StopRace,
				message: fmt.Sprintf("%s moved %d times while the operation ran", p.Request.Target, s.stats.RaceRetries+1),
			}, nil
		}
		old := p.Tips.Target
		if err := p.Op.Branch.Update(ctx, func(r *locks.Record) { r.Move = locks.Move{} }); err != nil {
			return nil, nil, err
		}
		if err := p.Resnapshot(ctx); err != nil {
			return nil, nil, err
		}
		s.stats.RaceRetries++
		if err := p.Events.RaceRetry(ctx, s.stats.RaceRetries, old, p.Tips.Target); err != nil {
			return nil, nil, err
		}
		cur, err := s.workTip(ctx)
		if err != nil {
			return nil, nil, err
		}
		if p.Path, err = p.Decide(ctx, cur); err != nil {
			return nil, nil, err
		}
		if p.Path == rebase.NothingToLand {
			if err := s.setResult(ctx, cur); err != nil {
				return nil, nil, err
			}
			return s.cleanup(ctx, false)
		}
	}

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

func (s *runner) lostCheck() *stopSpec {
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
	var lc []landmove.LockCheck
	for _, h := range p.Op.Set.Locks {
		lc = append(lc, landmove.LockCheck{Ref: h.Ref, Oid: h.OID()})
	}
	held := append([]*locks.Held(nil), p.Op.Set.Locks...)
	ttl := p.Layer.Config.LeaseTTL
	return landmove.Fence{Locks: lc, RenewalFresh: func() bool {
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

	rs := &conflict.Resolver{Git: p.d.Git, Hooks: s.hookRunner(), Rebase: rb, Config: p.Config, Facts: p.Facts}
	cr, err := rs.Resolve(ctx, conflict.Input{Dir: work, Rounds: s.stats.ConflictRounds, AutoMerged: res.AutoMerged, Diag: p.Diag})
	if err != nil {
		return nil, err
	}
	s.stats.ConflictRounds = cr.Rounds
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
		_ = p.Events.Hook(ctx, rec)
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
		r := mv.Move(ctx, landmove.Input{
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
	r := (&branchmove.Mover{Git: p.d.Git}).Move(ctx, branchmove.Input{
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
	if err := p.EnterStage(ctx, oprecord.StageCleanup, nil); err != nil {
		return nil, nil, err
	}
	in := cleanup.Input{
		Operation: p.Operation, NothingToLand: nothing, OpID: p.OpID, Branch: p.Request.Branch,
		Tips: p.Tips, KeepSourceWorktree: p.Request.KeepSourceWorktree, KeepSourceBranch: p.Request.KeepSourceBranch,
		Worktrees: p.Selection.Worktrees, Work: p.Selection.Work, Dir: p.Repo,
		Fence: s.fence(), Chdir: p.d.Chdir,
	}
	if p.Operation == worktrees.Land {
		in.Target = p.Request.Target
	}
	res := (&cleanup.Cleaner{Git: p.d.Git}).Run(ctx, in)
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
	if sp.reason == outcome.StopLockLost {
		lr, err := p.Op.HandleLostLock(ctx, oprecord.LostLock{Reason: sp.reason, Repo: p.Request.RepoFlag})
		serr = err
		if lr != nil {
			o = lr.Outcome
		}
	} else {
		serr = p.Op.Stop(ctx, oprecord.StopInfo{Reason: sp.reason, Message: sp.message, Paths: sp.paths, MovedAlready: s.moved})
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

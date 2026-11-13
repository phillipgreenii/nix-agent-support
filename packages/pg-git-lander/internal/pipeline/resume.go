package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/cleanup"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/conflict"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/hooks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/oplog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/oprecord"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/rebase"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/workbranch"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/worktrees"
)

// This file is the resume operations of design section 7.3: --continue,
// --skip, --abort and --quit.

// ResumeRequest is one resume invocation. The roles, keep flags, consent and
// context of the operation are read from its record, never from flags or the
// PGL_* variables.
type ResumeRequest struct {
	// Flag is outcome.FlagContinue, FlagSkip, FlagAbort or FlagQuit.
	Flag string
	// Operation is the operation word the caller typed (rebase or land); empty
	// skips the operation-mismatch check.
	Operation worktrees.Operation
	// Branch is BRANCH; empty finds the stopped operation whose work worktree
	// contains Dir (see FindStopped).
	Branch string
	// Dir is the directory git runs in: --repo, or the current directory.
	Dir string
	// RepoFlag is the --repo value, repeated in next commands; empty when the
	// invocation did not use it.
	RepoFlag string
}

// StoppedOp is a stopped operation found by FindStopped.
type StoppedOp struct {
	Operation, Branch, Target, OpID, Stage string
	// WorkWorktree is the path of the operation's work worktree.
	WorkWorktree string
	// StopReason is the reason of the stop.
	StopReason string
}

func (d Deps) layer(dir string, cfg locks.Config) *locks.Layer {
	return &locks.Layer{
		Git: d.Git, Clock: d.Clock, Dir: dir, Config: cfg, Out: d.Out, Sleep: d.Sleep, Jitter: d.Jitter,
		Interval: d.RenewInterval, Liveness: d.Liveness, Host: d.Host,
	}
}

// FindStopped finds the stopped operation of branch, or, with an empty branch,
// the stopped operation whose work worktree contains dir. It returns nil when
// there is none (the caller raises nothing-stopped). A BRANCH record that is
// running is returned too, so that the take-back reports lost-to-concurrent.
func FindStopped(ctx context.Context, d Deps, dir, branch string) (*StoppedOp, error) {
	l := d.layer(dir, locks.Config{})
	if branch != "" {
		st, err := l.Read(ctx, locks.BranchRef(branch))
		if err != nil {
			return nil, err
		}
		if st.Unreadable != nil {
			return nil, &locks.RefusedError{Ref: st.Ref, Reason: outcome.RefUnreadableLock, Cause: st.Unreadable}
		}
		if st.Absent || st.Rec.Role != locks.RoleBranch {
			return nil, nil
		}
		return stoppedOf(st.Rec), nil
	}
	r := d.Git.Run(ctx, dir, "for-each-ref", "--format=%(refname)", locks.RefPrefix+"heads/")
	if !r.OK() {
		return nil, fmt.Errorf("git for-each-ref: %s", strings.TrimSpace(string(r.Stderr)))
	}
	refs := strings.Fields(string(r.Stdout))
	sort.Strings(refs)
	for _, ref := range refs {
		st, err := l.Read(ctx, ref)
		if err != nil {
			return nil, err
		}
		if st.Rec == nil || st.Rec.Role != locks.RoleBranch || st.Rec.State != locks.StateStopped {
			continue
		}
		for _, w := range st.Rec.Worktrees {
			for _, role := range w.Roles {
				if role == string(worktrees.Work) && inside(dir, w.Path) {
					return stoppedOf(st.Rec), nil
				}
			}
		}
	}
	return nil, nil
}

func stoppedOf(r *locks.Record) *StoppedOp {
	s := &StoppedOp{Operation: r.Operation, Branch: r.Branch, Target: r.Target, OpID: r.OpID, Stage: r.Stage}
	if r.Stop != nil {
		s.StopReason = r.Stop.Reason
	}
	for _, w := range r.Worktrees {
		for _, role := range w.Roles {
			if role == string(worktrees.Work) {
				s.WorkWorktree = w.Path
			}
		}
	}
	return s
}

func inside(p, dir string) bool {
	p, dir = realPath(p), realPath(dir)
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// resumer is the working state of Resume.
type resumer struct {
	d   Deps
	req ResumeRequest
	pre *config.Pre

	resumed *oprecord.Resumed
	p       *Prepared
	// prior is the stopped BRANCH record taken from.
	prior locks.Record
	// configChanged is true when --continue loaded a different digest.
	configChanged bool
}

// Resume runs --continue, --skip, --abort or --quit. The outcome is returned
// for every result a caller prints (landed, rebased, aborted, quit, a new stop
// or a refusal); the error is only for failures that are not outcomes.
func Resume(ctx context.Context, d Deps, req ResumeRequest) (*outcome.Outcome, error) {
	rs := &resumer{d: d, req: req}
	op := string(req.Operation)

	common, err := d.Git.CommonDir(ctx, req.Dir)
	if err != nil {
		o := outcome.New(outcome.UsageError)
		o.Operation, o.Branch, o.Repo = op, req.Branch, req.RepoFlag
		o.Message = fmt.Sprintf("%s is not inside a git repository", req.Dir)
		if req.RepoFlag == "" {
			o.Message += ": resume flags take no worktree flags, so pass --repo or run from inside the repository"
		}
		return o, nil
	}
	// Resume flags need only the built-in lock defaults and the XDG file, so
	// --abort and --quit work with a broken repo file.
	loader := &config.Loader{Git: d.Git, Paths: d.Paths}
	rs.pre, err = loader.LoadXDG(ctx, req.Dir)
	if err != nil {
		var ce *config.Error
		if errors.As(err, &ce) {
			return configOutcomeFor(ce, op, req.Branch, req.RepoFlag), nil
		}
		return nil, err
	}

	if d.Wait != nil {
		rs.pre.Lock.Wait = *d.Wait
	}

	// The checks that need no lock.
	found, err := FindStopped(ctx, d, req.Dir, req.Branch)
	if err != nil {
		var ref *locks.RefusedError
		if errors.As(err, &ref) {
			return rs.refusedLock(ref, op, req.Branch), nil
		}
		return nil, err
	}
	if found == nil {
		o := outcome.NewRefused(outcome.RefNothingStopped, op, req.Branch, req.RepoFlag)
		o.Message = "no stopped operation was found"
		if req.Branch == "" {
			o.Message += " for the current directory"
		}
		return o, nil
	}
	if req.Operation != "" && string(req.Operation) != found.Operation {
		o := outcome.NewRefused(outcome.RefOperationMismatch, op, found.Branch, req.RepoFlag)
		o.Message = fmt.Sprintf("%s is a stopped %s, not a %s", found.Branch, found.Operation, req.Operation)
		o.Next = outcome.NextCommands(found.Operation, found.Branch, req.RepoFlag, []string{req.Flag})
		return o, nil
	}
	op = found.Operation

	layer := d.layer(rs.pre.Repo, locks.Config{LeaseTTL: rs.pre.Lock.LeaseTTL, Wait: rs.pre.Lock.Wait})
	// The first step of every resume flag: take the stopped BRANCH record back.
	rs.resumed, err = oprecord.TakeBack(ctx, layer, found.Branch, d.Owner)
	if err != nil {
		var ref *locks.RefusedError
		if errors.As(err, &ref) {
			return rs.refusedLock(ref, op, found.Branch), nil
		}
		return nil, err
	}
	rs.prior = rs.resumed.Stopped

	if req.Flag == outcome.FlagQuit {
		return rs.quit(ctx, common)
	}

	// The other locks, with the wait; busy swaps everything back.
	if err := rs.resumed.TakeOtherLocks(ctx); err != nil {
		var busy *locks.BusyError
		var ref *locks.RefusedError
		switch {
		case errors.As(err, &busy):
			o := busy.Outcome(op, found.Branch, rs.prior.Target)
			o.Message = busy.Error()
			return o, nil
		case errors.As(err, &ref):
			return rs.refusedLock(ref, op, found.Branch), nil
		}
		return nil, err
	}
	if err := rs.build(ctx, common); err != nil {
		_ = rs.giveBack(ctx)
		return nil, err
	}

	switch req.Flag {
	case outcome.FlagAbort:
		return rs.abort(ctx)
	case outcome.FlagContinue, outcome.FlagSkip:
		return rs.resume(ctx, req.Flag == outcome.FlagSkip)
	}
	_ = rs.giveBack(ctx)
	o := outcome.New(outcome.UsageError)
	o.Message = fmt.Sprintf("unknown resume flag %q", req.Flag)
	return o, nil
}

func configOutcomeFor(e *config.Error, op, branch, repo string) *outcome.Outcome {
	var o *outcome.Outcome
	if e.Word == outcome.Refused {
		o = outcome.NewRefused(e.Reason, op, branch, repo)
	} else {
		o = outcome.New(e.Word)
		o.Operation, o.Branch, o.Repo = op, branch, repo
	}
	o.Message = e.Message
	if e.Hint != "" {
		o.Hint = e.Hint
	}
	if e.Loc != nil {
		loc := *e.Loc
		o.ConfigError, o.Path, o.Detail = &loc, loc.File, e.Error()
	}
	return o
}

func (rs *resumer) refusedLock(ref *locks.RefusedError, op, branch string) *outcome.Outcome {
	o := ref.Outcome(op, branch, rs.req.RepoFlag)
	o.Message = ref.Error()
	if h := ref.Holder; h.OpID != "" {
		o.Holder = &h
	}
	return o
}

// giveBack stops renewal and swaps every record this resume took back to its
// stopped state (and releases the locks it took new), so that a resume that
// cannot proceed leaves the stopped record unchanged.
func (rs *resumer) giveBack(ctx context.Context) error {
	ctx = context.WithoutCancel(ctx)
	if rs.p != nil {
		if rs.p.stopRenew != nil {
			rs.p.stopRenew()
			rs.p.stopRenew = nil
		}
		rs.p.released = true
		defer func() { _ = rs.p.Log.Close() }()
	}
	return rs.resumed.SwapBack(ctx)
}

// refuse swaps the records back and returns the refusal.
func (rs *resumer) refuse(ctx context.Context, o *outcome.Outcome) (*outcome.Outcome, error) {
	if err := rs.giveBack(ctx); err != nil {
		return o, err
	}
	rs.finish(ctx, o)
	return o, nil
}

// finish emits the closing log line of a refusal.
func (rs *resumer) finish(_ context.Context, o *outcome.Outcome) {
	if rs.p != nil {
		o.OpID, o.Log = rs.p.OpID, rs.p.Log.Path
	}
}

func (rs *resumer) cannotResume(detail, format string, a ...any) *outcome.Outcome {
	rec := rs.prior
	o := outcome.NewRefused(outcome.RefCannotResume, rec.Operation, rec.Branch, rs.req.RepoFlag)
	o.Detail, o.Message = detail, fmt.Sprintf(format, a...)
	o.Stage = rec.Stage
	flags := []string{outcome.FlagAbort, outcome.FlagQuit}
	if detail == outcome.DetailUnresolved || detail == outcome.DetailMarkersLeft {
		flags = []string{outcome.FlagContinue, outcome.FlagAbort, outcome.FlagQuit}
	}
	o.Next = outcome.NextCommands(rec.Operation, rec.Branch, rs.req.RepoFlag, flags)
	return o
}

func contextPairs(m map[string]any) []hooks.ContextPair {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []hooks.ContextPair
	for _, k := range keys {
		out = append(out, hooks.ContextPair{Key: k, Value: fmt.Sprint(m[k])})
	}
	return out
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func tipsOf(t locks.Tips) outcome.Tips {
	return outcome.Tips{Branch: deref(t.Branch), Target: deref(t.Target), Onto: deref(t.Onto), Result: deref(t.Result)}
}

// selectionOf rebuilds the worktree selection from the record: the roles and
// the starting states are the recorded ones, not the present ones.
func selectionOf(rec locks.Record) *worktrees.Selection {
	sel := &worktrees.Selection{SourceExplicit: rec.Keep.RemovalConsent}
	for _, w := range rec.Worktrees {
		c := &worktrees.Chosen{Path: w.Path, AdminName: w.Admin, IsMain: w.Admin == locks.MainWorktreeAdm}
		if w.Start.Detached != "" {
			c.Start, c.StartDetached = w.Start.Detached, true
		} else {
			c.Start = w.Start.Branch
		}
		c.OnBranch = !c.StartDetached && c.Start == rec.Branch
		for _, r := range w.Roles {
			role := worktrees.Role(r)
			c.Roles = append(c.Roles, role)
			switch role {
			case worktrees.Source:
				sel.Source = c
			case worktrees.Destination:
				sel.Destination = c
			case worktrees.Work:
				sel.Work = c
			}
		}
		sel.Worktrees = append(sel.Worktrees, c)
		if rec.Operation == oprecord.OperationLand && !c.StartDetached && c.Start == rec.Target {
			sel.TargetCheckedOut = true
		}
	}
	sel.DestinationFallback = sel.Destination != nil && !sel.TargetCheckedOut
	return sel
}

// build assembles the Prepared operation a resume works on from the taken-back
// record. It starts the lease renewal.
func (rs *resumer) build(ctx context.Context, common string) error {
	d, rec := rs.d, rs.resumed.Op.Record()
	sel := selectionOf(rec)
	if sel.Work == nil || sel.Source == nil {
		return fmt.Errorf("the record of %s names no work or source worktree", rec.Branch)
	}
	target := rec.Target
	if rec.Operation != oprecord.OperationLand {
		target = ""
	}
	p := &Prepared{
		Request: Request{
			Operation: worktrees.Operation(rec.Operation), Branch: rec.Branch, Target: rec.Target,
			Onto: deref(rec.Tips.Onto), Dir: rs.req.Dir, RepoFlag: rs.req.RepoFlag,
			KeepSourceWorktree: rec.Keep.SourceWorktree, KeepSourceBranch: rec.Keep.SourceBranch,
			Context: contextPairs(rec.Context),
		},
		Operation: worktrees.Operation(rec.Operation), OpID: rec.OpID, Repo: rs.pre.Repo, CommonDir: common,
		Selection: sel, Layer: rs.resumed.Op.Layer, Op: rs.resumed.Op, Pre: rs.pre,
		Tips: tipsOf(rec.Tips), WorkBranch: rec.WorkBranch, Path: rebase.FastForward,
		Started: rec.StartedAt, Stage: rec.Stage, StageStarted: d.Clock.Now(),
		d: d, opStarted: true,
	}
	var err error
	if p.Log, err = oplog.Open(common, rec.OpID, rec.Branch, target, d.Clock); err != nil {
		return err
	}
	p.Log.Line("RESUME", rs.req.Flag+" at stage "+rec.Stage)
	p.Events = &oplog.Events{
		Path: oplog.EventsPath(common), Clock: d.Clock, Owner: d.Owner,
		Common: oplog.Common{
			Repo: p.Repo, Host: d.Owner.Host, PID: d.Owner.PID, ToolVersion: d.Owner.ToolVersion,
			OpID: rec.OpID, Operation: rec.Operation, Branch: rec.Branch, Target: target,
		},
	}
	p.Events.Locks = p.Layer
	oplog.Attach(p.Layer, p.Log, p.Events)
	p.Facts = hooks.Facts{
		Operation: rec.Operation, OpID: rec.OpID, Repo: p.Repo, GitCommonDir: common,
		WorkWorktree: sel.Work.Path, SourceWorktree: sel.Source.Path, Branch: rec.Branch,
		BranchTip: p.Tips.Branch, WorkBranch: rec.WorkBranch, Result: p.Tips.Result, Context: p.Request.Context,
	}
	if sel.Destination != nil {
		p.Facts.DestinationWorktree = sel.Destination.Path
	}
	if rec.Operation == oprecord.OperationLand {
		p.Facts.Target, p.Facts.TargetTip, p.Facts.Onto = rec.Target, p.Tips.Target, p.Tips.Target
	} else {
		p.Facts.Onto = p.Tips.Onto
	}
	rs.p = p
	p.startRenewal(ctx)
	return nil
}

// stageIndex is the position of a stage in the stage table.
func stageIndex(stage string) int {
	for i, s := range oprecord.Stages {
		if s == stage {
			return i
		}
	}
	return -1
}

func (rs *resumer) git(ctx context.Context, dir string, args ...string) gitexec.Result {
	return rs.d.Git.Run(ctx, dir, args...)
}

// refTip returns the oid ref names, or "" when it does not exist.
func (rs *resumer) refTip(ctx context.Context, ref string) string {
	r := rs.git(ctx, rs.p.Repo, "rev-parse", "--verify", "-q", ref+"^{commit}")
	if !r.OK() {
		return ""
	}
	return strings.TrimSpace(string(r.Stdout))
}

func (rs *resumer) isAncestor(ctx context.Context, anc, desc string) bool {
	return rs.git(ctx, rs.p.Repo, "merge-base", "--is-ancestor", anc, desc).OK()
}

// moved reports whether the move already happened: the record holds a move
// intent and the result is in place (TARGET holds it for a land, BRANCH equals
// it for a rebase).
func (rs *resumer) moved(ctx context.Context, rec locks.Record) (bool, string) {
	if rec.Move.Intent == nil || rec.Move.ExpectedResult == nil {
		return false, ""
	}
	exp := *rec.Move.ExpectedResult
	if rec.Operation == oprecord.OperationLand {
		if t := rs.refTip(ctx, "refs/heads/"+rec.Target); t != "" && (t == exp || rs.isAncestor(ctx, exp, t)) {
			return true, exp
		}
		return false, exp
	}
	return rs.refTip(ctx, "refs/heads/"+rec.Branch) == exp, exp
}

func (rs *resumer) workDir() string { return rs.p.Selection.Work.Path }

func (rs *resumer) listed(ctx context.Context) ([]gitexec.Worktree, error) {
	return rs.d.Git.Worktrees(ctx, rs.p.Repo)
}

func findListed(wts []gitexec.Worktree, path string) *gitexec.Worktree {
	for i := range wts {
		if realPath(wts[i].Path) == realPath(path) {
			return &wts[i]
		}
	}
	return nil
}

func gone(w *gitexec.Worktree) bool {
	if w == nil || w.Prunable {
		return true
	}
	_, err := os.Stat(w.Path)
	return err != nil
}

// checkRoles re-runs the role checks of section 11.1 against the recorded
// roles and verifies that the work worktree is the recorded one. It returns a
// refusal, or nil.
func (rs *resumer) checkRoles(ctx context.Context, rec locks.Record) (*outcome.Outcome, error) {
	p := rs.p
	wts, err := rs.listed(ctx)
	if err != nil {
		return nil, err
	}
	sel := p.Selection
	late := stageIndex(rec.Stage) >= stageIndex(oprecord.StageAfter)
	sx := &worktrees.Selector{Git: rs.d.Git}

	ww := findListed(wts, sel.Work.Path)
	if gone(ww) {
		if !late {
			return rs.cannotResume(outcome.DetailWorktreeMismatch, "the work worktree %s is missing", sel.Work.Path), nil
		}
	} else if name, err := sx.AdminName(ctx, sel.Work.Path, sel.Work.IsMain); err != nil {
		return nil, err
	} else if name != sel.Work.AdminName {
		return rs.cannotResume(outcome.DetailWorktreeMismatch, "the work worktree %s is %q, the record names %q", sel.Work.Path, name, sel.Work.AdminName), nil
	}

	// A worktree that is already gone counts as done at the after stage or
	// later; before it, the move needs the destination.
	for _, c := range sel.Worktrees {
		if c == sel.Work || (c.Path == sel.Work.Path) {
			continue
		}
		if gone(findListed(wts, c.Path)) {
			continue
		}
		if r, err := sx.CheckClean(ctx, c, nil); err != nil {
			return nil, err
		} else if r != nil {
			o := outcome.NewRefused(r.Reason, rec.Operation, rec.Branch, rs.req.RepoFlag)
			o.Message, o.Detail, o.Role, o.Path = r.Message, r.Detail, string(r.Role), r.Path
			return o, nil
		}
	}
	return nil, nil
}

// reloadConfig loads the repo configuration again (as step 3 does) and
// records its digest; a different digest is the warning config-changed.
func (rs *resumer) reloadConfig(ctx context.Context, rec locks.Record) (*outcome.Outcome, error) {
	p := rs.p
	ref := ""
	if p.Operation == worktrees.Land {
		ref = p.Tips.Target
	} else {
		ref = p.Pre.RebaseTip(ctx)
		if rs.refTip(ctx, ref) == "" {
			ref = "" // no local primary branch: no repo file
		}
	}
	cfg, err := p.Pre.Load(ctx, ref)
	if err != nil {
		var ce *config.Error
		if errors.As(err, &ce) {
			return configOutcomeFor(ce, rec.Operation, rec.Branch, rs.req.RepoFlag), nil
		}
		return nil, err
	}
	p.Config = cfg
	if rec.ConfigDigest != "" && rec.ConfigDigest != cfg.Digest {
		rs.configChanged = true
		p.Warnings = append(p.Warnings, outcome.WarnConfigChanged)
	}
	if err := p.Op.SetConfigDigest(ctx, cfg.Digest); err != nil {
		return nil, err
	}
	return nil, nil
}

// markersLeft lists the paths whose staged (or committed) content still holds
// conflict markers.
func (rs *resumer) markersLeft(ctx context.Context, stopPaths []string) ([]string, error) {
	dir := rs.workDir()
	r := rs.git(ctx, dir, "diff", "--cached", "--name-only", "-z")
	if !r.OK() {
		return nil, fmt.Errorf("git diff --cached: %s", strings.TrimSpace(string(r.Stderr)))
	}
	seen := map[string]bool{}
	var paths []string
	for _, p := range append(strings.Split(string(r.Stdout), "\x00"), stopPaths...) {
		if p != "" && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	var bad []string
	for _, p := range paths {
		b := rs.git(ctx, dir, "cat-file", "blob", ":"+p)
		if b.OK() && conflict.HasMarkers(b.Stdout) {
			bad = append(bad, p)
		}
	}
	return bad, nil
}

// resume is --continue and --skip.
func (rs *resumer) resume(ctx context.Context, skip bool) (*outcome.Outcome, error) {
	p := rs.p
	rec := p.Op.Record()
	rb := &rebase.Rebaser{Git: rs.d.Git}

	if o, err := rs.reloadConfig(ctx, rec); o != nil || err != nil {
		return rs.failed(ctx, o, err)
	}
	if o, err := rs.checkRoles(ctx, rec); o != nil || err != nil {
		return rs.failed(ctx, o, err)
	}
	work := rs.workDir()
	if gone(findListedOrNil(rs, ctx, work)) {
		// Only possible at the after stage or later (checkRoles refused it
		// before): no hook can run in a work worktree that is gone, so the
		// record cleanup finishes.
		done, exp := rs.moved(ctx, rec)
		done = done || rec.Move.Intent != nil
		return rs.enter(ctx, rec, &resumePlan{entry: entryCleanup, moved: done, landed: exp, conflictRounds: rec.Attempts.ConflictRounds})
	}
	diag, err := rb.Settings(ctx, work)
	if err != nil {
		return rs.failed(ctx, nil, err)
	}
	p.Diag = diag

	inProgress, err := rb.InProgress(ctx, work)
	if err != nil {
		return rs.failed(ctx, nil, err)
	}
	plan := &resumePlan{conflictRounds: rec.Attempts.ConflictRounds}
	stage := rec.Stage
	if skip && (stage != oprecord.StageRebasing || !inProgress) {
		return rs.failed(ctx, rs.cannotResume(outcome.DetailRebaseStateMissing,
			"--skip is valid only at stage rebasing with git's rebase in progress (stage %s)", stage), nil)
	}
	base := p.Tips.Target
	if p.Operation == worktrees.Rebase {
		base = p.Tips.Onto
	}
	branchTip := p.Tips.Branch
	baseMoved := p.Tips.Result != "" && p.Tips.Result != branchTip
	workTip := rs.refTip(ctx, "refs/heads/"+p.WorkBranch)
	onWorkBranch := func() bool {
		r := rs.git(ctx, work, "symbolic-ref", "--short", "-q", "HEAD")
		return r.OK() && strings.TrimSpace(string(r.Stdout)) == p.WorkBranch
	}
	mismatch := func(format string, a ...any) (*outcome.Outcome, error) {
		return rs.failed(ctx, rs.cannotResume(outcome.DetailWorktreeMismatch, format, a...), nil)
	}

	switch stage {
	case oprecord.StageLocked, oprecord.StagePreparing:
		return rs.restart(ctx, rec, stage == oprecord.StagePreparing)

	case oprecord.StageRebasing:
		switch {
		case inProgress && skip:
			plan.entry = entrySkipConflict
		case inProgress:
			paths, err := rb.Unmerged(ctx, work)
			if err != nil {
				return rs.failed(ctx, nil, err)
			}
			if len(paths) > 0 {
				o := rs.cannotResume(outcome.DetailUnresolved, "unmerged paths remain in %s: %s", work, strings.Join(paths, ", "))
				o.Paths = paths
				return rs.failed(ctx, o, nil)
			}
			var stopPaths []string
			if rs.prior.Stop != nil {
				stopPaths = rs.prior.Stop.Paths
			}
			bad, err := rs.markersLeft(ctx, stopPaths)
			if err != nil {
				return rs.failed(ctx, nil, err)
			}
			if len(bad) > 0 {
				o := rs.cannotResume(outcome.DetailMarkersLeft, "a resolution still holds conflict markers: %s", strings.Join(bad, ", "))
				o.Paths = bad
				return rs.failed(ctx, o, nil)
			}
			plan.entry = entryContinueConflict
		case workTip == "":
			return rs.failed(ctx, rs.cannotResume(outcome.DetailRebaseStateMissing, "the work branch %s does not exist", p.WorkBranch), nil)
		case workTip == branchTip:
			// The rebase never started or was aborted by hand: start it again.
			p.Path = rebase.NeedsRebase
			plan.entry = entryNormal
		case base != "" && rs.isAncestor(ctx, base, workTip):
			// The caller finished the rebase with git by hand.
			if err := p.Op.SetTips(ctx, func(t *locks.Tips) { v := workTip; t.Result = &v }); err != nil {
				return rs.failed(ctx, nil, err)
			}
			p.Tips.Result, p.Facts.Result = workTip, workTip
			plan.entry = entryRebased
		default:
			return rs.failed(ctx, rs.cannotResume(outcome.DetailRebaseStateMissing,
				"git's rebase is neither in progress nor finished in a way the record can account for"), nil)
		}

	case oprecord.StageAfterRebase, oprecord.StageBeforeMove:
		if inProgress || workTip == "" || !onWorkBranch() {
			return mismatch("the work worktree %s is not on the work branch %s", work, p.WorkBranch)
		}
		if p.Tips.Result == "" || !rs.isAncestor(ctx, p.Tips.Result, workTip) {
			return mismatch("the work branch tip %s is not the recorded result %s or a descendant", workTip, p.Tips.Result)
		}
		plan.entry, plan.baseMoved = entryAfterRebase, true
		if stage == oprecord.StageBeforeMove {
			plan.entry, plan.baseMoved = entryBeforeMove, baseMoved
		}

	case oprecord.StageMoving:
		done, exp := rs.moved(ctx, rec)
		if done {
			plan.entry, plan.landed, plan.rebased = entryAfterHook, exp, baseMoved
			break
		}
		if inProgress || workTip == "" || !onWorkBranch() {
			return mismatch("the work worktree %s is not on the work branch %s", work, p.WorkBranch)
		}
		if p.Tips.Result == "" || !rs.isAncestor(ctx, p.Tips.Result, workTip) {
			return mismatch("the work branch tip %s is not the recorded result %s or a descendant", workTip, p.Tips.Result)
		}
		// Back to step 3 with the work branch tip as the content to land.
		if err := p.Op.Branch.Update(ctx, func(r *locks.Record) { r.Move = locks.Move{} }); err != nil {
			return rs.failed(ctx, nil, err)
		}
		if err := p.Resnapshot(ctx); err != nil {
			var ce *config.Error
			if errors.As(err, &ce) {
				return rs.failed(ctx, configOutcomeFor(ce, rec.Operation, rec.Branch, rs.req.RepoFlag), nil)
			}
			return rs.failed(ctx, nil, err)
		}
		path, err := p.Decide(ctx, workTip)
		if err != nil {
			return rs.failed(ctx, nil, err)
		}
		switch {
		case path == rebase.UpToDate:
			path = rebase.FastForward // a rebase whose result is already on top of ONTO still has to move BRANCH
		case path == rebase.NothingToLand:
			if err := p.Op.SetTips(ctx, func(t *locks.Tips) { v := workTip; t.Result = &v }); err != nil {
				return rs.failed(ctx, nil, err)
			}
			p.Tips.Result, p.Facts.Result = workTip, workTip
			plan.entry = entryCleanup
		}
		p.Path = path
		if plan.entry != entryCleanup {
			plan.entry = entryNormal
		}

	case oprecord.StageAfter:
		_, exp := rs.moved(ctx, rec)
		if exp == "" {
			exp = p.Tips.Result
		}
		plan.entry, plan.landed, plan.rebased = entryAfterHook, exp, baseMoved

	case oprecord.StageCleanup:
		done, exp := rs.moved(ctx, rec)
		if rec.Move.Intent != nil {
			done = true // the move was written and the stage moved on: it happened
		}
		plan.entry, plan.moved, plan.landed, plan.rebased = entryCleanup, done, exp, done && baseMoved
		plan.nothing = !done && (p.Tips.Result == "" || p.Tips.Result == branchTip)

	default:
		return rs.failed(ctx, rs.cannotResume(outcome.DetailRebaseStateMissing, "unknown stage %q", stage), nil)
	}
	return rs.enter(ctx, rec, plan)
}

func findListedOrNil(rs *resumer, ctx context.Context, path string) *gitexec.Worktree {
	wts, err := rs.listed(ctx)
	if err != nil {
		return nil
	}
	return findListed(wts, path)
}

// failed ends a resume that cannot proceed: the records go back to stopped. A
// non-nil o is the refusal; err an unexpected failure.
func (rs *resumer) failed(ctx context.Context, o *outcome.Outcome, err error) (*outcome.Outcome, error) {
	if o == nil {
		_ = rs.giveBack(ctx)
		return nil, err
	}
	return rs.refuse(ctx, o)
}

// enter runs the rest of the operation from plan.
func (rs *resumer) enter(ctx context.Context, rec locks.Record, plan *resumePlan) (*outcome.Outcome, error) {
	p := rs.p
	p.plan = plan
	if plan.entry == entryAfterHook || plan.entry == entryCleanup || plan.entry == entryContinueConflict ||
		plan.entry == entrySkipConflict || plan.entry == entryRebased || plan.entry == entryAfterRebase || plan.entry == entryBeforeMove {
		// A stage entered again: the counters of a race and of autofix start over.
		if err := p.Op.SetAttempts(ctx, func(a *locks.Attempts) { a.RaceRetries, a.Autofix = 0, map[string]int{} }); err != nil {
			return rs.failed(ctx, nil, err)
		}
	}
	out, err := p.Run(ctx)
	if out != nil && rs.configChanged {
		t := true
		out.ConfigChanged = &t
	}
	return out, err
}

// restart resumes at stage locked or preparing: steps 3 to 7 run again (at
// preparing, steps 6 and 7, after the removal of a half-created work branch).
func (rs *resumer) restart(ctx context.Context, rec locks.Record, preparing bool) (*outcome.Outcome, error) {
	p := rs.p
	s := &session{
		d: rs.d, req: p.Request, p: p, pre: rs.pre, onto: p.Tips.Onto, resumed: true,
		tco: p.Selection.TargetCheckedOut,
	}
	if !preparing {
		s.initFacts()
	}
	br := &workbranch.Brancher{Git: rs.d.Git}
	if preparing {
		if wts, err := rs.listed(ctx); err == nil {
			if w := findListed(wts, p.Selection.Work.Path); !gone(w) && w.Branch == "refs/heads/"+p.WorkBranch {
				if err := br.Restore(ctx, p.Selection.Work); err != nil {
					return rs.failed(ctx, nil, err)
				}
			}
		}
		if err := br.Delete(ctx, p.Repo, p.OpID); err != nil {
			return rs.failed(ctx, nil, err)
		}
	}
	var res *Result
	var err error
	if preparing {
		if p.Tips.Branch == "" {
			return rs.failed(ctx, rs.cannotResume(outcome.DetailRebaseStateMissing, "the record holds no BRANCH tip"), nil)
		}
		diag, derr := (&rebase.Rebaser{Git: rs.d.Git}).Settings(ctx, p.Selection.Work.Path)
		if derr != nil {
			return rs.failed(ctx, nil, derr)
		}
		p.Diag = diag
		res, err = s.prepare(ctx)
	} else {
		res, err = s.stepsThreeToFive(ctx)
	}
	if err != nil {
		return rs.failed(ctx, nil, err)
	}
	if res.Prepared == nil {
		// A refusal after the take-back (the before hook, the work branch
		// switch): like a fresh run, the records are released.
		if p.stopRenew != nil {
			p.stopRenew()
		}
		_ = p.Release(ctx)
		_ = p.Log.Close()
		p.released = true
		if res.Outcome != nil {
			res.Outcome.OpID, res.Outcome.Log = p.OpID, p.Log.Path
		}
		return res.Outcome, nil
	}
	return rs.enter(ctx, rec, &resumePlan{conflictRounds: rec.Attempts.ConflictRounds})
}

// ---- --abort ----

func (rs *resumer) abort(ctx context.Context) (*outcome.Outcome, error) {
	p := rs.p
	rec := p.Op.Record()
	s := &runner{p: p}
	var o *outcome.Outcome
	if done, exp := rs.moved(ctx, rec); done {
		// The move happened: only finish the clean-up of section 7.2.
		s.moved, s.landed = true, exp
		res := (&cleanup.Cleaner{Git: rs.d.Git}).Run(ctx, s.cleanupInput(false))
		s.cres = &res
		if res.Stop != nil {
			out, err := s.stop(ctx, &stopSpec{reason: res.Stop.Reason, message: "the clean-up stopped", detail: res.Stop.Detail})
			_ = p.Log.Close()
			return out, err
		}
		for _, w := range res.Warnings {
			s.entries = append(s.entries, outcome.WarningEntry{Warning: w.Word, Detail: w.Detail})
		}
		o = outcome.New(outcome.Aborted)
		s.fill(o)
		t := true
		o.MovedAlready = &t
		o.Message = "the move had already happened; only the clean-up was finished"
	} else {
		var err error
		o, err = rs.undo(ctx, s)
		if err != nil || o.Outcome == outcome.Stopped {
			return o, err
		}
	}
	if p.stopRenew != nil {
		p.stopRenew()
		p.stopRenew = nil
	}
	if err := p.Release(ctx); err != nil {
		return o, err
	}
	s.opEnd(ctx, o)
	_ = p.Log.Close()
	return o, nil
}

// undo is --abort before the move: git's rebase is aborted, every worktree
// goes back to its recorded starting state, the work branch is deleted and the
// records are deleted. BRANCH and TARGET are not touched.
func (rs *resumer) undo(ctx context.Context, s *runner) (*outcome.Outcome, error) {
	p := rs.p
	sel := p.Selection
	wts, err := rs.listed(ctx)
	if err != nil {
		return rs.stopAbort(ctx, s, err)
	}
	missing := map[string]bool{}
	var restored []outcome.Restored
	rb := &rebase.Rebaser{Git: rs.d.Git}
	for _, c := range sel.Worktrees {
		if gone(findListed(wts, c.Path)) {
			missing[c.Path] = true
			// Forget the vanished worktree, as git worktree prune does for it
			// alone: git refuses to delete a branch a registered worktree has.
			if !c.IsMain {
				_ = os.RemoveAll(filepath.Join(p.CommonDir, "worktrees", c.AdminName))
			}
			continue
		}
		if in, err := rb.InProgress(ctx, c.Path); err == nil && in {
			if r := rs.git(ctx, c.Path, "rebase", "--abort"); !r.OK() {
				return rs.stopAbort(ctx, s, fmt.Errorf("git rebase --abort in %s: %s", c.Path, strings.TrimSpace(string(r.Stderr))))
			}
		}
		args := []string{"switch", "--discard-changes"}
		if c.StartDetached {
			args = append(args, "--detach")
		}
		args = append(args, c.Start)
		if r := rs.git(ctx, c.Path, args...); !r.OK() {
			return rs.stopAbort(ctx, s, fmt.Errorf("restoring %s to %s: %s", c.Path, c.Start, strings.TrimSpace(string(r.Stderr))))
		}
		restored = append(restored, outcome.Restored{Worktree: c.Path, State: c.Start})
	}
	if err := (&workbranch.Brancher{Git: rs.d.Git}).Delete(ctx, p.Repo, p.OpID); err != nil {
		return rs.stopAbort(ctx, s, fmt.Errorf("deleting the work branch: %w", err))
	}
	o := outcome.New(outcome.Aborted)
	s.fill(o)
	f := false
	o.MovedAlready = &f
	o.Restored = restored
	o.Message = "the operation was undone; BRANCH and TARGET are where they were"
	for _, w := range []*outcome.Worktree{o.SourceWorktree, o.DestinationWorktree, o.WorkWorktree} {
		if w != nil && missing[w.Path] {
			w.Missing = true
		}
	}
	return o, nil
}

// stopAbort turns a failed abort into a stop (git-error: the next valid
// commands are --abort and --quit).
func (rs *resumer) stopAbort(ctx context.Context, s *runner, err error) (*outcome.Outcome, error) {
	out, serr := s.stop(ctx, &stopSpec{reason: outcome.StopGitError, message: err.Error()})
	_ = rs.p.Log.Close()
	rs.p.released = true
	if serr != nil {
		return out, serr
	}
	return out, nil
}

// ---- --quit ----

func (rs *resumer) quit(ctx context.Context, common string) (*outcome.Outcome, error) {
	d, rec := rs.d, rs.resumed.Op.Record()
	target := rec.Target
	if rec.Operation != oprecord.OperationLand {
		target = ""
	}
	log, err := oplog.Open(common, rec.OpID, rec.Branch, target, d.Clock)
	if err != nil {
		_ = rs.resumed.SwapBack(context.WithoutCancel(ctx))
		return nil, err
	}
	defer func() { _ = log.Close() }()
	log.Line("RESUME", "--quit at stage "+rec.Stage)
	if err := rs.resumed.Quit(ctx); err != nil {
		return nil, err
	}
	work := ""
	for _, w := range rec.Worktrees {
		for _, r := range w.Roles {
			if r == string(worktrees.Work) {
				work = w.Path
			}
		}
	}
	o := outcome.New(outcome.Quit)
	o.OpID, o.Log = rec.OpID, log.Path
	o.Operation, o.Branch, o.Target, o.Repo = rec.Operation, rec.Branch, target, rs.req.RepoFlag
	o.Stage = rec.Stage
	o.WorkBranch = rec.WorkBranch
	o.WorkWorktree = &outcome.Worktree{Name: adminOf(rec, work), Path: work}
	t := tipsOf(rec.Tips)
	o.Tips = &t
	o.Message = "the tool's records are deleted; the worktrees and the work branch are left as they are"
	o.Hint = fmt.Sprintf("Finish with git in %s: run `git rebase --continue` or `git rebase --abort` there; "+
		"to put %s on the result, run `git switch -C %s %s`. Delete the work branch %s when you are done.",
		work, rec.Branch, rec.Branch, rec.WorkBranch, rec.WorkBranch)
	ev := &oplog.Events{
		Path: oplog.EventsPath(common), Clock: d.Clock, Owner: d.Owner,
		Common: oplog.Common{
			Repo: rs.pre.Repo, Host: d.Owner.Host, PID: d.Owner.PID, ToolVersion: d.Owner.ToolVersion,
			OpID: rec.OpID, Operation: rec.Operation, Branch: rec.Branch, Target: target,
		},
		Locks: rs.resumed.Op.Layer,
	}
	_ = ev.OpEnd(context.WithoutCancel(ctx), oplog.OpEnd{
		Outcome: string(o.Outcome), Stage: rec.Stage, Restored: []outcome.Restored{},
		Stats: map[string]any{}, Warnings: []string{},
	})
	log.Line("OUTCOME", "quit")
	return o, nil
}

func adminOf(rec locks.Record, path string) string {
	for _, w := range rec.Worktrees {
		if w.Path == path {
			return w.Admin
		}
	}
	return ""
}

// LayerFor is a lock layer over dir with the lock defaults, for the callers
// that only read records (the log pruning, status).
func LayerFor(d Deps, dir string) *locks.Layer { return d.layer(dir, locks.Config{}) }

// Package pipeline runs steps 1 to 7 of the pg-git-lander pipeline for both
// operations: resolve (step 1), lock (2), snapshot and configure (3), check the
// preconditions (4), run the before hook (5), prepare the work branch (6) and
// decide the path (7), design section 7.1.
//
// Start returns either a final outcome (a usage error, busy, a refusal; every
// lock released) or a Prepared operation that still holds its locks and is
// ready for the rebase and move steps. A land that has nothing to land and a
// rebase that is already up to date also return a Prepared, with the base
// outcome in Result.Outcome, because step 13 (clean-up) still has to run
// under the locks.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/cleanup"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/hooks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/oplog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/oprecord"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/rebase"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/workbranch"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/worktrees"
)

// maxReselect bounds how often the lock step starts over because the
// worktree selection changed between the first selection and the moment the
// locks were held.
const maxReselect = 5

// Deps are the seams and process facts the pipeline needs.
type Deps struct {
	Git      *gitexec.Git
	Clock    seams.Clock
	Liveness seams.Liveness
	Host     seams.Host
	Paths    seams.ConfigPaths
	// Owner identifies this process in the lock records it writes.
	Owner locks.Owner
	// Out receives the lock layer's waiting messages; nil discards them.
	Out io.Writer
	// Rand is the entropy of the op id; nil is crypto/rand.
	Rand io.Reader
	// Sleep, Jitter and RenewInterval are test seams of the lock layer.
	Sleep         func(ctx context.Context, d time.Duration) error
	Jitter        func(max time.Duration) time.Duration
	RenewInterval time.Duration
	// Chdir changes the process working directory for the clean-up (step 13);
	// nil is os.Chdir. Tests that must not move the test process inject a
	// no-op.
	Chdir func(string) error
}

// Request is one operation, with its flags already resolved against the PGL_*
// variables by the CLI.
type Request struct {
	Operation worktrees.Operation
	Branch    string
	// Target is a land's TARGET; Onto is a rebase's REF (resolved once).
	Target string
	Onto   string
	// Dir is the directory git runs in and the cwd of the worktree cascade:
	// --repo, or the current directory.
	Dir string
	// RepoFlag is the --repo value, repeated in next commands; empty when
	// the invocation did not use it.
	RepoFlag string

	TargetWorktree, SourceWorktree, WorkWorktree string
	KeepSourceWorktree, KeepSourceBranch         bool
	Context                                      []hooks.ContextPair
}

// Result is what Start returns.
type Result struct {
	// Prepared is non-nil when the operation still holds its locks: it
	// continues (Path is fast-forward or rebase) or only needs clean-up
	// (nothing-to-land, up-to-date).
	Prepared *Prepared
	// Outcome is set when the outcome is known: always when Prepared is nil
	// (every lock is then released and the log closed), and for
	// nothing-to-land and up-to-date as the outcome to complete at step 13.
	Outcome *outcome.Outcome
}

// Prepared is an operation that has finished step 7. It holds every lock.
type Prepared struct {
	Request   Request
	Operation worktrees.Operation
	OpID      string
	// Repo is the main worktree; CommonDir the git common dir.
	Repo, CommonDir string

	Selection *worktrees.Selection
	Layer     *locks.Layer
	Op        *oprecord.Op
	Log       *oplog.Log
	Events    *oplog.Events
	Pre       *config.Pre
	Config    *config.Config
	// Tips holds tips.branch, tips.target (land) and tips.onto (rebase) as
	// snapshotted; Result is filled by the later steps.
	Tips       outcome.Tips
	WorkBranch string
	// Path is the decided path of step 7.
	Path rebase.Path
	// Facts is the command environment of section 13.2; it is final for the
	// before hook and gets Result and BaseMoved from the later steps.
	Facts hooks.Facts
	// Warnings are the warning words so far (for example work-in-destination).
	Warnings     []outcome.Warning
	SkippedHooks []hooks.Skipped

	// Diag holds the merge-driver diagnostics of step 4, for the conflict loop.
	Diag *rebase.Diagnostics

	Started time.Time
	// Stage and StageStarted say where the operation is, for the next stage
	// event's previous_stage and duration_ms.
	Stage        string
	StageStarted time.Time
	// Lost receives the error of a lease renewal that failed (lock-lost).
	Lost <-chan error

	d         Deps
	stopRenew func()
	opStarted bool
	released  bool
}

// Done reports whether the path needs no rebase and no move.
func (p *Prepared) Done() bool {
	return p.Path == rebase.NothingToLand || p.Path == rebase.UpToDate
}

// EnterStage records a stage boundary: the record's stage, stage_started_at
// and (when result is non-nil) tips.result, the stage event with the previous
// stage and its duration, and the in-memory stage.
func (p *Prepared) EnterStage(ctx context.Context, stage string, result *string) error {
	prev, prevStart := p.Stage, p.StageStarted
	if err := p.Op.SetStage(ctx, stage, result); err != nil {
		return err
	}
	now := p.d.Clock.Now()
	p.Stage, p.StageStarted = stage, now
	if result != nil {
		p.Tips.Result = *result
		p.Facts.Result = *result
	}
	p.Log.Line("STAGE", stage)
	return p.Events.Stage(ctx, stage, prev, now.Sub(prevStart))
}

// Decide decides the path of step 7 for branchTip: the snapshot's
// tips.branch the first time; after a race the work branch's tip.
func (p *Prepared) Decide(ctx context.Context, branchTip string) (rebase.Path, error) {
	r := &rebase.Rebaser{Git: p.d.Git}
	dir := p.Selection.Work.Path
	if p.Operation == worktrees.Land {
		return r.DecideLand(ctx, dir, branchTip, p.Tips.Target)
	}
	return r.DecideRebase(ctx, dir, branchTip, p.Tips.Onto)
}

// Resnapshot is the return to step 3 after a race: it re-reads TARGET (a
// land), reloads the repo configuration from the new tip and records both. It
// does not repeat the before hook, the preparation or the preconditions. A
// repo file that no longer loads is returned as a *config.Error for the
// caller to stop on; ONTO is never re-resolved and tips.branch never changes.
func (p *Prepared) Resnapshot(ctx context.Context) error {
	cfgErr, err := p.snapshot(ctx, false)
	if err != nil {
		return err
	}
	if cfgErr != nil {
		return cfgErr
	}
	return nil
}

// Release stops the renewal loop and releases every lock (the records are
// deleted). It is idempotent.
func (p *Prepared) Release(ctx context.Context) error {
	if p.released {
		return nil
	}
	p.released = true
	if p.stopRenew != nil {
		p.stopRenew()
	}
	return p.Op.Finish(context.WithoutCancel(ctx))
}

// Close closes the operation log.
func (p *Prepared) Close() error { return p.Log.Close() }

func (p *Prepared) git(ctx context.Context, dir string, args ...string) (string, error) {
	r := p.d.Git.Run(ctx, dir, args...)
	switch {
	case r.TimedOut:
		return "", fmt.Errorf("git %s: %w", args[0], gitexec.ErrTimeout)
	case !r.OK():
		return "", fmt.Errorf("git %s: exit %d: %s%v", args[0], r.Code, strings.TrimSpace(string(r.Stderr)), r.StartErr)
	}
	return strings.TrimSpace(string(r.Stdout)), nil
}

func (p *Prepared) revParse(ctx context.Context, rev string) (string, error) {
	return p.git(ctx, p.Repo, "rev-parse", "--verify", "-q", rev+"^{commit}")
}

// snapshot is step 3: tips and the repo configuration. first reads BRANCH's
// tip too; a return after a race keeps tips.branch. A configuration failure is
// returned as the first result, any other failure as the second.
func (p *Prepared) snapshot(ctx context.Context, first bool) (cfgErr *config.Error, err error) {
	if first {
		if p.Tips.Branch, err = p.revParse(ctx, "refs/heads/"+p.Request.Branch); err != nil {
			return nil, err
		}
	}
	ref := ""
	if p.Operation == worktrees.Land {
		if p.Tips.Target, err = p.revParse(ctx, "refs/heads/"+p.Request.Target); err != nil {
			return nil, err
		}
		ref = p.Tips.Target // the oid, so the file is read from the tip snapshotted
		p.Facts.Target, p.Facts.TargetTip, p.Facts.Onto = p.Request.Target, p.Tips.Target, p.Tips.Target
	} else {
		ref = p.Pre.RebaseTip(ctx)
		if _, perr := p.revParse(ctx, ref); perr != nil {
			ref = "" // no local primary branch: no repo file
		}
		p.Facts.Onto = p.Tips.Onto
	}
	p.Facts.BranchTip = p.Tips.Branch

	cfg, lerr := p.Pre.Load(ctx, ref)
	var ce *config.Error
	switch {
	case errors.As(lerr, &ce):
		cfgErr = ce
	case lerr != nil:
		return nil, lerr
	default:
		p.Config = cfg
	}

	b, t, o := p.Tips.Branch, p.Tips.Target, p.Tips.Onto
	if err := p.Op.SetTips(ctx, func(tp *locks.Tips) {
		tp.Branch = &b
		if p.Operation == worktrees.Land {
			tp.Target = &t
		} else {
			tp.Onto = &o
		}
	}); err != nil {
		return nil, err
	}
	if p.Config != nil && cfgErr == nil {
		if err := p.Op.SetConfigDigest(ctx, p.Config.Digest); err != nil {
			return nil, err
		}
	}
	return cfgErr, nil
}

// session is the working state of Start.
type session struct {
	d     Deps
	req   Request
	p     *Prepared
	pre   *config.Pre
	onto  string
	tco   bool
	input worktrees.Input
}

// Start runs steps 1 to 7. The error is for failures that are not outcomes
// (git or I/O failures); every lock is released when it is non-nil.
func Start(ctx context.Context, d Deps, req Request) (*Result, error) {
	s := &session{d: d, req: req}
	s.p = &Prepared{Request: req, Operation: req.Operation, d: d, Started: d.Clock.Now()}
	return s.run(ctx)
}

func (s *session) base(o *outcome.Outcome) *outcome.Outcome {
	o.Operation, o.Branch, o.Repo = string(s.req.Operation), s.req.Branch, s.req.RepoFlag
	if s.req.Operation == worktrees.Land {
		o.Target = s.req.Target
	}
	return o
}

func (s *session) usage(format string, a ...any) *Result {
	o := s.base(outcome.New(outcome.UsageError))
	o.Message = fmt.Sprintf(format, a...)
	return &Result{Outcome: o}
}

func (s *session) refused(r *worktrees.Refusal) *outcome.Outcome {
	o := s.base(outcome.NewRefused(r.Reason, string(s.req.Operation), s.req.Branch, s.req.RepoFlag))
	o.Message, o.Detail, o.Role, o.Path = r.Message, r.Detail, string(r.Role), r.Path
	return o
}

func (s *session) resolve(ctx context.Context, rev string) (string, bool) {
	r := s.d.Git.Run(ctx, s.req.Dir, "rev-parse", "--verify", "-q", rev+"^{commit}")
	if !r.OK() {
		return "", false
	}
	return strings.TrimSpace(string(r.Stdout)), true
}

func (s *session) selectWorktrees(ctx context.Context) (*worktrees.Selection, error) {
	return (&worktrees.Selector{Git: s.d.Git}).Select(ctx, s.input)
}

func (s *session) run(ctx context.Context) (*Result, error) {
	req, p := s.req, s.p

	// Step 1: resolve.
	switch {
	case req.Operation != worktrees.Land && req.Operation != worktrees.Rebase:
		return s.usage("unknown operation %q", req.Operation), nil
	case req.Branch == "":
		return s.usage("BRANCH is required"), nil
	case req.Operation == worktrees.Land && req.Target == "":
		return s.usage("land needs a TARGET (BRANCH is %s)", req.Branch), nil
	case req.Operation == worktrees.Land && req.Branch == req.Target:
		return s.usage("BRANCH and TARGET are both %s", req.Branch), nil
	case req.Operation == worktrees.Rebase && req.Onto == "":
		return s.usage("rebase needs --onto REF"), nil
	}
	if r := workbranch.CheckNamespace(req.Branch, req.Target); r != nil {
		return &Result{Outcome: s.refused(r)}, nil
	}
	common, err := s.d.Git.CommonDir(ctx, req.Dir)
	if err != nil {
		return s.usage("%s is not inside a git repository", req.Dir), nil
	}
	p.CommonDir = common

	loader := &config.Loader{Git: s.d.Git, Paths: s.d.Paths}
	s.pre, err = loader.LoadXDG(ctx, req.Dir)
	if err != nil {
		var ce *config.Error
		if errors.As(err, &ce) {
			return &Result{Outcome: s.configOutcome(ce)}, nil
		}
		return nil, err
	}
	p.Pre, p.Repo = s.pre, s.pre.Repo

	if _, ok := s.resolve(ctx, "refs/heads/"+req.Branch); !ok {
		return s.usage("BRANCH %s does not exist", req.Branch), nil
	}
	switch req.Operation {
	case worktrees.Land:
		if _, ok := s.resolve(ctx, "refs/heads/"+req.Target); !ok {
			return s.usage("TARGET %s does not exist", req.Target), nil
		}
	default:
		oid, ok := s.resolve(ctx, req.Onto)
		if !ok {
			return s.usage("REF %s does not name a commit", req.Onto), nil
		}
		s.onto = oid // ONTO is resolved once, here
	}

	s.input = worktrees.Input{
		Op: req.Operation, Branch: req.Branch, Target: req.Target,
		TargetFlag: req.TargetWorktree, SourceFlag: req.SourceWorktree, WorkFlag: req.WorkWorktree,
		Cwd: req.Dir, KeepSourceWorktree: req.KeepSourceWorktree,
	}
	sel, err := s.selectWorktrees(ctx)
	if err != nil {
		var r *worktrees.Refusal
		if errors.As(err, &r) {
			return &Result{Outcome: s.refused(r)}, nil
		}
		return nil, err
	}
	s.tco = sel.TargetCheckedOut // evaluated once, on the starting state

	p.OpID, err = oprecord.NewOpID(s.d.Clock.Now(), s.d.Rand)
	if err != nil {
		return nil, err
	}
	target := req.Target
	if req.Operation == worktrees.Rebase {
		target = ""
	}
	if p.Log, err = oplog.Open(common, p.OpID, req.Branch, target, s.d.Clock); err != nil {
		return nil, err
	}
	p.Events = &oplog.Events{
		Path: oplog.EventsPath(common), Clock: s.d.Clock, Owner: s.d.Owner,
		Common: oplog.Common{
			Repo: p.Repo, Host: s.d.Owner.Host, PID: s.d.Owner.PID, ToolVersion: s.d.Owner.ToolVersion,
			OpID: p.OpID, Operation: string(req.Operation), Branch: req.Branch, Target: target,
		},
	}
	p.Layer = &locks.Layer{
		Git: s.d.Git, Clock: s.d.Clock, Dir: p.Repo,
		Config: locks.Config{LeaseTTL: s.pre.Lock.LeaseTTL, Wait: s.pre.Lock.Wait},
		Out:    s.d.Out, Sleep: s.d.Sleep, Jitter: s.d.Jitter, Interval: s.d.RenewInterval,
		Liveness: s.d.Liveness, Host: s.d.Host,
	}
	p.Events.Locks = p.Layer
	oplog.Attach(p.Layer, p.Log, p.Events)

	// Step 2: lock.
	res, err := s.lock(ctx, sel)
	if res != nil || err != nil {
		if res != nil && res.Outcome != nil {
			s.p.Log.Line("OUTCOME", string(res.Outcome.Outcome)+" "+res.Outcome.Reason)
		}
		_ = p.Log.Close()
		return res, err
	}
	res, err = s.afterLock(ctx)
	if err != nil || (res != nil && res.Prepared == nil) {
		if res != nil && res.Outcome != nil {
			s.p.Log.Line("OUTCOME", string(res.Outcome.Outcome)+" "+res.Outcome.Reason)
		}
		_ = p.Release(ctx)
		_ = p.Log.Close()
	}
	return res, err
}

func entries(sel *worktrees.Selection) []locks.WorktreeEntry {
	var out []locks.WorktreeEntry
	for _, c := range sel.Worktrees {
		e := locks.WorktreeEntry{Admin: c.AdminName, Path: c.Path}
		for _, r := range c.Roles {
			e.Roles = append(e.Roles, string(r))
		}
		if c.StartDetached {
			e.Start.Detached = c.Start
		} else {
			e.Start.Branch = c.Start
		}
		out = append(out, e)
	}
	return out
}

func (s *session) contextMap() map[string]any {
	m := map[string]any{}
	for _, c := range s.req.Context {
		m[c.Key] = c.Value
	}
	return m
}

func (s *session) params(sel *worktrees.Selection) oprecord.Params {
	placeholder := ""
	if s.req.Operation == worktrees.Land && s.req.KeepSourceWorktree && !s.req.KeepSourceBranch {
		placeholder = cleanup.PlaceholderName(sel.Source.AdminName)
	}
	target := s.req.Target
	if s.req.Operation == worktrees.Rebase {
		target = ""
	}
	return oprecord.Params{
		Operation: string(s.req.Operation), Branch: s.req.Branch, Target: target,
		Owner: s.d.Owner, Worktrees: entries(sel), WorkBranch: workbranch.Name(s.p.OpID),
		PlaceholderBranch: placeholder,
		Keep: locks.Keep{
			SourceWorktree: s.req.KeepSourceWorktree, SourceBranch: s.req.KeepSourceBranch,
			RemovalConsent: sel.SourceExplicit,
		},
		Context: s.contextMap(),
	}
}

// lock is step 2. It takes the locks, re-runs the cascade and the checks of
// section 11.1 once they are held, and starts over (releasing everything
// first) when the selection changed in between. On success s.p.Selection and
// s.p.Op are set and (nil, nil) is returned.
func (s *session) lock(ctx context.Context, sel *worktrees.Selection) (*Result, error) {
	p := s.p
	for attempt := 0; ; attempt++ {
		params := s.params(sel)
		rec := oprecord.NewRecord(params, p.OpID, s.d.Clock.Now())
		specs := p.Layer.RebaseLocks(rec)
		if s.req.Operation == worktrees.Land {
			specs = p.Layer.LandLocks(rec)
		}
		set, err := p.Layer.Acquire(ctx, specs...)
		if err != nil {
			return s.lockFailure(err)
		}
		op := &oprecord.Op{Layer: p.Layer, Set: set, Branch: set.ByRef(locks.BranchRef(s.req.Branch))}

		sel2, err := s.selectWorktrees(ctx)
		if err != nil {
			_ = op.Finish(context.WithoutCancel(ctx))
			var r *worktrees.Refusal
			if errors.As(err, &r) {
				return &Result{Outcome: s.refused(r)}, nil
			}
			return nil, err
		}
		if reflect.DeepEqual(entries(sel), entries(sel2)) {
			sel2.TargetCheckedOut = s.tco
			p.Selection, p.Op = sel2, op
			p.Warnings = append(p.Warnings, sel2.Warnings...)
			s.startRenewal(ctx)
			return nil, nil
		}
		_ = op.Finish(context.WithoutCancel(ctx))
		if attempt+1 >= maxReselect {
			return nil, fmt.Errorf("the worktrees of %s kept changing while the locks were taken", s.req.Branch)
		}
		sel = sel2
	}
}

func (s *session) lockFailure(err error) (*Result, error) {
	var busy *locks.BusyError
	var ref *locks.RefusedError
	switch {
	case errors.As(err, &busy):
		o := busy.Outcome(string(s.req.Operation), s.req.Branch, s.targetName())
		o.OpID, o.Log = s.p.OpID, s.p.Log.Path
		o.Message = busy.Error()
		return &Result{Outcome: o}, nil
	case errors.As(err, &ref):
		o := s.base(ref.Outcome(string(s.req.Operation), s.req.Branch, s.req.RepoFlag))
		o.Message = ref.Error()
		if h := ref.Holder; h.OpID != "" {
			o.Holder = &h
			info := outcome.Refusals[ref.Reason]
			next := outcome.NextCommands(h.Operation, h.Branch, s.req.RepoFlag, info.NextFlags)
			o.BlockingOp = []outcome.BlockingOp{{
				OpID: h.OpID, Operation: h.Operation, Branch: h.Branch, Reason: string(ref.Reason), Next: next,
			}}
			o.Next = next
		}
		return &Result{Outcome: o}, nil
	}
	return nil, err
}

func (s *session) targetName() string {
	if s.req.Operation == worktrees.Land {
		return s.req.Target
	}
	return ""
}

// startRenewal keeps every held lease fresh from here on.
func (s *session) startRenewal(ctx context.Context) {
	p := s.p
	lost := make(chan error, len(p.Op.Set.Locks))
	var stops []func()
	for _, h := range p.Op.Set.Locks {
		stop, ch := h.RunRenewal(ctx)
		stops = append(stops, stop)
		go func() {
			if err := <-ch; err != nil {
				lost <- err
			}
		}()
	}
	p.Lost = lost
	p.stopRenew = func() {
		for _, stop := range stops {
			stop()
		}
	}
}

// fail ends the operation with a refusal after the locks are held: it emits
// op_end when op_start was written. The caller releases the locks.
func (s *session) fail(ctx context.Context, o *outcome.Outcome) *Result {
	p := s.p
	o.OpID, o.Log = p.OpID, p.Log.Path
	if p.opStarted {
		stats := map[string]any{"duration_ms": s.d.Clock.Since(p.Started).Milliseconds()}
		_ = p.Events.OpEnd(ctx, oplog.OpEnd{
			Outcome: string(o.Outcome), Reason: o.Reason, Stage: p.Stage, Stats: stats,
			Restored: []outcome.Restored{}, Warnings: warningWords(p.Warnings),
		})
	}
	return &Result{Outcome: o}
}

func warningWords(ws []outcome.Warning) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = string(w)
	}
	return out
}

func (s *session) configOutcome(e *config.Error) *outcome.Outcome {
	var o *outcome.Outcome
	if e.Word == outcome.Refused {
		o = s.base(outcome.NewRefused(e.Reason, string(s.req.Operation), s.req.Branch, s.req.RepoFlag))
	} else {
		o = s.base(outcome.New(e.Word))
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

// afterLock runs steps 3 to 7. Locks are held when it is called.
func (s *session) afterLock(ctx context.Context) (*Result, error) {
	p, req := s.p, s.req
	sel := p.Selection
	p.Tips.Onto = s.onto
	p.Facts = hooks.Facts{
		Operation: string(req.Operation), OpID: p.OpID, Repo: p.Repo, GitCommonDir: p.CommonDir,
		WorkWorktree: sel.Work.Path, SourceWorktree: sel.Source.Path,
		Branch: req.Branch, Context: req.Context,
	}
	if sel.Destination != nil {
		p.Facts.DestinationWorktree = sel.Destination.Path
	}
	p.WorkBranch = workbranch.Name(p.OpID)
	p.Stage, p.StageStarted = oprecord.StageLocked, s.d.Clock.Now()

	// Step 3: snapshot and configure.
	cfgErr, err := p.snapshot(ctx, true)
	if err != nil {
		return nil, err
	}
	digest := ""
	if p.Config != nil {
		digest = p.Config.Digest
	}
	rec := p.Op.Record()
	if err := p.Events.OpStart(ctx, s.contextMap(), rec.Worktrees, p.WorkBranch, rec.PlaceholderBranch, rec.Keep, digest); err != nil {
		return nil, err
	}
	p.opStarted = true
	p.Log.Line("STAGE", oprecord.StageLocked)
	if err := p.Events.Stage(ctx, oprecord.StageLocked, "", 0); err != nil {
		return nil, err
	}

	// Step 4: preconditions (the cascade and the checks ran under the locks
	// in step 2; layout, dirty and role failures were refused there).
	if r := s.d.Git.Run(ctx, p.Repo, "var", "GIT_COMMITTER_IDENT"); !r.OK() {
		o := s.base(outcome.NewRefused(outcome.RefNoGitIdentity, string(req.Operation), req.Branch, req.RepoFlag))
		o.Message = "git has no committer identity: " + strings.TrimSpace(string(r.Stderr))
		return s.fail(ctx, o), nil
	}
	if cfgErr != nil {
		return s.fail(ctx, s.configOutcome(cfgErr)), nil
	}
	diag, err := (&rebase.Rebaser{Git: s.d.Git}).Settings(ctx, sel.Work.Path)
	if err != nil {
		return nil, err
	}
	p.Diag = diag
	for _, l := range diag.Lines {
		p.Log.MergeDriver(l)
	}

	// Step 5: the before hook, once per operation.
	runner := &hooks.Runner{Git: s.d.Git, AutofixAttempts: p.Config.AutofixAttempts, RunGitHooks: p.Config.RunGitHooks}
	hr, err := runner.RunPoint(ctx, hooks.PointInput{
		Point: hooks.PointBefore, Hooks: p.Config.HooksFor(string(req.Operation), hooks.PointBefore), Facts: p.Facts,
	})
	for _, rec := range hr.Records {
		p.Log.Command("hook", rec)
		_ = p.Events.Hook(ctx, rec)
	}
	if err != nil {
		return nil, err
	}
	p.SkippedHooks = hr.Skipped
	if f := hr.Failure; f != nil {
		o := s.base(outcome.NewRefused(outcome.RefBeforeHookRefused, string(req.Operation), req.Branch, req.RepoFlag))
		o.Message = fmt.Sprintf("the before hook (entry %d) %s", f.Index, hookWhy(f))
		o.Detail = hookDetail(f)
		if f.LogTail != "" {
			o.LogTail = strings.Split(f.LogTail, "\n")
		}
		return s.fail(ctx, o), nil
	}

	// Step 6: prepare the work branch.
	tip := p.Tips.Branch
	if err := p.EnterStage(ctx, oprecord.StagePreparing, &tip); err != nil {
		return nil, err
	}
	p.Facts.WorkBranch = p.WorkBranch
	br := &workbranch.Brancher{Git: s.d.Git}
	if err := br.Create(ctx, sel.Work, p.OpID, tip); err != nil {
		var r *worktrees.Refusal
		if errors.As(err, &r) {
			return s.fail(ctx, s.refused(r)), nil
		}
		return nil, err
	}

	// Step 7: decide the path.
	path, err := p.Decide(ctx, p.Tips.Branch)
	if err != nil {
		return nil, err
	}
	p.Path = path
	res := &Result{Prepared: p}
	switch path {
	case rebase.NothingToLand:
		o := s.base(outcome.New(outcome.NothingToLand))
		o.OpID, o.Log, o.PathTaken = p.OpID, p.Log.Path, string(outcome.PathNothing)
		res.Outcome = o
	case rebase.UpToDate:
		o := s.base(outcome.New(outcome.UpToDate))
		o.OpID, o.Log, o.PathTaken, o.Onto = p.OpID, p.Log.Path, string(outcome.PathNothing), req.Onto
		res.Outcome = o
	}
	return res, nil
}

func hookWhy(f *hooks.Failure) string {
	switch f.Kind {
	case hooks.TimedOut:
		return "timed out"
	case hooks.Transient:
		return fmt.Sprintf("exited %d (transient)", f.Code)
	}
	return fmt.Sprintf("exited %d", f.Code)
}

func hookDetail(f *hooks.Failure) string {
	switch f.Kind {
	case hooks.TimedOut:
		return "timeout"
	case hooks.Transient:
		return "transient"
	}
	return fmt.Sprintf("exit-%d", f.Code)
}

// CurrentOwner builds the Owner of this process for the lock records.
func CurrentOwner(host seams.Host, live seams.Liveness, toolVersion, token string) locks.Owner {
	pid := os.Getpid()
	o := locks.Owner{Host: host.Name(), PID: pid, Token: token, ToolVersion: toolVersion}
	if st, err := live.StartTime(pid); err == nil {
		var n int64
		if _, serr := fmt.Sscanf(st, "%d", &n); serr == nil {
			o.PIDStart = n
		}
	}
	return o
}

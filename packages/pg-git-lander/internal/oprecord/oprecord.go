// Package oprecord is the operation record lifecycle of pg-git-lander on top
// of the lock layer: it creates the BRANCH record when an operation holds its
// locks, records stage boundaries and the move intent, turns a running
// operation into a stopped one, takes a stopped record back for a resume flag
// and deletes the records at the end (design sections 7.1, 7.3, 8.2).
//
// Hook output is never written into a record; --context values are stored as
// given (callers must not pass secrets).
package oprecord

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
)

// Stages of an operation (design section 7.1, the stage table).
const (
	StageLocked      = "locked"
	StagePreparing   = "preparing"
	StageRebasing    = "rebasing"
	StageAfterRebase = "after_rebase"
	StageBeforeMove  = "before_move"
	StageMoving      = "moving"
	StageAfter       = "after"
	StageCleanup     = "cleanup"
)

// Stages lists the stages in order.
var Stages = []string{StageLocked, StagePreparing, StageRebasing, StageAfterRebase, StageBeforeMove, StageMoving, StageAfter, StageCleanup}

// MoveIntentMoving is the intent written before the move (step 11).
const MoveIntentMoving = "moving"

// OperationLand is the operation name whose lock set includes TARGET.
const OperationLand = "land"

// NewOpID returns an operation id of the form 20261107T101500Z-3f9c2a. rnd
// supplies the six hex digits' entropy; nil is crypto/rand.
func NewOpID(now time.Time, rnd io.Reader) (string, error) {
	if rnd == nil {
		rnd = rand.Reader
	}
	b := make([]byte, 3)
	if _, err := io.ReadFull(rnd, b); err != nil {
		return "", err
	}
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b), nil
}

// Params are the inputs of a new BRANCH record.
type Params struct {
	Operation         string
	Branch            string
	Target            string
	Owner             locks.Owner
	Worktrees         []locks.WorktreeEntry
	WorkBranch        string
	PlaceholderBranch string
	Keep              locks.Keep
	Context           map[string]any
	ConfigDigest      string
	// Tips are the known starting tips (Branch, Target); nil fields stay null.
	Tips locks.Tips
}

// NewRecord builds the BRANCH record of a new operation in stage locked: every
// field of the section 8.2 example.
func NewRecord(p Params, opID string, now time.Time) locks.Record {
	now = now.UTC().Truncate(time.Second)
	r := locks.Record{
		Schema: locks.SchemaVersion, Role: locks.RoleBranch, State: locks.StateRunning,
		OpID: opID, Operation: p.Operation, Branch: p.Branch, Target: p.Target,
		Owner:             p.Owner,
		Worktrees:         append([]locks.WorktreeEntry(nil), p.Worktrees...),
		WorkBranch:        p.WorkBranch,
		PlaceholderBranch: p.PlaceholderBranch,
		Keep:              p.Keep,
		Move:              locks.Move{},
		StartedAt:         now,
		Stage:             StageLocked,
		StageStartedAt:    now,
		Tips:              p.Tips,
		Attempts:          locks.Attempts{Autofix: map[string]int{}},
		ConfigDigest:      p.ConfigDigest,
		Context:           p.Context,
	}
	if r.Context == nil {
		r.Context = map[string]any{}
	}
	return r
}

// Op is a running operation: its record and the locks it holds.
type Op struct {
	Layer  *locks.Layer
	Set    *locks.Set
	Branch *locks.Held
}

func (o *Op) now() time.Time { return o.Layer.Clock.Now().UTC().Truncate(time.Second) }

// Begin creates the BRANCH record and takes the operation's locks together:
// the whole land set (TARGET, BRANCH and each distinct worktree) for a land,
// the rebase set otherwise. The record is the BRANCH lock's own record. The
// pipeline itself takes a land's locks in two phases (LandBranchLocks, then
// AcquireTarget); Begin is the single-phase form.
func Begin(ctx context.Context, l *locks.Layer, p Params, rnd io.Reader) (*Op, error) {
	id, err := NewOpID(l.Clock.Now(), rnd)
	if err != nil {
		return nil, err
	}
	rec := NewRecord(p, id, l.Clock.Now())
	specs := l.RebaseLocks(rec)
	if p.Operation == OperationLand {
		specs = l.LandLocks(rec)
	}
	set, err := l.Acquire(ctx, specs...)
	if err != nil {
		return nil, err
	}
	return &Op{Layer: l, Set: set, Branch: set.ByRef(locks.BranchRef(p.Branch))}, nil
}

// AcquireTarget is the second lock phase of a land: it takes TARGET and every
// worktree whose only role is destination, in one all-or-nothing Acquire in
// ascending ref order, and adds them to the set. A lock the set already holds
// (a resume that took them back) is skipped, so the call is idempotent. It
// returns the locks newly taken.
func (o *Op) AcquireTarget(ctx context.Context) ([]*locks.Held, error) {
	var specs []locks.Spec
	for _, sp := range o.Layer.LandTargetLocks(o.Branch.Record()) {
		if o.Set.ByRef(sp.Ref) == nil {
			specs = append(specs, sp)
		}
	}
	if len(specs) == 0 {
		return nil, nil
	}
	set, err := o.Layer.Acquire(ctx, specs...)
	if err != nil {
		return nil, err
	}
	o.Set.Locks = append(o.Set.Locks, set.Locks...)
	return set.Locks, nil
}

// TargetLocks returns the held locks of the second phase (TARGET and the
// destination-only worktrees).
func (o *Op) TargetLocks() []*locks.Held {
	var out []*locks.Held
	for _, sp := range o.Layer.LandTargetLocks(o.Branch.Record()) {
		if h := o.Set.ByRef(sp.Ref); h != nil {
			out = append(out, h)
		}
	}
	return out
}

// ReleaseTarget releases the second-phase locks and removes them from the
// set. The caller stops their renewal first.
func (o *Op) ReleaseTarget(ctx context.Context) error {
	held := o.TargetLocks()
	drop := map[*locks.Held]bool{}
	var errs []error
	for i := len(held) - 1; i >= 0; i-- {
		drop[held[i]] = true
		errs = append(errs, held[i].Release(ctx))
	}
	kept := o.Set.Locks[:0:0]
	for _, h := range o.Set.Locks {
		if !drop[h] {
			kept = append(kept, h)
		}
	}
	o.Set.Locks = kept
	return errors.Join(errs...)
}

// Record returns a copy of the current BRANCH record.
func (o *Op) Record() locks.Record { return o.Branch.Record() }

// SetStage records a stage boundary: stage, stage_started_at and, when result
// is non-nil, tips.result (the work branch's tip).
func (o *Op) SetStage(ctx context.Context, stage string, result *string) error {
	now := o.now()
	return o.Branch.Update(ctx, func(r *locks.Record) {
		r.Stage, r.StageStartedAt = stage, now
		if result != nil {
			v := *result
			r.Tips.Result = &v
		}
	})
}

// SetTips writes the recorded tips through mutate.
func (o *Op) SetTips(ctx context.Context, mutate func(*locks.Tips)) error {
	return o.Branch.Update(ctx, func(r *locks.Record) { mutate(&r.Tips) })
}

// SetMove writes the move intent {"moving", expected_result} BEFORE the move,
// so a crash after the fast-forward but before the record is updated is
// recognised (TARGET equal to expected_result means the move happened).
func (o *Op) SetMove(ctx context.Context, expectedResult string) error {
	return o.Branch.Update(ctx, func(r *locks.Record) {
		in, ex := MoveIntentMoving, expectedResult
		r.Move = locks.Move{Intent: &in, ExpectedResult: &ex}
	})
}

// SetCurrentCommand records the running step (nil clears it). The deadline
// gates lease renewal.
func (o *Op) SetCurrentCommand(ctx context.Context, cc *locks.CurrentCommand) error {
	return o.Branch.Update(ctx, func(r *locks.Record) { r.CurrentCommand = cc })
}

// SetAttempts writes the retry counters through mutate.
func (o *Op) SetAttempts(ctx context.Context, mutate func(*locks.Attempts)) error {
	return o.Branch.Update(ctx, func(r *locks.Record) {
		if r.Attempts.Autofix == nil {
			r.Attempts.Autofix = map[string]int{}
		}
		mutate(&r.Attempts)
	})
}

// SetConfigDigest writes config_digest.
func (o *Op) SetConfigDigest(ctx context.Context, digest string) error {
	return o.Branch.Update(ctx, func(r *locks.Record) { r.ConfigDigest = digest })
}

// StopInfo describes why an operation stops. Message MUST NOT carry hook
// output (section 14.7).
type StopInfo struct {
	Reason       outcome.StopReason
	Message      string
	Paths        []string
	MovedAlready bool
	// Retryable overrides the reason's table value when non-nil.
	Retryable *bool
}

// Stop turns the running operation into a stopped one in ONE git transaction:
// the BRANCH and worktree records become stopped (lease null, stop object
// written) and the TARGET record is deleted. The locks are then held by
// nobody; renewal MUST be stopped by the caller.
func (o *Op) Stop(ctx context.Context, s StopInfo) error {
	now := o.now()
	var edits []locks.Edit
	for _, h := range o.Set.Locks {
		switch h.Record().Role {
		case locks.RoleBranch:
			edits = append(edits, locks.Edit{H: h, Mutate: func(r *locks.Record) { stopBranch(r, s, now) }})
		case locks.RoleTarget:
			edits = append(edits, locks.Edit{H: h, Delete: true})
		case locks.RoleWorktree:
			edits = append(edits, locks.Edit{H: h, Mutate: func(r *locks.Record) {
				r.State, r.Lease = locks.StateStopped, nil
			}})
		}
	}
	return o.Layer.Transact(ctx, edits)
}

func stopBranch(r *locks.Record, s StopInfo, now time.Time) {
	retry := outcome.Stops[s.Reason].Retryable
	if s.Retryable != nil {
		retry = *s.Retryable
	}
	paths := s.Paths
	if paths == nil {
		paths = []string{}
	}
	r.State, r.Lease, r.CurrentCommand = locks.StateStopped, nil, nil
	r.Stop = &locks.Stop{
		Reason: string(s.Reason), Retryable: retry, At: now, Stage: r.Stage,
		Paths: paths, Message: s.Message, MovedAlready: s.MovedAlready,
	}
}

// Finish deletes the operation's records and so releases every lock.
func (o *Op) Finish(ctx context.Context) error { return o.Set.Release(ctx) }

// Resumed is an operation taken back from a stopped record by a resume flag.
type Resumed struct {
	Op *Op
	// Stopped is the stopped BRANCH record taken from (its stop object says
	// what happened).
	Stopped locks.Record
}

// TakeBack takes the stopped BRANCH record of branch back by compare-and-swap
// (stopped to running, owner = the caller). Every resume flag, --quit
// included, does this first. A loser gets *locks.RefusedError with reason
// lost-to-concurrent; no record is nothing-stopped.
func TakeBack(ctx context.Context, l *locks.Layer, branch string, owner locks.Owner) (*Resumed, error) {
	h, prior, err := l.TakeBack(ctx, locks.BranchRef(branch), func(r *locks.Record) {
		r.Owner, r.Stop = owner, nil
	})
	if err != nil {
		return nil, err
	}
	return &Resumed{Op: &Op{Layer: l, Set: &locks.Set{Locks: []*locks.Held{h}}, Branch: h}, Stopped: prior}, nil
}

// SwapBack swaps the taken-back record, and every lock taken with it, back to
// its stopped state, so a resume that cannot proceed never strands a record
// in running.
func (r *Resumed) SwapBack(ctx context.Context) error { return r.Op.Set.Undo(ctx) }

// TakesTargetLocks reports whether a resume of a stopped record at stage
// takes TARGET and the destination-only worktrees: only from stage moving on.
// An earlier stage takes them at the lock point of the run, like a fresh one.
func TakesTargetLocks(stage string) bool {
	switch stage {
	case StageMoving, StageAfter, StageCleanup:
		return true
	}
	return false
}

// TakeOtherLocks takes the operation's other locks after the BRANCH take-back:
// its own stopped worktree records by compare-and-swap and, for a land at
// stage moving or later, TARGET by re-acquisition (a worktree whose only role
// is destination is taken back at the same stages and left alone before), in
// ascending ref order with the wait of the lock layer. When any stays busy
// past the wait (or is refused) everything, BRANCH included, is swapped back
// to stopped and the error is returned (*locks.BusyError: the caller exits
// busy).
func (r *Resumed) TakeOtherLocks(ctx context.Context) error {
	rec := r.Op.Branch.Record()
	var specs []locks.Spec
	seen := map[string]bool{}
	withTarget := rec.Operation == OperationLand && TakesTargetLocks(rec.Stage)
	for _, w := range rec.Worktrees {
		if seen[w.Admin] {
			continue
		}
		seen[w.Admin] = true
		if rec.Operation == OperationLand && !withTarget && len(w.Roles) == 1 && w.Roles[0] == "destination" {
			continue
		}
		specs = append(specs, locks.Spec{
			Ref: locks.WorktreeRef(w.Admin), Role: locks.RoleWorktree,
			Rec: rec.Short(locks.RoleWorktree), TakeBack: rec.OpID,
		})
	}
	if withTarget {
		specs = append(specs, locks.Spec{Ref: locks.BranchRef(rec.Target), Role: locks.RoleTarget, Rec: rec.Short(locks.RoleTarget)})
	}
	sort.SliceStable(specs, func(i, j int) bool { return specs[i].Ref < specs[j].Ref })
	set, err := r.Op.Layer.Acquire(ctx, specs...)
	if err != nil {
		return errors.Join(err, r.SwapBack(context.WithoutCancel(ctx)))
	}
	r.Op.Set.Locks = append(r.Op.Set.Locks, set.Locks...)
	return nil
}

// Quit deletes the operation's records (--quit): the BRANCH record already
// taken back is released, and the worktree records, stopped and held by
// nobody, are deleted by compare-and-swap against their stopped value.
func (r *Resumed) Quit(ctx context.Context) error {
	l := r.Op.Layer
	rec := r.Op.Branch.Record()
	var errs []error
	seen := map[string]bool{}
	for _, w := range rec.Worktrees {
		if seen[w.Admin] {
			continue
		}
		seen[w.Admin] = true
		ref := locks.WorktreeRef(w.Admin)
		st, err := l.Read(ctx, ref)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if st.Rec == nil || st.Rec.OpID != rec.OpID || st.Rec.State != locks.StateStopped {
			continue
		}
		if err := l.DeleteIf(ctx, ref, st.OID); err != nil {
			errs = append(errs, err)
		}
	}
	errs = append(errs, r.Op.Set.Release(ctx))
	return errors.Join(errs...)
}

package locks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
)

// Results of a lock wait (the lock_wait event's result).
const (
	WaitAcquired = "acquired"
	WaitBusy     = "busy"
)

// LockWait is one wait for a held lock, handed to the log and events writer
// through Layer.OnWait when Acquire acquired the set after waiting or gave up
// with a BusyError (lock_wait: lock, holder, waited_ms, result).
type LockWait struct {
	// Lock is the ref that was held when the wait began (the last one waited on).
	Lock   string
	Holder outcome.Holder
	Waited time.Duration
	Result string
}

func (l *Layer) emitWait(w LockWait) {
	if l.OnWait != nil {
		l.OnWait(w)
	}
}

// Spec is one lock to take: its ref, role and the record to write.
type Spec struct {
	Ref  string
	Role Role
	Rec  Record
	// TakeBack, when set to an op id, lets the lock be taken by
	// compare-and-swap from a stopped record of that operation (a resume
	// taking back its own worktree records). Any other stopped record is
	// still refused.
	TakeBack string
}

// BusyError is a lock that stayed held (running) for the whole wait.
type BusyError struct {
	Ref    string
	Holder outcome.Holder
}

func (e *BusyError) Error() string {
	return fmt.Sprintf("lock %s is held by %s %s (op %s, pid %d on %s)", e.Ref, e.Holder.Operation, e.Holder.Branch, e.Holder.OpID, e.Holder.PID, e.Holder.Host)
}

// Outcome renders the busy outcome (exit 3).
func (e *BusyError) Outcome(operation, branch, target string) *outcome.Outcome {
	return outcome.NewBusy(operation, branch, target, e.Holder)
}

// RefusedError is a lock that cannot be taken: a stopped record
// (stopped-pending, or target-stopped for a TARGET lock) or an unreadable one
// (unreadable-lock).
type RefusedError struct {
	Ref    string
	Reason outcome.RefusalReason
	Holder outcome.Holder
	Cause  error
}

func (e *RefusedError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("lock %s refused (%s): %v", e.Ref, e.Reason, e.Cause)
	}
	return fmt.Sprintf("lock %s refused (%s)", e.Ref, e.Reason)
}

func (e *RefusedError) Unwrap() error { return e.Cause }

// Outcome renders the refusal outcome (exit 5).
func (e *RefusedError) Outcome(operation, branch, repo string) *outcome.Outcome {
	return outcome.NewRefused(e.Reason, operation, branch, repo)
}

// Set is the locks of one operation, held together.
type Set struct {
	Locks []*Held
}

// ByRef returns the held lock for ref, or nil.
func (s *Set) ByRef(ref string) *Held {
	for _, h := range s.Locks {
		if h.Ref == ref {
			return h
		}
	}
	return nil
}

// Undo gives every lock of the set back, in reverse acquisition order: a lock
// taken back from a stopped record is swapped back to that record, any other
// is released.
func (s *Set) Undo(ctx context.Context) error {
	var errs []error
	for i := len(s.Locks) - 1; i >= 0; i-- {
		if err := s.Locks[i].Undo(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Release releases every lock of the set, in reverse acquisition order.
func (s *Set) Release(ctx context.Context) error {
	var errs []error
	for i := len(s.Locks) - 1; i >= 0; i-- {
		if err := s.Locks[i].Release(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type conflictKind int

const (
	conflictRunning conflictKind = iota
	conflictStopped
	conflictUnreadable
)

type conflict struct {
	kind   conflictKind
	holder outcome.Holder
	cause  error
	since  time.Time
}

func (l *Layer) holderOf(rec *Record) (outcome.Holder, time.Time) {
	h := outcome.Holder{
		OpID: rec.OpID, Operation: rec.Operation, Branch: rec.Branch, Stage: rec.Stage,
		PID: rec.Owner.PID, Host: rec.Owner.Host,
	}
	since := rec.StartedAt
	if since.IsZero() && rec.Lease != nil {
		since = rec.Lease.RenewedAt
	}
	if !since.IsZero() {
		if age := l.Clock.Now().Sub(since); age > 0 {
			h.AgeS = int64(age / time.Second)
		}
	}
	return h, since
}

// tryOnce makes one attempt at one lock. A write is attempted only after a
// read showed the lock absent, so waiters never hold git's ref lock on a poll.
func (l *Layer) tryOnce(ctx context.Context, s Spec) (*Held, *conflict, error) {
	for {
		st, err := l.Read(ctx, s.Ref)
		if err != nil {
			return nil, nil, err
		}
		switch {
		case st.Unreadable != nil:
			return nil, &conflict{kind: conflictUnreadable, cause: st.Unreadable}, nil
		case !st.Absent && s.TakeBack != "" && st.Rec.State == StateStopped && st.Rec.OpID == s.TakeBack:
			rec := s.Rec.Clone()
			rec.Role, rec.State = s.Role, StateRunning
			l.stamp(&rec)
			oid, err := l.write(ctx, s.Ref, rec, st.OID, nil)
			if errors.Is(err, ErrTaken) {
				continue
			}
			if err != nil {
				return nil, nil, err
			}
			prior := st.Rec.Clone()
			return &Held{l: l, Ref: s.Ref, rec: rec, oid: oid, lastRenew: l.Clock.Now(), prior: &prior}, nil, nil
		case !st.Absent && st.Rec.State == StateRunning && l.judgeDeadOrExpired(st) != "":
			held, err := l.takeOver(ctx, s, st)
			if errors.Is(err, errRetryRead) {
				continue
			}
			if err != nil {
				return nil, nil, err
			}
			if held != nil {
				return held, nil, nil
			}
			h, since := l.holderOf(st.Rec)
			return nil, &conflict{kind: conflictRunning, holder: h, since: since}, nil
		case !st.Absent:
			h, since := l.holderOf(st.Rec)
			k := conflictRunning
			if st.Rec.State == StateStopped {
				k = conflictStopped
			}
			return nil, &conflict{kind: k, holder: h, since: since}, nil
		}
		rec := s.Rec.Clone()
		rec.Role, rec.State = s.Role, StateRunning
		l.stamp(&rec)
		oid, err := l.write(ctx, s.Ref, rec, "", nil)
		if errors.Is(err, ErrTaken) {
			continue // someone created it between our read and write: re-read
		}
		if err != nil {
			return nil, nil, err
		}
		return &Held{l: l, Ref: s.Ref, rec: rec, oid: oid, lastRenew: l.Clock.Now()}, nil, nil
	}
}

// Acquire takes the locks in ascending ref-name order, all or nothing. When
// one is held it releases those it holds, waits a random 0 to 500 ms and
// tries again until the wait runs out (a lone lock is polled once a second).
// A stopped record is refused at once (stopped-pending, or target-stopped for
// a TARGET lock); so is an unreadable one (unreadable-lock), as a live
// holder; a running one is waited on and ends in *BusyError naming the
// holder. A running one whose holder is dead or whose lease expired by
// observation is taken over (TARGET role) or converted (BRANCH and worktree
// roles, ending in a holder-died refusal), see takeover.go.
func (l *Layer) Acquire(ctx context.Context, specs ...Spec) (*Set, error) {
	ordered := append([]Spec(nil), specs...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Ref < ordered[j].Ref })
	start := l.Clock.Now()
	nextMsg := MessageEvery
	var wait *LockWait
	for {
		set := &Set{}
		var c *conflict
		var blocked Spec
		for _, s := range ordered {
			h, cf, err := l.tryOnce(ctx, s)
			if err != nil {
				_ = set.Undo(context.WithoutCancel(ctx))
				return nil, err
			}
			if cf != nil {
				c, blocked = cf, s
				break
			}
			set.Locks = append(set.Locks, h)
		}
		if c == nil {
			if wait != nil {
				wait.Waited, wait.Result = l.Clock.Since(start), WaitAcquired
				l.emitWait(*wait)
			}
			return set, nil
		}
		heldSome := len(set.Locks) > 0
		if err := set.Undo(context.WithoutCancel(ctx)); err != nil {
			return nil, err
		}
		switch c.kind {
		case conflictUnreadable:
			return nil, &RefusedError{Ref: blocked.Ref, Reason: outcome.RefUnreadableLock, Cause: c.cause}
		case conflictStopped:
			reason := outcome.RefStoppedPending
			if blocked.Role == RoleTarget {
				reason = outcome.RefTargetStopped
			}
			return nil, &RefusedError{Ref: blocked.Ref, Reason: reason, Holder: c.holder}
		}
		elapsed := l.Clock.Since(start)
		wait = &LockWait{Lock: blocked.Ref, Holder: c.holder}
		if elapsed >= l.Config.Wait {
			wait.Waited, wait.Result = elapsed, WaitBusy
			l.emitWait(*wait)
			return nil, &BusyError{Ref: blocked.Ref, Holder: c.holder}
		}
		for elapsed >= nextMsg {
			if l.Out != nil {
				fmt.Fprintf(l.Out, "waiting for %s %s: stage %s, %ds elapsed\n",
					c.holder.Operation, c.holder.Branch, c.holder.Stage, c.holder.AgeS)
			}
			nextMsg += MessageEvery
		}
		d := PollInterval
		if heldSome {
			d = l.jitter(SetRetryMax)
		}
		if rem := l.Config.Wait - elapsed; d > rem {
			d = rem
		}
		if err := l.sleep(ctx, d); err != nil {
			return nil, err
		}
	}
}

func shortSpec(role Role, ref string, op Record) Spec {
	return Spec{Ref: ref, Role: role, Rec: op.Short(role)}
}

func (l *Layer) worktreeSpecs(op Record, seen map[string]bool) []Spec {
	var out []Spec
	for _, w := range op.Worktrees {
		if seen[w.Admin] {
			continue
		}
		seen[w.Admin] = true
		out = append(out, shortSpec(RoleWorktree, WorktreeRef(w.Admin), op))
	}
	return out
}

// RebaseLocks is the lock set of rebase BRANCH --onto REF (section 8.5):
// BRANCH and each distinct worktree the operation uses (SW and WW). REF is
// read-only and takes no lock. op is the BRANCH record, whose Worktrees list
// the worktrees.
func (l *Layer) RebaseLocks(op Record) []Spec {
	op.Role = RoleBranch
	specs := []Spec{{Ref: BranchRef(op.Branch), Role: RoleBranch, Rec: op}}
	return append(specs, l.worktreeSpecs(op, map[string]bool{})...)
}

// LandLocks is the lock set of land BRANCH: TARGET, BRANCH and each distinct
// worktree (SW, DW and WW).
func (l *Layer) LandLocks(op Record) []Spec {
	op.Role = RoleBranch
	specs := []Spec{
		shortSpec(RoleTarget, BranchRef(op.Target), op),
		{Ref: BranchRef(op.Branch), Role: RoleBranch, Rec: op},
	}
	return append(specs, l.worktreeSpecs(op, map[string]bool{})...)
}

// StaleLock is a ref .lock file left by a killed git process.
type StaleLock struct {
	Path string
	Age  time.Duration
	// Ours is true under refs/pg-git-lander/; false under refs/heads/.
	Ours bool
}

// StaleLockFiles lists .lock files older than StaleLockAge under
// refs/pg-git-lander/ and refs/heads/, for status.
func (l *Layer) StaleLockFiles(ctx context.Context) ([]StaleLock, error) {
	common, err := l.common(ctx)
	if err != nil {
		return nil, err
	}
	var out []StaleLock
	now := l.Clock.Now()
	for _, ns := range []struct {
		dir  string
		ours bool
	}{{"refs/pg-git-lander", true}, {"refs/heads", false}} {
		root := filepath.Join(common, ns.dir)
		werr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".lock") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			if age := now.Sub(info.ModTime()); age > StaleLockAge {
				out = append(out, StaleLock{Path: p, Age: age, Ours: ns.ours})
			}
			return nil
		})
		if werr != nil {
			return nil, werr
		}
	}
	return out, nil
}

// RemoveStaleLockFiles removes the stale .lock files in the tool's own
// namespace and never one under refs/heads/. It returns the paths removed.
func (l *Layer) RemoveStaleLockFiles(ctx context.Context) ([]string, error) {
	stale, err := l.StaleLockFiles(ctx)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, s := range stale {
		if !s.Ours {
			continue
		}
		if err := os.Remove(s.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return removed, err
		}
		removed = append(removed, s.Path)
	}
	return removed, nil
}

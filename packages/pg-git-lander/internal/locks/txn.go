package locks

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
)

// Edit is one change of a Transact: Mutate is applied to a copy of the held
// lock's current record, or the ref is deleted when Delete is set. Either way
// the Held is finished afterwards (no later write, renewal or release touches
// the ref): a stopped record is held by nobody and a deleted one is gone.
type Edit struct {
	H      *Held
	Mutate func(*Record)
	Delete bool
}

// Transact applies the edits to their lock refs in ONE git transaction
// (update-ref --stdin), each swapped from the held lock's current record. A
// failed transaction is retried only while no ref changed hands (section
// 8.4); a changed ref is ErrLockLost wrapping ErrTaken, nothing applied.
func (l *Layer) Transact(ctx context.Context, edits []Edit) error {
	seen := map[*Held]bool{}
	for _, e := range edits {
		if seen[e.H] {
			return fmt.Errorf("locks: %s edited twice in one transaction", e.H.Ref)
		}
		seen[e.H] = true
		e.H.mu.Lock()
		defer e.H.mu.Unlock()
		if e.H.released {
			return fmt.Errorf("%s: %w", e.H.Ref, ErrLockLost)
		}
	}
	next := make([]Record, len(edits))
	blobs := make([]string, len(edits))
	var in strings.Builder
	for i, e := range edits {
		if e.Delete {
			fmt.Fprintf(&in, "delete %s %s\n", e.H.Ref, e.H.oid)
			continue
		}
		next[i] = e.H.rec.Clone()
		e.Mutate(&next[i])
		blob, err := l.hashBlob(ctx, next[i])
		if err != nil {
			return err
		}
		fmt.Fprintf(&in, "update %s %s %s\n", e.H.Ref, blob, e.H.oid)
		blobs[i] = blob
	}
	for attempt := 0; ; attempt++ {
		r := l.Git.RunInput(ctx, l.Dir, []byte(in.String()), "update-ref", "--stdin")
		if r.OK() {
			break
		}
		for _, e := range edits {
			cur, err := l.Read(ctx, e.H.Ref)
			if err != nil {
				return err
			}
			if cur.OID != e.H.oid {
				return fmt.Errorf("%s: %w: %w", e.H.Ref, ErrLockLost, ErrTaken)
			}
		}
		if attempt >= WriteRetries {
			return fmt.Errorf("transaction: %w: %w: %s", ErrLockLost, ErrWriteFailed, strings.TrimSpace(string(r.Stderr)))
		}
		if err := l.sleep(ctx, l.jitter(backoffBase<<attempt)); err != nil {
			return err
		}
	}
	for i, e := range edits {
		e.H.released = true
		if !e.Delete {
			e.H.rec, e.H.oid = next[i], blobs[i]
		}
	}
	return nil
}

// TakeBack takes the stopped record at ref back by compare-and-swap (stopped
// to running): mutate is applied to a copy of it (to make the taker the owner
// and clear the stop), the lease is stamped, and the swap expects the stopped
// record's blob. It returns the lock now held and the stopped record taken
// from; Held.Undo swaps back to that record. Refusals are *RefusedError:
// nothing-stopped (no record), unreadable-lock, and lost-to-concurrent (the
// record is running, or another invocation swapped it first).
func (l *Layer) TakeBack(ctx context.Context, ref string, mutate func(*Record)) (*Held, Record, error) {
	for {
		st, err := l.Read(ctx, ref)
		if err != nil {
			return nil, Record{}, err
		}
		switch {
		case st.Unreadable != nil:
			return nil, Record{}, &RefusedError{Ref: ref, Reason: outcome.RefUnreadableLock, Cause: st.Unreadable}
		case st.Absent:
			return nil, Record{}, &RefusedError{Ref: ref, Reason: outcome.RefNothingStopped}
		case st.Rec.State != StateStopped:
			holder, _ := l.holderOf(ctx, st.Rec)
			return nil, Record{}, &RefusedError{Ref: ref, Reason: outcome.RefLostToConcurrent, Holder: holder}
		}
		prior := st.Rec.Clone()
		rec := st.Rec.Clone()
		mutate(&rec)
		rec.State = StateRunning
		l.stamp(&rec)
		oid, err := l.write(ctx, ref, rec, st.OID, nil)
		if err != nil {
			if isTaken(err) {
				return nil, Record{}, &RefusedError{Ref: ref, Reason: outcome.RefLostToConcurrent, Cause: err}
			}
			return nil, Record{}, err
		}
		return &Held{l: l, Ref: ref, rec: rec, oid: oid, lastRenew: l.Clock.Now(), prior: &prior}, prior, nil
	}
}

func isTaken(err error) bool { return errors.Is(err, ErrTaken) }

// DeleteIf deletes ref when it still points at oid (compare-and-swap), for a
// record held by nobody (a stopped worktree record on --quit).
func (l *Layer) DeleteIf(ctx context.Context, ref, oid string) error {
	return l.deleteRef(ctx, ref, oid)
}

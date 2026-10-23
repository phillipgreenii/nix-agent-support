package engine

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// PrimaryChange is one stored change of the primary group. Prev is nil for a
// first sighting.
type PrimaryChange struct {
	ID         string
	Prev, Next []byte
	Info       RefreshInfo
}

// DetectPrimary is the primary-group seam's body: it runs inside the
// transaction that stored c.Next, after the rows are written.
//
// A nil Prev, a content the classifier describes as gone and an empty ID are the
// baseline and do nothing (the recorded origin hint is still consumed).
// Otherwise the change is classified and the groups the classifier names become
// the awaited set: merged into the entity's pending row when there is one (the
// deadline does not move), written as a new pending row when there is none and
// something is awaited (even when the primary's own result is empty), and
// appended at once when nothing is awaited.
func (e *Engine) DetectPrimary(ctx context.Context, tx *store.Tx, c PrimaryChange) error {
	origin := e.originFor(c.ID, e.spec.PrimaryGroup, c.Info.Purpose)
	if c.Prev == nil || c.ID == "" {
		return nil
	}
	next, err := e.cls.Describe(c.Next)
	if err != nil {
		return fmt.Errorf("engine: describe primary of %s: %w", c.ID, err)
	}
	if next.Gone {
		return nil
	}
	res, queue, err := e.cls.Primary(c.Prev, c.Next, e.bodyCap)
	if err != nil {
		return fmt.Errorf("engine: classify primary of %s: %w", c.ID, err)
	}
	now := e.now()

	p, has, err := tx.GetPending(ctx, e.spec.EntityKind, c.ID)
	if err != nil {
		return err
	}
	if has {
		pres, aw, err := pendingResult(p)
		if err != nil {
			return err
		}
		if err := e.expire(ctx, tx, c.ID, queue, now); err != nil {
			return err
		}
		p.SetStamps(next.Stamp, next.Stamp2)
		return e.putPending(ctx, tx, p, Merge(e.spec.Changes, pres, res), e.await(aw, queue, now))
	}
	if len(queue) > 0 {
		if err := e.expire(ctx, tx, c.ID, queue, now); err != nil {
			return err
		}
		np := store.PendingChange{
			Kind: e.spec.EntityKind, ID: c.ID,
			Origin: origin, OriginUpdatedAt: next.OriginAt,
			DetectedAt: now, Deadline: now.Add(e.settle),
		}
		np.SetStamps(next.Stamp, next.Stamp2)
		return e.putPending(ctx, tx, np, res, e.await(nil, queue, now))
	}
	_, err = e.appendRow(ctx, tx, c.ID, next.Ref, next.Stamp, res, origin, next.OriginAt)
	return err
}

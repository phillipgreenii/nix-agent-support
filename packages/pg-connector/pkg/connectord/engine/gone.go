package engine

import (
	"context"
	"strconv"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// removedMetaKey is the store_meta key that records that a removed row was
// appended for an entity's current tombstone.
func removedMetaKey(id string) string { return "changes.removed." + id }

// DetectGone is the tombstone seam's body. The seam's transaction has already
// set tombstoned_at; this method does not write it.
//
// When the entity has stored primary content (a baseline) it appends one
// removed-role row naming every configured query whose stored id list contains
// the entity (compared through Spec.FoldRef), merged with the entity's pending
// row when there is one (a tombstone ends the barrier: nothing more will be
// fetched). With no stored primary content it appends nothing and deletes any
// pending row. A repeat call for the same tombstone appends nothing.
func (e *Engine) DetectGone(ctx context.Context, tx *store.Tx, id string, purpose governor.Purpose) error {
	origin := e.originFor(id, e.spec.PrimaryGroup, purpose)
	p, has, err := tx.GetPending(ctx, e.spec.EntityKind, id)
	if err != nil {
		return err
	}
	prim, ok, err := e.storedPrimary(ctx, tx, id)
	if err != nil {
		return err
	}
	if !ok {
		if has {
			return tx.DeletePending(ctx, e.spec.EntityKind, id)
		}
		return nil
	}

	tomb := "0"
	ent, entOK, err := tx.GetEntity(ctx, e.spec.EntityKind, id)
	if err != nil {
		return err
	}
	if entOK && !ent.TombstonedAt.IsZero() {
		tomb = strconv.FormatInt(ent.TombstonedAt.UnixMilli(), 10)
	}
	if done, ok, err := tx.GetMeta(ctx, removedMetaKey(id)); err != nil {
		return err
	} else if ok && done == tomb {
		if has {
			return tx.DeletePending(ctx, e.spec.EntityKind, id)
		}
		return nil
	}

	var names []string
	if prim.Ref != "" {
		queries, err := tx.ListQueries(ctx, true)
		if err != nil {
			return err
		}
		want := e.fold(prim.Ref)
		for _, q := range queries {
			if !q.Configured {
				continue
			}
			for _, member := range q.IDs {
				if e.fold(member) == want {
					names = append(names, q.Key)
					break
				}
			}
		}
	}

	res := RemovedResult(e.spec.Roles.Removed, names)
	stamp, originAt := prim.Stamp, prim.OriginAt
	if has {
		pres, _, err := pendingResult(p)
		if err != nil {
			return err
		}
		pstamp, _ := p.Stamps()
		res, origin, stamp, originAt = Merge(e.spec.Changes, pres, res), p.Origin, pstamp, p.OriginUpdatedAt
		if err := tx.DeletePending(ctx, e.spec.EntityKind, id); err != nil {
			return err
		}
	}
	if _, err := e.appendRow(ctx, tx, id, prim.Ref, stamp, res, origin, originAt); err != nil {
		return err
	}
	return tx.PutMeta(ctx, removedMetaKey(id), tomb)
}

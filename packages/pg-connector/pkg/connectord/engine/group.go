package engine

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// DetectGroup is the group seam's body: it runs inside the transaction that
// stored next. Only a stored refresh reaches it (a refresh that started before
// a write invalidation loses its commit in the group engine and never gets
// here).
//
// A nil prev is the group's baseline and adds no fields, but still satisfies the
// barrier for an awaited group when the fetch counts (Classifier.Counts). With a
// pending row the group's fields merge into it; without one a non-empty result
// appends at once.
func (e *Engine) DetectGroup(ctx context.Context, tx *store.Tx, prev *store.GroupRow, next store.GroupRow, info RefreshInfo) error {
	if next.Kind != e.spec.EntityKind || next.Group == e.spec.PrimaryGroup {
		return nil
	}
	id := next.ID
	origin := e.originFor(id, next.Group, info.Purpose)

	var res Result
	if prev != nil {
		var err error
		if res, err = e.cls.Group(next.Group, prev.Content, next.Content, e.bodyCap); err != nil {
			return fmt.Errorf("engine: classify %s of %s: %w", next.Group, id, err)
		}
	}

	p, has, err := tx.GetPending(ctx, e.spec.EntityKind, id)
	if err != nil {
		return err
	}
	if !has {
		return e.appendNow(ctx, tx, id, res, origin)
	}
	pres, aw, err := pendingResult(p)
	if err != nil {
		return err
	}
	kept := aw[:0:0]
	for _, a := range aw {
		if !e.cls.Counts(a, next, p, info.Started) {
			kept = append(kept, a)
		}
	}
	return e.putPending(ctx, tx, p, Merge(e.spec.Changes, pres, res), kept)
}

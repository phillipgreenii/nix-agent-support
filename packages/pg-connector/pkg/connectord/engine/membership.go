package engine

import (
	"context"
	"errors"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Membership is an arrival or departure event the query runner hands the
// engine.
type Membership struct {
	// Kind is Spec.Roles.EnteredQuery or Spec.Roles.LeftQuery.
	Kind string
	// Query is the query's name; ID the entity key; Ref the wire reference.
	Query, ID, Ref string
	// Extra is appended to the row's fields.
	Extra []Field
}

// AppendMembership records a membership change. It ensures the entity row
// exists (an entity new to the store has none yet when it enters a query), then
// merges the change into the entity's pending row when one exists and leaves
// that row pending (a membership change never waits on the barrier itself and
// never ends someone else's), and otherwise appends one row at once with origin
// OriginQuery.
func (e *Engine) AppendMembership(ctx context.Context, tx *store.Tx, m Membership) error {
	if m.ID == "" || m.Ref == "" {
		return errors.New("engine: membership needs an id and a reference")
	}
	if m.Kind != e.spec.Roles.EnteredQuery && m.Kind != e.spec.Roles.LeftQuery {
		return errors.New("engine: membership kind " + m.Kind + " is neither the arrival nor the departure role")
	}
	if err := tx.EnsureEntity(ctx, e.spec.EntityKind, m.ID); err != nil {
		return err
	}
	res := Merge(e.spec.Changes, MembershipResult(e.spec.Roles, m.Kind, m.Query), Result{Fields: m.Extra})

	p, has, err := tx.GetPending(ctx, e.spec.EntityKind, m.ID)
	if err != nil {
		return err
	}
	if has {
		pres, aw, err := pendingResult(p)
		if err != nil {
			return err
		}
		return savePending(ctx, tx, p, Merge(e.spec.Changes, pres, res), aw)
	}
	var stamp string
	var originAt time.Time
	if prim, ok, err := e.storedPrimary(ctx, tx, m.ID); err != nil {
		return err
	} else if ok {
		stamp, originAt = prim.Stamp, prim.OriginAt
	}
	_, err = e.appendRow(ctx, tx, m.ID, m.Ref, stamp, res, OriginQuery, originAt)
	return err
}

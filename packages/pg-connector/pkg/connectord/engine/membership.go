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
	// Watch is zero for no watch. For an arrival into a watched query it is
	// WatchBaseline (the query's baseline run: set the flag, append no row at
	// all) or WatchLate (set the flag; the arrival row may gain the Added kind).
	Watch WatchMode
	// Via is the cause recorded with the flag; it is required when Watch is set.
	Via string
}

// AppendMembership records a membership change. With Watch set it first sets the
// entity's watched flag (creating the entity row), and with WatchBaseline it
// stops there. An entity that is not watched gets nothing at all.
//
// Otherwise the arrival is ONE row, the union of two parts: the membership kind
// with its query field, only when Spec.Membership.StoreRows; and the Added kind
// when this very call newly set the flag (the Added part carries no fields). An empty union appends nothing.
// The row is merged into the entity's pending row when one exists and that row
// is left pending (a membership change never waits on the barrier itself and
// never ends someone else's), and otherwise appended at once with origin
// OriginQuery.
func (e *Engine) AppendMembership(ctx context.Context, tx *store.Tx, m Membership) error {
	if m.ID == "" || m.Ref == "" {
		return errors.New("engine: membership needs an id and a reference")
	}
	if m.Kind != e.spec.Roles.EnteredQuery && m.Kind != e.spec.Roles.LeftQuery {
		return errors.New("engine: membership kind " + m.Kind + " is neither the arrival nor the departure role")
	}
	if m.Watch != 0 && !m.Watch.valid() {
		return errors.New("engine: membership watch mode is neither the baseline nor the late watch")
	}
	if m.Watch != 0 && m.Via == "" {
		return errors.New("engine: a membership that watches needs a cause (via)")
	}

	newly := false
	if m.Watch != 0 {
		var err error
		if newly, err = tx.SetWatched(ctx, e.spec.EntityKind, m.ID, m.Via, e.now()); err != nil {
			return err
		}
		if m.Watch == WatchBaseline {
			return nil
		}
	} else if skip, err := e.watchedGate(ctx, tx, m.ID); err != nil || skip {
		return err
	}
	if err := tx.EnsureEntity(ctx, e.spec.EntityKind, m.ID); err != nil {
		return err
	}

	var res Result
	if e.spec.Membership.StoreRows {
		res = Merge(e.spec.Changes, MembershipResult(e.spec.Roles, m.Kind, m.Query), Result{Fields: m.Extra})
	}
	if newly && m.Kind == e.spec.Roles.EnteredQuery {
		res = Merge(e.spec.Changes, res, Result{Kinds: []string{e.spec.Roles.Added}})
	}
	if res.Empty() {
		return nil
	}

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

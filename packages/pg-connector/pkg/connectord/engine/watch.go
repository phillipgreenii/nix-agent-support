package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// WatchMode says when a watch is applied relative to the entity's baseline.
// The zero value is no watch.
type WatchMode int

const (
	// WatchBaseline sets the flag and never appends a row: the watch is part of
	// a baseline run.
	WatchBaseline WatchMode = iota + 1
	// WatchLate sets the flag and, when it was newly set and the emission rules
	// say so (Spec.Emission is registry.EmitWatchedOnly), appends one row of the
	// Spec.Roles.Added kind.
	WatchLate
)

// valid reports whether m is one of the two watch modes.
func (m WatchMode) valid() bool { return m == WatchBaseline || m == WatchLate }

// WatchRequest asks for one entity to be watched.
type WatchRequest struct {
	// ID is the entity key. Ref is the wire reference; it is needed only when
	// the entity has no stored primary content.
	ID, Ref string
	// Via is the cause recorded with the flag (registry.ViaQuery, ViaDirect or
	// ViaAdHoc, the first and last followed by a query name).
	Via  string
	Mode WatchMode
}

// WatchResult reports what a watch did: Newly is whether this call set the flag
// and Appended whether it appended a row.
type WatchResult struct{ Newly, Appended bool }

// watchedGate reports whether detection for id must stop: the emission is
// registry.EmitWatchedOnly and the entity is not watched. It reads the flag in
// the open transaction.
func (e *Engine) watchedGate(ctx context.Context, tx *store.Tx, id string) (skip bool, err error) {
	if e.spec.Emission != registry.EmitWatchedOnly {
		return false, nil
	}
	watched, err := tx.IsWatched(ctx, e.spec.EntityKind, id)
	return !watched, err
}

// Watch sets the entity's watched flag inside tx, first cause wins. A flag that
// was already set changes nothing and appends nothing. When the flag was newly
// set and the mode is WatchLate under registry.EmitWatchedOnly, one row of the
// Added role is appended, provided the entity has stored primary content (a
// known entity); an entity with none gets the flag only, and so does every
// watch under registry.EmitAll and every WatchBaseline.
func (e *Engine) Watch(ctx context.Context, tx *store.Tx, w WatchRequest) (WatchResult, error) {
	switch {
	case tx == nil:
		return WatchResult{}, errors.New("engine: watch needs an open transaction")
	case w.ID == "":
		return WatchResult{}, errors.New("engine: watch needs an entity id")
	case w.Via == "":
		return WatchResult{}, errors.New("engine: watch needs a cause (via)")
	case !w.Mode.valid():
		return WatchResult{}, fmt.Errorf("engine: watch mode %d is neither the baseline nor the late watch", w.Mode)
	}
	newly, err := tx.SetWatched(ctx, e.spec.EntityKind, w.ID, w.Via, e.now())
	if err != nil {
		return WatchResult{}, err
	}
	res := WatchResult{Newly: newly}
	if !newly || w.Mode != WatchLate || e.spec.Emission != registry.EmitWatchedOnly {
		return res, nil
	}
	prim, ok, err := e.storedPrimary(ctx, tx, w.ID)
	if err != nil || !ok {
		return res, err
	}
	ref := prim.Ref
	if ref == "" {
		ref = w.Ref
	}
	origin := OriginQuery
	if strings.HasPrefix(w.Via, registry.ViaDirect) {
		origin = OriginRead
	}
	added := Result{Kinds: []string{e.spec.Roles.Added}}
	res.Appended, err = e.appendRow(ctx, tx, w.ID, ref, prim.Stamp, added, origin, prim.OriginAt)
	return res, err
}

// WatchOne is Watch for one entity outside any transaction of the caller: it
// opens its own with st.InTx. It is the one-off watch helper of a read that
// carries the watch option; call it before the entity is refreshed or read, so a
// change the refresh finds is appended as a row of a watched entity.
func (e *Engine) WatchOne(ctx context.Context, st *store.Store, w WatchRequest) (WatchResult, error) {
	if st == nil {
		return WatchResult{}, errors.New("engine: watch needs a store")
	}
	var res WatchResult
	err := st.InTx(ctx, func(tx *store.Tx) error {
		var err error
		res, err = e.Watch(ctx, tx, w)
		return err
	})
	if err != nil {
		return WatchResult{}, err
	}
	return res, nil
}

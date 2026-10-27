package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// The delivery gate of INV-ICD-16.
//
// pg-desk appends, and so delivers, no change_log row for an issue entity
// that is not watched, whatever the origin of the row. Every row reaches
// change_log through AppendChangeLogTx, so the gate is ONE check inside that
// function; this file holds its scope predicate, the transactional column
// writes the feed path needs, and the write options that carry them.
//
// The gate is on APPEND, never on read: ReadChanges and ListChangesAfter stay
// unfiltered, so a closed row appended while the entity was watched is still
// delivered after the mark is cleared.

// GateScope reports whether the delivery gate applies to an entity. It runs
// inside the append's transaction (tx) and receives the entity's backend
// column AS IT WILL STAND after any SetBackend of the same write ("" = NULL).
// A scope MUST NOT write.
type GateScope func(tx *sql.Tx, repo, entityType, entityID, backend string) (inScope bool, err error)

// SetGateScope installs the gate's scope predicate on this handle. Open
// installs none, so nothing is gated until a scope is set and every existing
// caller behaves as before. Passing nil removes the gate. It is meant to be
// called right after Open, before the handle is shared.
func (s *Store) SetGateScope(scope GateScope) {
	if scope == nil {
		s.gateScope.Store(nil)
		return
	}
	s.gateScope.Store(&scope)
}

// OwningGateScope is the first rollout scope of the gate: an entity of type
// issue whose backend column names an instance in owning, or whose backend is
// NULL and whose id resolve maps to an owning instance (the resolver covers
// the ids that predate the backend column's backfill; a nil resolve resolves
// nothing). A beads entity is out of scope, so it still emits through the old
// tiers. A later phase widens this predicate and adds no new append path.
func OwningGateScope(owning map[string]bool, resolve func(entityID string) (instance string, ok bool)) GateScope {
	return func(_ *sql.Tx, _, entityType, entityID, backend string) (bool, error) {
		if entityType != "issue" {
			return false, nil
		}
		if backend != "" {
			return owning[backend], nil
		}
		if resolve == nil {
			return false, nil
		}
		instance, ok := resolve(entityID)
		return ok && owning[instance], nil
	}
}

// FeedMarker is the change-feed position of one backend instance: the
// identity of that backend's own store and the highest feed sequence pg-desk
// has committed from it.
type FeedMarker struct {
	Instance      string
	StoreIdentity string
	Seq           int64
}

// WriteOptions are the transactional extras of WriteEntityStateWithLogOpts.
type WriteOptions struct {
	// Backend, when non-nil, is written to entity.backend BEFORE the append (a
	// pointer to "" writes NULL), so the gate sees the new backend.
	Backend *string
	// Watched, when non-nil, is written to entity.watched BEFORE the append, so
	// the row of a first observation passes the gate.
	Watched *bool
	// ClearWatchedAfterAppend marks the entity as stated not watched AFTER the
	// append, in the same transaction, so a closed or removed row of a watched
	// entity is always appended and delivered.
	ClearWatchedAfterAppend bool
	// FeedMarker is accepted and ignored here; the feed-marker write is the
	// feed store's own step.
	FeedMarker *FeedMarker
}

func (o WriteOptions) isZero() bool {
	return o.Backend == nil && o.Watched == nil && !o.ClearWatchedAfterAppend
}

// gateColumns reads the entity's backend and watched columns inside tx. found
// is false when the entity row is absent.
func gateColumns(tx *sql.Tx, repo, entityType, entityID string) (backend string, watched, found bool, err error) {
	var b sql.NullString
	var w sql.NullInt64
	err = tx.QueryRow(
		`SELECT backend, watched FROM entity WHERE repo = ? AND entity_type = ? AND entity_id = ?`,
		repo, entityType, entityID,
	).Scan(&b, &w)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, err
	}
	return b.String, w.Valid && w.Int64 != 0, true, nil
}

// setColumnsTx writes the non-nil arguments to entity.backend and
// entity.watched inside tx (a pointer to "" writes NULL to backend). It
// appends nothing and bumps no version.
func setColumnsTx(tx *sql.Tx, repo, entityType, entityID string, backend *string, watched *bool) error {
	var sets []string
	var args []any
	if backend != nil {
		sets = append(sets, "backend = ?")
		args = append(args, nullableString(*backend))
	}
	if watched != nil {
		w := 0
		if *watched {
			w = 1
		}
		sets = append(sets, "watched = ?")
		args = append(args, w)
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, repo, entityType, entityID)
	if _, err := tx.Exec(
		`UPDATE entity SET `+strings.Join(sets, ", ")+` WHERE repo = ? AND entity_type = ? AND entity_id = ?`,
		args...,
	); err != nil {
		return fmt.Errorf("store: set entity columns (%s,%s,%s): %w", repo, entityType, entityID, err)
	}
	return nil
}

// clearWatchedTx marks the entity as stated not watched (watched = 0).
func clearWatchedTx(tx *sql.Tx, repo, entityType, entityID string) error {
	off := false
	return setColumnsTx(tx, repo, entityType, entityID, nil, &off)
}

// WriteEntityStateWithLogOpts is WriteEntityStateWithLogFP with the
// transactional options of the feed path. opts.Backend and opts.Watched are
// copied into the ChangeRecord it appends and written inside this
// transaction BEFORE the append (so the delivery gate sees them), and
// opts.ClearWatchedAfterAppend clears the mark AFTER it. When kinds is empty
// no row is appended, but the columns are still written (and cleared) in the
// same transaction. When the gate withholds the row the snapshot, version
// bump and column writes still commit and the new version is returned. Same
// compare-and-set, ErrVersionConflict and ConflictCount behavior as the
// other writers; new schema only.
func (s *Store) WriteEntityStateWithLogOpts(e Entity, expectedVersion int64, hydratedAt string, active bool, listFP *string, kinds []string, origin, at string, opts WriteOptions) (newVersion int64, err error) {
	return s.writeEntity(e, expectedVersion, &entityState{hydratedAt: hydratedAt, active: active, listFP: listFP}, kinds, origin, at, opts)
}

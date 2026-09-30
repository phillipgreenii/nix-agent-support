package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// The origin/relation xref API (schema version 2 only). Links are either
// derived (origin "derived:<extractor>", replaced on every hydration of the
// entity they start from) or externally managed (origin "external:<actor>",
// persisting until that actor removes them). An external link never
// overrides a derived one, and no suppression state is stored.
//
// Rows owned by the old xref accessors (origin xrefLegacyOrigin) are never
// touched by this API's writes; they are returned by its list functions.

const (
	xrefDerivedPrefix  = "derived:"
	xrefExternalPrefix = "external:"
)

// XrefLink is one xref row with its origin and relation. For derived links
// Origin is "derived:<extractor>" and Actor, ActedAt and Reason are empty.
// For external links Origin is set by AddExternalXref from Actor.
type XrefLink struct {
	Repo          string
	FromType      string
	FromID        string
	ToType        string
	ToID          string
	Relation      string
	Origin        string
	Evidence      string
	FirstSeen     string // RFC3339
	LastConfirmed string // RFC3339
	Actor         string // external only
	ActedAt       string // external only
	Reason        string // external only
}

// ReplaceDerivedXrefs makes links the complete set of derived (non-legacy)
// links leaving the entity (repo, fromType, fromID): links present are
// upserted (keeping their first_seen), derived links no longer present are
// deleted. External and legacy rows are untouched. Every link must belong to
// that entity and carry a "derived:<extractor>" origin.
func (s *Store) ReplaceDerivedXrefs(repo, fromType, fromID string, links []XrefLink) error {
	if err := s.RequireNewSchema(); err != nil {
		return err
	}
	wrap := func(err error) error {
		return fmt.Errorf("store: replace derived xrefs (%s,%s,%s): %w", repo, fromType, fromID, err)
	}
	for _, l := range links {
		if l.Repo != repo || l.FromType != fromType || l.FromID != fromID {
			return wrap(fmt.Errorf("link %s,%s,%s does not start from the entity", l.Repo, l.FromType, l.FromID))
		}
		if !strings.HasPrefix(l.Origin, xrefDerivedPrefix) || l.Origin == xrefLegacyOrigin || l.Relation == "" {
			return wrap(fmt.Errorf("link to (%s,%s) needs a derived:<extractor> origin (not %q) and a relation", l.ToType, l.ToID, xrefLegacyOrigin))
		}
	}
	tx, err := s.sql.Begin()
	if err != nil {
		return wrap(err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	type key struct{ toType, toID, relation, origin string }
	keep := map[key]bool{}
	for _, l := range links {
		keep[key{l.ToType, l.ToID, l.Relation, l.Origin}] = true
		if _, err := tx.Exec(
			`INSERT INTO xref (repo, from_type, from_id, to_type, to_id, origin, relation, evidence, first_seen, last_confirmed)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (repo, from_type, from_id, to_type, to_id, relation, origin) DO UPDATE SET
			   evidence = excluded.evidence,
			   last_confirmed = excluded.last_confirmed`,
			repo, fromType, fromID, l.ToType, l.ToID, l.Origin, l.Relation, nullableString(l.Evidence), l.FirstSeen, l.LastConfirmed,
		); err != nil {
			return wrap(err)
		}
	}

	rows, err := tx.Query(
		`SELECT to_type, to_id, relation, origin FROM xref
		 WHERE repo = ? AND from_type = ? AND from_id = ? AND origin LIKE 'derived:%' AND origin != ?`,
		repo, fromType, fromID, xrefLegacyOrigin,
	)
	if err != nil {
		return wrap(err)
	}
	var stale []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.toType, &k.toID, &k.relation, &k.origin); err != nil {
			_ = rows.Close()
			return wrap(err)
		}
		if !keep[k] {
			stale = append(stale, k)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return wrap(err)
	}
	_ = rows.Close()
	for _, k := range stale {
		if _, err := tx.Exec(
			`DELETE FROM xref WHERE repo = ? AND from_type = ? AND from_id = ? AND to_type = ? AND to_id = ? AND relation = ? AND origin = ?`,
			repo, fromType, fromID, k.toType, k.toID, k.relation, k.origin,
		); err != nil {
			return wrap(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return wrap(err)
	}
	return nil
}

// AddExternalXref records an externally managed link (origin
// "external:<x.Actor>"). It is a silent no-op when any derived row exists
// for the same (repo, from, to, relation): an external link never overrides
// a derived one. Adding the same actor's link again refreshes its
// last_confirmed, reason and acted_at.
func (s *Store) AddExternalXref(x XrefLink) error {
	if err := s.RequireNewSchema(); err != nil {
		return err
	}
	wrap := func(err error) error {
		return fmt.Errorf("store: add external xref (%s,%s,%s -> %s,%s): %w", x.Repo, x.FromType, x.FromID, x.ToType, x.ToID, err)
	}
	if x.Actor == "" || x.Relation == "" {
		return wrap(fmt.Errorf("actor and relation are required"))
	}
	origin := xrefExternalPrefix + x.Actor
	tx, err := s.sql.Begin()
	if err != nil {
		return wrap(err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	var derived int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM xref WHERE repo = ? AND from_type = ? AND from_id = ? AND to_type = ? AND to_id = ? AND relation = ? AND origin LIKE 'derived:%'`,
		x.Repo, x.FromType, x.FromID, x.ToType, x.ToID, x.Relation,
	).Scan(&derived); err != nil {
		return wrap(err)
	}
	if derived > 0 {
		return nil
	}
	if _, err := tx.Exec(
		`INSERT INTO xref (repo, from_type, from_id, to_type, to_id, origin, relation, evidence, first_seen, last_confirmed, actor, acted_at, reason)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (repo, from_type, from_id, to_type, to_id, relation, origin) DO UPDATE SET
		   evidence = excluded.evidence,
		   last_confirmed = excluded.last_confirmed,
		   acted_at = excluded.acted_at,
		   reason = excluded.reason`,
		x.Repo, x.FromType, x.FromID, x.ToType, x.ToID, origin, x.Relation, nullableString(x.Evidence),
		x.FirstSeen, x.LastConfirmed, x.Actor, nullableString(x.ActedAt), nullableString(x.Reason),
	); err != nil {
		return wrap(err)
	}
	if err := tx.Commit(); err != nil {
		return wrap(err)
	}
	return nil
}

// RemoveExternalXref removes the link (from, to, relation) that actor added
// and reports whether a row was removed. It never removes a derived row or
// another actor's link.
func (s *Store) RemoveExternalXref(repo, fromType, fromID, toType, toID, relation, actor string) (bool, error) {
	if err := s.RequireNewSchema(); err != nil {
		return false, err
	}
	if actor == "" {
		return false, fmt.Errorf("store: remove external xref: actor is required")
	}
	res, err := s.sql.Exec(
		`DELETE FROM xref WHERE repo = ? AND from_type = ? AND from_id = ? AND to_type = ? AND to_id = ? AND relation = ? AND origin = ?`,
		repo, fromType, fromID, toType, toID, relation, xrefExternalPrefix+actor,
	)
	if err != nil {
		return false, fmt.Errorf("store: remove external xref (%s,%s,%s -> %s,%s): %w", repo, fromType, fromID, toType, toID, err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

const xrefLinkCols = `repo, from_type, from_id, to_type, to_id, relation, origin, evidence, first_seen, last_confirmed, actor, acted_at, reason`

// ListXrefLinksFrom returns every link leaving (repo, fromType, fromID),
// with origin and relation, ordered by target, relation and origin.
func (s *Store) ListXrefLinksFrom(repo, fromType, fromID string) ([]XrefLink, error) {
	return s.queryXrefLinks(`WHERE repo = ? AND from_type = ? AND from_id = ?`, repo, fromType, fromID)
}

// ListXrefLinksTo returns every link arriving at (repo, toType, toID), with
// origin and relation, ordered by source, relation and origin.
func (s *Store) ListXrefLinksTo(repo, toType, toID string) ([]XrefLink, error) {
	return s.queryXrefLinks(`WHERE repo = ? AND to_type = ? AND to_id = ?`, repo, toType, toID)
}

func (s *Store) queryXrefLinks(where string, args ...any) ([]XrefLink, error) {
	if err := s.RequireNewSchema(); err != nil {
		return nil, err
	}
	rows, err := s.sql.Query(
		`SELECT `+xrefLinkCols+` FROM xref `+where+` ORDER BY from_type, from_id, to_type, to_id, relation, origin`, args...,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list xref links: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []XrefLink
	for rows.Next() {
		var l XrefLink
		var evidence, actor, actedAt, reason sql.NullString
		if err := rows.Scan(&l.Repo, &l.FromType, &l.FromID, &l.ToType, &l.ToID, &l.Relation, &l.Origin,
			&evidence, &l.FirstSeen, &l.LastConfirmed, &actor, &actedAt, &reason); err != nil {
			return nil, fmt.Errorf("store: scan xref link: %w", err)
		}
		l.Evidence, l.Actor, l.ActedAt, l.Reason = evidence.String, actor.String, actedAt.String, reason.String
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate xref links: %w", err)
	}
	return out, nil
}

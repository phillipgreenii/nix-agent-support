package store

import (
	"database/sql"
	"fmt"
)

// Xref is one row of the xref table: a cross-reference from one entity to
// another, with evidence and first/last-confirmed timestamps, per the
// design doc's section 7.6.
//
// Phase 9 created the xref table empty and wrote nothing to it — cross-
// reference population is Phase 13 (docket pg2-2j5ac.40). It is populated
// starting Phase 13 by internal/gather's own ticket-key scan
// (UpsertXref, one call per (ticket key, evidence field) pair) and read
// back by cmd/pg-desk/run.go's `run issue <jira-ticket-key>` (ListXrefsByTo)
// and this docket's Slack-half sibling packet's `run thread`
// (ListXrefsByTo/ListXrefsByFrom).
//
// Schema-dual: this struct and the four accessors below are the OLD xref
// API. On the new schema (after the cutover) the table also carries
// origin, relation, actor, acted_at and reason, and its primary key
// includes relation and origin; the old accessors then read and write only
// the rows marked xrefLegacyOrigin / xrefLegacyRelation, which is what
// every pre-cutover row becomes. The richer accessors are a separate API.
type Xref struct {
	Repo          string
	FromType      string
	FromID        string
	ToType        string
	ToID          string
	Evidence      string
	FirstSeen     string // RFC3339 timestamp
	LastConfirmed string // RFC3339 timestamp
}

const (
	// xrefLegacyOrigin and xrefLegacyRelation mark the rows the OLD xref
	// API owns on the new schema: the cutover stamps every pre-existing row
	// with them, and the old writer stamps every row it inserts.
	xrefLegacyOrigin   = "derived:legacy"
	xrefLegacyRelation = "references"
)

// xrefLegacyFilter returns the extra WHERE clause (and its arguments) that
// restricts a query to the legacy rows on the new schema, and nothing on the
// old schema, where the columns do not exist. It issues its own query, so
// call it before starting any other result set.
func (s *Store) xrefLegacyFilter() (clause string, args []any, err error) {
	newSchema, err := s.isNewSchema()
	if err != nil {
		return "", nil, err
	}
	if !newSchema {
		return "", nil, nil
	}
	return " AND origin = ? AND relation = ?", []any{xrefLegacyOrigin, xrefLegacyRelation}, nil
}

// UpsertXref inserts or replaces the xref row keyed by
// (Repo, FromType, FromID, ToType, ToID). On the new schema the row is the
// legacy-marked one (see Xref).
func (s *Store) UpsertXref(x Xref) error {
	newSchema, err := s.isNewSchema()
	if err != nil {
		return fmt.Errorf("store: upsert xref (%s,%s,%s -> %s,%s): %w", x.Repo, x.FromType, x.FromID, x.ToType, x.ToID, err)
	}
	if newSchema {
		_, err = s.sql.Exec(
			`INSERT INTO xref (repo, from_type, from_id, to_type, to_id, origin, relation, evidence, first_seen, last_confirmed)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (repo, from_type, from_id, to_type, to_id, relation, origin) DO UPDATE SET
			   evidence = excluded.evidence,
			   last_confirmed = excluded.last_confirmed`,
			x.Repo, x.FromType, x.FromID, x.ToType, x.ToID, xrefLegacyOrigin, xrefLegacyRelation,
			x.Evidence, x.FirstSeen, x.LastConfirmed,
		)
	} else {
		_, err = s.sql.Exec(
			`INSERT INTO xref (repo, from_type, from_id, to_type, to_id, evidence, first_seen, last_confirmed)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (repo, from_type, from_id, to_type, to_id) DO UPDATE SET
			   evidence = excluded.evidence,
			   last_confirmed = excluded.last_confirmed`,
			x.Repo, x.FromType, x.FromID, x.ToType, x.ToID, x.Evidence, x.FirstSeen, x.LastConfirmed,
		)
	}
	if err != nil {
		return fmt.Errorf("store: upsert xref (%s,%s,%s -> %s,%s): %w", x.Repo, x.FromType, x.FromID, x.ToType, x.ToID, err)
	}
	return nil
}

// GetXref returns the xref row for (repo, fromType, fromID, toType, toID),
// or found=false if no such row exists. On the new schema only the
// legacy-marked row is visible (see Xref).
func (s *Store) GetXref(repo, fromType, fromID, toType, toID string) (xref Xref, found bool, err error) {
	filter, filterArgs, err := s.xrefLegacyFilter()
	if err != nil {
		return Xref{}, false, fmt.Errorf("store: get xref (%s,%s,%s -> %s,%s): %w", repo, fromType, fromID, toType, toID, err)
	}
	row := s.sql.QueryRow(
		`SELECT repo, from_type, from_id, to_type, to_id, evidence, first_seen, last_confirmed
		 FROM xref WHERE repo = ? AND from_type = ? AND from_id = ? AND to_type = ? AND to_id = ?`+filter,
		append([]any{repo, fromType, fromID, toType, toID}, filterArgs...)...,
	)
	if err := row.Scan(&xref.Repo, &xref.FromType, &xref.FromID, &xref.ToType, &xref.ToID,
		&xref.Evidence, &xref.FirstSeen, &xref.LastConfirmed); err != nil {
		if err == sql.ErrNoRows {
			return Xref{}, false, nil
		}
		return Xref{}, false, fmt.Errorf("store: get xref (%s,%s,%s -> %s,%s): %w", repo, fromType, fromID, toType, toID, err)
	}
	return xref, true, nil
}

// ListXrefsByTo returns every xref row for (repo, toType, toID), regardless
// of from_type/from_id — the REVERSE (to-id-keyed, multi-row) lookup
// GetXref's own exact-key query cannot serve at all, since it requires
// from_id already known, which is exactly what this lookup exists to
// discover (docket pg2-2j5ac.40, Phase 13). Consumed by
// cmd/pg-desk/run.go's own `run issue <jira-ticket-key>` (to_type="issue")
// and this docket's Slack-half sibling packet's `run thread`
// (to_type="thread") — the same reverse lookup, two to_type values. Returns
// an empty (nil) slice, not an error, when nothing matches. On the new
// schema only the legacy-marked rows are returned (see Xref).
func (s *Store) ListXrefsByTo(repo, toType, toID string) ([]Xref, error) {
	filter, filterArgs, err := s.xrefLegacyFilter()
	if err != nil {
		return nil, fmt.Errorf("store: list xrefs by to (%s,%s,%s): %w", repo, toType, toID, err)
	}
	rows, err := s.sql.Query(
		`SELECT repo, from_type, from_id, to_type, to_id, evidence, first_seen, last_confirmed
		 FROM xref WHERE repo = ? AND to_type = ? AND to_id = ?`+filter,
		append([]any{repo, toType, toID}, filterArgs...)...,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list xrefs by to (%s,%s,%s): %w", repo, toType, toID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []Xref
	for rows.Next() {
		var x Xref
		if err := rows.Scan(&x.Repo, &x.FromType, &x.FromID, &x.ToType, &x.ToID,
			&x.Evidence, &x.FirstSeen, &x.LastConfirmed); err != nil {
			return nil, fmt.Errorf("store: scan xref row (%s,%s,%s): %w", repo, toType, toID, err)
		}
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate xrefs by to (%s,%s,%s): %w", repo, toType, toID, err)
	}
	return out, nil
}

// ListXrefsByFrom returns every xref row for (repo, fromType, fromID,
// toType) — the FORWARD direction, regardless of to_id (docket
// pg2-2j5ac.40, Phase 13). Added alongside ListXrefsByTo for this docket's
// Slack-half sibling packet's own gather-stage addition (reading thread
// entities already linked to a PR, from_type="pr"/to_type="thread"), which
// has no other file/edit rights of its own to add it — this packet already
// holds xref.go edit rights for ListXrefsByTo above. Not consumed by this
// packet's own code. Returns an empty (nil) slice, not an error, when
// nothing matches. On the new schema only the legacy-marked rows are
// returned (see Xref).
func (s *Store) ListXrefsByFrom(repo, fromType, fromID, toType string) ([]Xref, error) {
	filter, filterArgs, err := s.xrefLegacyFilter()
	if err != nil {
		return nil, fmt.Errorf("store: list xrefs by from (%s,%s,%s,%s): %w", repo, fromType, fromID, toType, err)
	}
	rows, err := s.sql.Query(
		`SELECT repo, from_type, from_id, to_type, to_id, evidence, first_seen, last_confirmed
		 FROM xref WHERE repo = ? AND from_type = ? AND from_id = ? AND to_type = ?`+filter,
		append([]any{repo, fromType, fromID, toType}, filterArgs...)...,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list xrefs by from (%s,%s,%s,%s): %w", repo, fromType, fromID, toType, err)
	}
	defer func() { _ = rows.Close() }()

	var out []Xref
	for rows.Next() {
		var x Xref
		if err := rows.Scan(&x.Repo, &x.FromType, &x.FromID, &x.ToType, &x.ToID,
			&x.Evidence, &x.FirstSeen, &x.LastConfirmed); err != nil {
			return nil, fmt.Errorf("store: scan xref row (%s,%s,%s,%s): %w", repo, fromType, fromID, toType, err)
		}
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate xrefs by from (%s,%s,%s,%s): %w", repo, fromType, fromID, toType, err)
	}
	return out, nil
}

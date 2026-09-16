package store

import (
	"database/sql"
	"fmt"
)

// Xref is one row of the xref table: a cross-reference from one entity to
// another, with evidence and first/last-confirmed timestamps, per the
// design doc's section 7.6.
//
// This phase (Phase 9) creates the xref table empty and never writes to
// it — cross-reference population is Phase 13 (this packet's Out of scope
// section). UpsertXref/GetXref exist so a later packet's interpret stage
// has the writer/reader to call; nothing in THIS package calls them.
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

// UpsertXref inserts or replaces the xref row keyed by
// (Repo, FromType, FromID, ToType, ToID).
func (s *Store) UpsertXref(x Xref) error {
	_, err := s.sql.Exec(
		`INSERT INTO xref (repo, from_type, from_id, to_type, to_id, evidence, first_seen, last_confirmed)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (repo, from_type, from_id, to_type, to_id) DO UPDATE SET
		   evidence = excluded.evidence,
		   last_confirmed = excluded.last_confirmed`,
		x.Repo, x.FromType, x.FromID, x.ToType, x.ToID, x.Evidence, x.FirstSeen, x.LastConfirmed,
	)
	if err != nil {
		return fmt.Errorf("store: upsert xref (%s,%s,%s -> %s,%s): %w", x.Repo, x.FromType, x.FromID, x.ToType, x.ToID, err)
	}
	return nil
}

// GetXref returns the xref row for (repo, fromType, fromID, toType, toID),
// or found=false if no such row exists.
func (s *Store) GetXref(repo, fromType, fromID, toType, toID string) (xref Xref, found bool, err error) {
	row := s.sql.QueryRow(
		`SELECT repo, from_type, from_id, to_type, to_id, evidence, first_seen, last_confirmed
		 FROM xref WHERE repo = ? AND from_type = ? AND from_id = ? AND to_type = ? AND to_id = ?`,
		repo, fromType, fromID, toType, toID,
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

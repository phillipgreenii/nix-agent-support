package store

import (
	"database/sql"
	"fmt"
)

// Interpretation is one row of the interpretation table: ownership,
// enrichment, urgency, category, dispositions, approvals, gate state, match
// reasons, panel, ready-to-promote, degraded, and sync_error for a single
// (repo, entity_type, entity_id), per the design doc's section 7.6.
//
// sync_error is a field on THIS table (not a separate table) written only
// by Phase 10's sync when it exists; no Phase-9 packet (gather, interpret,
// or the pipeline) ever writes it, so it stays empty here (corrected during
// this docket's semantic post-check, round 1 — see this packet's Contract
// section).
type Interpretation struct {
	Repo       string
	EntityType string
	EntityID   string

	Ownership      string
	Enrichment     string // JSON
	Urgency        string // JSON
	Category       string
	Dispositions   string // JSON
	Approvals      string // JSON
	GateState      string
	MatchReasons   string // JSON
	Panel          string
	ReadyToPromote bool
	Degraded       bool
	SyncError      string // old schema only (the cutover drops the column); always empty on the new schema
	AsOf           string // RFC3339 timestamp

	// InterpVersion is the stamp the row was written under (the interp_version
	// column). Readers fill it; "" means NULL, a row written before stamping
	// (or read from a store that does not have the column yet). It is never
	// written from this field: the writer stamps with the handle's stamp
	// (SetInterpStamp).
	InterpVersion string
}

// interpretationSelectList is the column list the readers select. It is
// schema-dual: the new schema has no sync_error column (the cutover drops
// it), so there the same position is filled with NULL and SyncError reads
// back empty. It is also guarded for the stamp: a store that has not had
// interp_version added yet (a read-only or raw open that ran before any Open
// of the new binary) gets NULL in that position, so the readers work and the
// row reads as unstamped.
func interpretationSelectList(newSchema, haveStamp bool) string {
	syncError := "sync_error"
	if newSchema {
		syncError = "NULL"
	}
	stamp := "interp_version"
	if !haveStamp {
		stamp = "NULL"
	}
	return `repo, entity_type, entity_id, ownership, enrichment, urgency, category,
		        dispositions, approvals, gate_state, match_reasons, panel,
		        ready_to_promote, degraded, ` + syncError + `, as_of, ` + stamp
}

// selectListFor decides both guards from the table's current shape, before
// any query is issued (never while a result set is open).
func (s *Store) selectListFor() (string, error) {
	newSchema, haveStamp, err := s.interpShape()
	if err != nil {
		return "", err
	}
	return interpretationSelectList(newSchema, haveStamp), nil
}

// UpsertInterpretation inserts or replaces the interpretation row keyed by
// (Repo, EntityType, EntityID). It works on both schema versions; on the
// new schema, which has no sync_error column, a non-empty SyncError is an
// error rather than a silently dropped write.
//
// It writes the handle's stamp (SetInterpStamp) into interp_version: a handle
// with no stamp writes NULL. On a store whose table has no interp_version
// column (a raw open that never ran the ensure step) the stamp is not written.
func (s *Store) UpsertInterpretation(i Interpretation) error {
	newSchema, haveStamp, err := s.interpShape()
	if err != nil {
		return fmt.Errorf("store: upsert interpretation (%s,%s,%s): %w", i.Repo, i.EntityType, i.EntityID, err)
	}
	stampCol, stampVal, stampSet := "", "", ""
	var stampArgs []any
	if haveStamp {
		stampCol, stampVal, stampSet = ", interp_version", ", ?", ", interp_version = excluded.interp_version"
		stampArgs = []any{nullableString(s.InterpStamp())}
	}
	if newSchema {
		if i.SyncError != "" {
			return fmt.Errorf("store: upsert interpretation (%s,%s,%s): SyncError is not stored on the new schema", i.Repo, i.EntityType, i.EntityID)
		}
		_, err = s.sql.Exec(
			`INSERT INTO interpretation (
			   repo, entity_type, entity_id, ownership, enrichment, urgency, category,
			   dispositions, approvals, gate_state, match_reasons, panel,
			   ready_to_promote, degraded, as_of`+stampCol+`
			 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?`+stampVal+`)
			 ON CONFLICT (repo, entity_type, entity_id) DO UPDATE SET
			   ownership = excluded.ownership,
			   enrichment = excluded.enrichment,
			   urgency = excluded.urgency,
			   category = excluded.category,
			   dispositions = excluded.dispositions,
			   approvals = excluded.approvals,
			   gate_state = excluded.gate_state,
			   match_reasons = excluded.match_reasons,
			   panel = excluded.panel,
			   ready_to_promote = excluded.ready_to_promote,
			   degraded = excluded.degraded,
			   as_of = excluded.as_of`+stampSet,
			append([]any{
				i.Repo, i.EntityType, i.EntityID, i.Ownership, i.Enrichment, i.Urgency, i.Category,
				i.Dispositions, i.Approvals, i.GateState, i.MatchReasons, i.Panel,
				i.ReadyToPromote, i.Degraded, i.AsOf,
			}, stampArgs...)...,
		)
	} else {
		_, err = s.sql.Exec(
			`INSERT INTO interpretation (
			   repo, entity_type, entity_id, ownership, enrichment, urgency, category,
			   dispositions, approvals, gate_state, match_reasons, panel,
			   ready_to_promote, degraded, sync_error, as_of`+stampCol+`
			 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?`+stampVal+`)
			 ON CONFLICT (repo, entity_type, entity_id) DO UPDATE SET
			   ownership = excluded.ownership,
			   enrichment = excluded.enrichment,
			   urgency = excluded.urgency,
			   category = excluded.category,
			   dispositions = excluded.dispositions,
			   approvals = excluded.approvals,
			   gate_state = excluded.gate_state,
			   match_reasons = excluded.match_reasons,
			   panel = excluded.panel,
			   ready_to_promote = excluded.ready_to_promote,
			   degraded = excluded.degraded,
			   sync_error = excluded.sync_error,
			   as_of = excluded.as_of`+stampSet,
			append([]any{
				i.Repo, i.EntityType, i.EntityID, i.Ownership, i.Enrichment, i.Urgency, i.Category,
				i.Dispositions, i.Approvals, i.GateState, i.MatchReasons, i.Panel,
				i.ReadyToPromote, i.Degraded, nullableString(i.SyncError), i.AsOf,
			}, stampArgs...)...,
		)
	}
	if err != nil {
		return fmt.Errorf("store: upsert interpretation (%s,%s,%s): %w", i.Repo, i.EntityType, i.EntityID, err)
	}
	return nil
}

// ListInterpretations returns every interpretation row, ordered by
// (repo, entity_type, entity_id) for a deterministic result — this
// docket's packet 7 (serve) routes rows into a fixed set of panel arrays
// and needs a stable order across calls, not incidental SQLite scan order.
func (s *Store) ListInterpretations() ([]Interpretation, error) {
	cols, err := s.selectListFor()
	if err != nil {
		return nil, fmt.Errorf("store: list interpretations: %w", err)
	}
	rows, err := s.sql.Query(
		`SELECT ` + cols + `
		 FROM interpretation ORDER BY repo, entity_type, entity_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list interpretations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Interpretation
	for rows.Next() {
		var interp Interpretation
		var syncError, stamp sql.NullString
		if err := rows.Scan(&interp.Repo, &interp.EntityType, &interp.EntityID, &interp.Ownership,
			&interp.Enrichment, &interp.Urgency, &interp.Category, &interp.Dispositions, &interp.Approvals,
			&interp.GateState, &interp.MatchReasons, &interp.Panel, &interp.ReadyToPromote, &interp.Degraded,
			&syncError, &interp.AsOf, &stamp); err != nil {
			return nil, fmt.Errorf("store: scan interpretation row: %w", err)
		}
		interp.SyncError = syncError.String
		interp.InterpVersion = stamp.String
		out = append(out, interp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list interpretations: %w", err)
	}
	return out, nil
}

// HasAnyInterpretation reports whether the interpretation table holds at
// least one row. This is the 503-until-first-interpretation gate this
// docket's packet 7 (serve) applies to every route (Binding decisions:
// "503 for every route until the interpretation table has ≥1 row").
func (s *Store) HasAnyInterpretation() (bool, error) {
	var exists int
	if err := s.sql.QueryRow(`SELECT EXISTS(SELECT 1 FROM interpretation LIMIT 1)`).Scan(&exists); err != nil {
		return false, fmt.Errorf("store: has any interpretation: %w", err)
	}
	return exists != 0, nil
}

// GetInterpretation returns the interpretation row for
// (repo, entityType, entityID), or found=false if no such row exists.
func (s *Store) GetInterpretation(repo, entityType, entityID string) (interp Interpretation, found bool, err error) {
	cols, err := s.selectListFor()
	if err != nil {
		return Interpretation{}, false, fmt.Errorf("store: get interpretation (%s,%s,%s): %w", repo, entityType, entityID, err)
	}
	var syncError, stamp sql.NullString
	row := s.sql.QueryRow(
		`SELECT `+cols+`
		 FROM interpretation WHERE repo = ? AND entity_type = ? AND entity_id = ?`,
		repo, entityType, entityID,
	)
	if err := row.Scan(&interp.Repo, &interp.EntityType, &interp.EntityID, &interp.Ownership,
		&interp.Enrichment, &interp.Urgency, &interp.Category, &interp.Dispositions, &interp.Approvals,
		&interp.GateState, &interp.MatchReasons, &interp.Panel, &interp.ReadyToPromote, &interp.Degraded,
		&syncError, &interp.AsOf, &stamp); err != nil {
		if err == sql.ErrNoRows {
			return Interpretation{}, false, nil
		}
		return Interpretation{}, false, fmt.Errorf("store: get interpretation (%s,%s,%s): %w", repo, entityType, entityID, err)
	}
	interp.SyncError = syncError.String
	interp.InterpVersion = stamp.String
	return interp, true, nil
}

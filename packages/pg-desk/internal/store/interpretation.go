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
	SyncError      string // Phase 10 only; this phase never writes it
	AsOf           string // RFC3339 timestamp
}

// UpsertInterpretation inserts or replaces the interpretation row keyed by
// (Repo, EntityType, EntityID).
func (s *Store) UpsertInterpretation(i Interpretation) error {
	_, err := s.sql.Exec(
		`INSERT INTO interpretation (
		   repo, entity_type, entity_id, ownership, enrichment, urgency, category,
		   dispositions, approvals, gate_state, match_reasons, panel,
		   ready_to_promote, degraded, sync_error, as_of
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
		   as_of = excluded.as_of`,
		i.Repo, i.EntityType, i.EntityID, i.Ownership, i.Enrichment, i.Urgency, i.Category,
		i.Dispositions, i.Approvals, i.GateState, i.MatchReasons, i.Panel,
		i.ReadyToPromote, i.Degraded, nullableString(i.SyncError), i.AsOf,
	)
	if err != nil {
		return fmt.Errorf("store: upsert interpretation (%s,%s,%s): %w", i.Repo, i.EntityType, i.EntityID, err)
	}
	return nil
}

// GetInterpretation returns the interpretation row for
// (repo, entityType, entityID), or found=false if no such row exists.
func (s *Store) GetInterpretation(repo, entityType, entityID string) (interp Interpretation, found bool, err error) {
	var syncError sql.NullString
	row := s.sql.QueryRow(
		`SELECT repo, entity_type, entity_id, ownership, enrichment, urgency, category,
		        dispositions, approvals, gate_state, match_reasons, panel,
		        ready_to_promote, degraded, sync_error, as_of
		 FROM interpretation WHERE repo = ? AND entity_type = ? AND entity_id = ?`,
		repo, entityType, entityID,
	)
	if err := row.Scan(&interp.Repo, &interp.EntityType, &interp.EntityID, &interp.Ownership,
		&interp.Enrichment, &interp.Urgency, &interp.Category, &interp.Dispositions, &interp.Approvals,
		&interp.GateState, &interp.MatchReasons, &interp.Panel, &interp.ReadyToPromote, &interp.Degraded,
		&syncError, &interp.AsOf); err != nil {
		if err == sql.ErrNoRows {
			return Interpretation{}, false, nil
		}
		return Interpretation{}, false, fmt.Errorf("store: get interpretation (%s,%s,%s): %w", repo, entityType, entityID, err)
	}
	interp.SyncError = syncError.String
	return interp, true, nil
}

package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// Interpretation is one row of the interpretation table: ownership,
// enrichment, urgency, category, dispositions, approvals, gate state, match
// reasons, panel, ready-to-promote and degraded for a single
// (repo, entity_type, entity_id), per the design doc's section 7.6.
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
	AsOf           string // RFC3339 timestamp

	// InterpVersion is the stamp the row was written under (the interp_version
	// column). Readers fill it; "" means NULL, a row written before stamping
	// (or read from a store that does not have the column yet). It is never
	// written from this field: the writer stamps with the handle's stamp
	// (SetInterpStamp).
	InterpVersion string
}

// interpretationSelectList is the column list the readers select. It is
// guarded for the stamp: a store that has not had interp_version added yet (a
// read-only or raw open that ran before any Open of the new binary) gets NULL
// in that position, so the readers work and the row reads as unstamped.
func interpretationSelectList(haveStamp bool) string {
	stamp := "interp_version"
	if !haveStamp {
		stamp = "NULL"
	}
	return `repo, entity_type, entity_id, ownership, enrichment, urgency, category,
		        dispositions, approvals, gate_state, match_reasons, panel,
		        ready_to_promote, degraded, as_of, ` + stamp
}

// selectListFor decides the guard from the table's current shape, before any
// query is issued (never while a result set is open).
func (s *Store) selectListFor() (string, error) {
	haveStamp, err := s.hasInterpStampColumn()
	if err != nil {
		return "", err
	}
	return interpretationSelectList(haveStamp), nil
}

// UpsertInterpretation inserts or replaces the interpretation row keyed by
// (Repo, EntityType, EntityID). It is the seeding writer of tests (production
// code writes through WriteInterpretation) and runs one statement.
//
// It writes the handle's stamp (SetInterpStamp) into interp_version: a handle
// with no stamp writes NULL. On a store whose table has no interp_version
// column (a raw open that never ran the ensure step) the stamp is not written.
func (s *Store) UpsertInterpretation(i Interpretation) error {
	haveStamp, err := s.hasInterpStampColumn()
	if err == nil {
		err = upsertInterpretationNew(s.sql, i, haveStamp, s.InterpStamp())
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
		var stamp sql.NullString
		if err := rows.Scan(&interp.Repo, &interp.EntityType, &interp.EntityID, &interp.Ownership,
			&interp.Enrichment, &interp.Urgency, &interp.Category, &interp.Dispositions, &interp.Approvals,
			&interp.GateState, &interp.MatchReasons, &interp.Panel, &interp.ReadyToPromote, &interp.Degraded,
			&interp.AsOf, &stamp); err != nil {
			return nil, fmt.Errorf("store: scan interpretation row: %w", err)
		}
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
	var stamp sql.NullString
	row := s.sql.QueryRow(
		`SELECT `+cols+`
		 FROM interpretation WHERE repo = ? AND entity_type = ? AND entity_id = ?`,
		repo, entityType, entityID,
	)
	if err := row.Scan(&interp.Repo, &interp.EntityType, &interp.EntityID, &interp.Ownership,
		&interp.Enrichment, &interp.Urgency, &interp.Category, &interp.Dispositions, &interp.Approvals,
		&interp.GateState, &interp.MatchReasons, &interp.Panel, &interp.ReadyToPromote, &interp.Degraded,
		&interp.AsOf, &stamp); err != nil {
		if err == sql.ErrNoRows {
			return Interpretation{}, false, nil
		}
		return Interpretation{}, false, fmt.Errorf("store: get interpretation (%s,%s,%s): %w", repo, entityType, entityID, err)
	}
	interp.InterpVersion = stamp.String
	return interp, true, nil
}

// execer is the Exec half of *sql.DB and *sql.Tx.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// upsertInterpretationNew is the ONE statement that writes an interpretation
// row. WriteInterpretation runs it inside its transaction; UpsertInterpretation
// (the seeding writer of tests, not called from production code) runs it on
// the handle.
// stamp is written into interp_version (NULL when empty) when haveStamp, i.e.
// when the table has the column.
func upsertInterpretationNew(ex execer, i Interpretation, haveStamp bool, stamp string) error {
	stampCol, stampVal, stampSet := "", "", ""
	var stampArgs []any
	if haveStamp {
		stampCol, stampVal, stampSet = ", interp_version", ", ?", ", interp_version = excluded.interp_version"
		stampArgs = []any{nullableString(stamp)}
	}
	_, err := ex.Exec(
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
	return err
}

// Decision is the part of an interpretation an operator reads: who owes the
// next step, the panel the entity belongs in, and the reason.
type Decision struct {
	WaitingOn string
	Panel     string
	Reason    string
}

// Decision reads the decision out of the row's approvals blob and panel. A
// malformed blob reads as no decision.
func (i Interpretation) Decision() Decision {
	var a struct {
		WaitingOn string `json:"waiting_on"`
		Reason    string `json:"reason"`
	}
	_ = json.Unmarshal([]byte(i.Approvals), &a)
	return Decision{WaitingOn: a.WaitingOn, Panel: i.Panel, Reason: a.Reason}
}

// DecisionPairChanged reports whether the pair a change record is about
// (waiting_on, panel) differs between two rows.
func DecisionPairChanged(old, new Interpretation) bool {
	o, n := old.Decision(), new.Decision()
	return o.WaitingOn != n.WaitingOn || o.Panel != n.Panel
}

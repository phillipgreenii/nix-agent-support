package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Revision is one observed (head_sha, base_sha) of a PR in time order.
type Revision struct {
	ID           int64
	PRID         int64
	Seq          int
	HeadSHA      string
	BaseSHA      string // "" when NULL
	ObservedAt   string
	LastSeenAt   string
	CIState      string
	CIPassed     int
	CIFailed     int
	CIPending    int
	CICapturedAt string // "" when NULL
	// GateState is the approval-gate's overall verdict for this revision —
	// "satisfied" | "partially-satisfied" | "unsatisfied" | "unknown"
	// (schema v11, pg2-4dz88.2.5). Distinct from CIState: a CI suite passing
	// does not mean an approval-gate policy is satisfied, and vice versa.
	// "unknown" until SetRevisionGateState has actually recorded an
	// observation for this revision.
	GateState string
	// GateStateN and GateStateM are the gate's satisfied/total counts, e.g.
	// partially-satisfied(n,m) or unsatisfied(0,m). Meaningful ONLY when
	// GateState is "partially-satisfied" or "unsatisfied" — SetRevisionGateState
	// persists them as NULL (read back here as 0) for "satisfied"/"unknown",
	// which never carry the pair.
	GateStateN, GateStateM int
	// GateStateCapturedAt is when the gate state was last observed; "" when
	// NULL. Mirrors CICapturedAt: the gate can be re-evaluated for an
	// existing revision without a new revision being appended.
	GateStateCapturedAt string
}

const revisionColumns = `id, pr_id, seq, head_sha, COALESCE(base_sha,''),
	observed_at, last_seen_at, ci_state, ci_passed, ci_failed, ci_pending,
	COALESCE(ci_captured_at,''),
	gate_state, COALESCE(gate_state_n,0), COALESCE(gate_state_m,0),
	COALESCE(gate_state_captured_at,'')`

func scanRevision(s rowScanner) (Revision, error) {
	var r Revision
	err := s.Scan(&r.ID, &r.PRID, &r.Seq, &r.HeadSHA, &r.BaseSHA,
		&r.ObservedAt, &r.LastSeenAt, &r.CIState, &r.CIPassed, &r.CIFailed,
		&r.CIPending, &r.CICapturedAt,
		&r.GateState, &r.GateStateN, &r.GateStateM, &r.GateStateCapturedAt)
	return r, err
}

// samePair reports whether two (head, base) pairs are the same revision. A NULL
// (empty) base on either side degrades to a head-only comparison.
func samePair(aHead, aBase, bHead, bBase string) bool {
	if aHead != bHead {
		return false
	}
	if aBase == "" || bBase == "" {
		return true
	}
	return aBase == bBase
}

// RecordRevision appends a new revision when (headSHA, baseSHA) differs from the
// PR's latest revision, otherwise bumps the latest revision's last_seen_at. It
// returns the resulting latest revision and whether a new row was appended.
func (db *DB) RecordRevision(ctx context.Context, prID int64, headSHA, baseSHA string) (Revision, bool, error) {
	var result Revision
	var appended bool
	err := db.InTx(ctx, func(tx *Tx) error {
		now := nowRFC3339()
		latest, err := tx.latestRevision(prID)
		if err != nil {
			return err
		}
		if latest != nil && samePair(latest.HeadSHA, latest.BaseSHA, headSHA, baseSHA) {
			if _, err := tx.Exec(`UPDATE pr_revision SET last_seen_at=? WHERE id=?`, now, latest.ID); err != nil {
				return fmt.Errorf("store: touch revision: %w", err)
			}
			latest.LastSeenAt = now
			result = *latest
			return nil
		}
		seq := 1
		if latest != nil {
			seq = latest.Seq + 1
		}
		var base any
		if baseSHA != "" {
			base = baseSHA
		}
		res, err := tx.Exec(`INSERT INTO pr_revision
			(pr_id, seq, head_sha, base_sha, observed_at, last_seen_at)
			VALUES (?,?,?,?,?,?)`, prID, seq, headSHA, base, now, now)
		if err != nil {
			return fmt.Errorf("store: append revision: %w", err)
		}
		id, _ := res.LastInsertId()
		appended = true
		result = Revision{
			ID: id, PRID: prID, Seq: seq, HeadSHA: headSHA,
			BaseSHA: baseSHA, ObservedAt: now, LastSeenAt: now, CIState: "none",
			GateState: "unknown",
		}
		return nil
	})
	return result, appended, err
}

// CIRollup is the compact CI summary captured for a revision's head SHA.
type CIRollup struct {
	State      string // none|pending|success|failure|error
	Passed     int
	Failed     int
	Pending    int
	CapturedAt string
}

// SetRevisionCI overwrites the CI rollup on a revision (idempotent).
func (db *DB) SetRevisionCI(ctx context.Context, revisionID int64, r CIRollup) error {
	state := r.State
	if state == "" {
		state = "none"
	}
	var capturedAt any
	if r.CapturedAt != "" {
		capturedAt = r.CapturedAt
	}
	_, err := db.sql.ExecContext(ctx, `UPDATE pr_revision
		SET ci_state=?, ci_passed=?, ci_failed=?, ci_pending=?, ci_captured_at=?
		WHERE id=?`, state, r.Passed, r.Failed, r.Pending, capturedAt, revisionID)
	if err != nil {
		return fmt.Errorf("store: set revision ci %d: %w", revisionID, err)
	}
	return nil
}

// GateState is the approval-gate's overall verdict for a revision, persisted
// by SetRevisionGateState and read back via the GateState* fields on Revision
// (schema v11, pg2-4dz88.2.5). Distinct from CIRollup: a CI suite passing does
// not mean an approval-gate policy is satisfied, and vice versa — see
// migrate.go's v10->v11 step for the full rationale.
type GateState struct {
	// State is one of "satisfied" | "partially-satisfied" | "unsatisfied" |
	// "unknown" (CHECK-constrained; see migrate.go). Empty defaults to
	// "unknown", mirroring CIRollup.State's "" -> "none" default.
	State string
	// N and M are the gate's satisfied/total counts, e.g.
	// partially-satisfied(n,m) or unsatisfied(0,m). SetRevisionGateState
	// persists them ONLY when State is "partially-satisfied" or
	// "unsatisfied" — the only two states that carry the pair; for
	// "satisfied"/"unknown" they are stored as NULL regardless of what is
	// passed here, since those states never carry a reading.
	N, M int
	// CapturedAt is when this verdict was observed; "" leaves the column
	// NULL. Mirrors CIRollup.CapturedAt: the gate can be re-evaluated for an
	// existing revision without a new revision being appended.
	CapturedAt string
}

// SetRevisionGateState overwrites the approval-gate verdict on a revision
// (idempotent), mirroring SetRevisionCI. N/M are written only for the two
// states that carry the pair (see GateState.N); for "satisfied"/"unknown"
// they are stored as NULL so a state that never observed a count can never be
// misread as "0 of 0".
func (db *DB) SetRevisionGateState(ctx context.Context, revisionID int64, g GateState) error {
	state := g.State
	if state == "" {
		state = "unknown"
	}
	var n, m any
	if state == "partially-satisfied" || state == "unsatisfied" {
		n, m = g.N, g.M
	}
	var capturedAt any
	if g.CapturedAt != "" {
		capturedAt = g.CapturedAt
	}
	_, err := db.sql.ExecContext(ctx, `UPDATE pr_revision
		SET gate_state=?, gate_state_n=?, gate_state_m=?, gate_state_captured_at=?
		WHERE id=?`, state, n, m, capturedAt, revisionID)
	if err != nil {
		return fmt.Errorf("store: set revision gate state %d: %w", revisionID, err)
	}
	return nil
}

// ListRevisions returns a PR's revisions in ascending seq order.
func (db *DB) ListRevisions(ctx context.Context, prID int64) ([]Revision, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+revisionColumns+`
		FROM pr_revision WHERE pr_id=? ORDER BY seq ASC`, prID)
	if err != nil {
		return nil, fmt.Errorf("store: list revisions %d: %w", prID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []Revision
	for rows.Next() {
		r, err := scanRevision(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan revision: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LatestRevision returns the highest-seq revision for a PR, or nil if none.
func (db *DB) LatestRevision(ctx context.Context, prID int64) (*Revision, error) {
	row := db.sql.QueryRowContext(ctx, `SELECT `+revisionColumns+`
		FROM pr_revision WHERE pr_id=? ORDER BY seq DESC LIMIT 1`, prID)
	r, err := scanRevision(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: latest revision %d: %w", prID, err)
	}
	return &r, nil
}

// latestRevision returns the highest-seq revision for prID, or nil if none.
func (t *Tx) latestRevision(prID int64) (*Revision, error) {
	row := t.QueryRow(`SELECT `+revisionColumns+`
		FROM pr_revision WHERE pr_id=? ORDER BY seq DESC LIMIT 1`, prID)
	r, err := scanRevision(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: latest revision: %w", err)
	}
	return &r, nil
}

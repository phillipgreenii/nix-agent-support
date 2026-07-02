package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Revision is one observed (head_sha, base_sha) of a PR in time order.
type Revision struct {
	ID            int64
	PRID          int64
	Seq           int
	HeadSHA       string
	BaseSHA       string // "" when NULL
	ObservedAt    string
	LastSeenAt    string
	CIState       string
	CIPassed      int
	CIFailed      int
	CIPending     int
	CICapturedAt  string // "" when NULL
	ReviewedAt    string // "" when NULL
	MyReviewState string // "" when NULL
	// ReviewedByAgentAt is the timestamp the daemon's draft-review consumer
	// (pg2-4c5i.36) recorded an agent review against this revision's head SHA.
	// "" when NULL (no agent review produced against this head yet).
	ReviewedByAgentAt string
	// OthersApproved is true when a NON-SELF (teammate) APPROVED review was
	// observed at this revision's head SHA (pg2-4c5i.13). Deliberately excludes
	// the viewer's own approval (that lives in my_review_state) so the attention
	// predicate can tell "a teammate approved" from "I approved" (X3).
	OthersApproved bool
	// OthersApprovedAt is the timestamp of the recorded teammate approval; ""
	// when NULL (no teammate approval observed at this head yet).
	OthersApprovedAt string
}

const revisionColumns = `id, pr_id, seq, head_sha, COALESCE(base_sha,''),
	observed_at, last_seen_at, ci_state, ci_passed, ci_failed, ci_pending,
	COALESCE(ci_captured_at,''), COALESCE(reviewed_at,''), COALESCE(my_review_state,''),
	COALESCE(reviewed_by_agent_at,''), others_approved, COALESCE(others_approved_at,'')`

func scanRevision(s rowScanner) (Revision, error) {
	var r Revision
	err := s.Scan(&r.ID, &r.PRID, &r.Seq, &r.HeadSHA, &r.BaseSHA,
		&r.ObservedAt, &r.LastSeenAt, &r.CIState, &r.CIPassed, &r.CIFailed,
		&r.CIPending, &r.CICapturedAt, &r.ReviewedAt, &r.MyReviewState,
		&r.ReviewedByAgentAt, &r.OthersApproved, &r.OthersApprovedAt)
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

// MarkRevisionReviewed records my submitted review at headSHA on the latest
// revision whose head_sha matches (a head SHA can recur after a force-push; #3
// cares about the most recent occurrence). No-op if no revision matches.
func (db *DB) MarkRevisionReviewed(ctx context.Context, prID int64, headSHA, state, reviewedAt string) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE pr_revision
		SET reviewed_at=?, my_review_state=?
		WHERE id = (SELECT id FROM pr_revision
		            WHERE pr_id=? AND head_sha=? ORDER BY seq DESC LIMIT 1)`,
		reviewedAt, state, prID, headSHA)
	if err != nil {
		return fmt.Errorf("store: mark revision reviewed %d %s: %w", prID, headSHA, err)
	}
	return nil
}

// MarkRevisionAgentReviewed records that the daemon's draft-review consumer
// produced an agent review against headSHA, on the latest revision whose
// head_sha matches (a head SHA can recur after a force-push; we care about the
// most recent occurrence). No-op if no revision matches. Mirrors
// MarkRevisionReviewed but records the *agent* review marker (semantics differ
// from my-submitted-GitHub-review's reviewed_at/my_review_state).
func (db *DB) MarkRevisionAgentReviewed(ctx context.Context, prID int64, headSHA, at string) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE pr_revision
		SET reviewed_by_agent_at=?
		WHERE id = (SELECT id FROM pr_revision
		            WHERE pr_id=? AND head_sha=? ORDER BY seq DESC LIMIT 1)`,
		at, prID, headSHA)
	if err != nil {
		return fmt.Errorf("store: mark revision agent-reviewed %d %s: %w", prID, headSHA, err)
	}
	return nil
}

// MarkRevisionOthersApproved records that a NON-SELF (teammate) APPROVED review
// was observed at headSHA, on the latest revision whose head_sha matches (a head
// SHA can recur after a force-push; we care about the most recent occurrence).
// No-op if no revision matches. Mirrors MarkRevisionReviewed/AgentReviewed but
// records the *teammate*-approval marker — deliberately distinct from
// my_review_state (the viewer's own review) so the attention predicate can tell
// "a teammate approved" from "I approved" (pg2-4c5i.13, X3).
func (db *DB) MarkRevisionOthersApproved(ctx context.Context, prID int64, headSHA, at string) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE pr_revision
		SET others_approved=1, others_approved_at=?
		WHERE id = (SELECT id FROM pr_revision
		            WHERE pr_id=? AND head_sha=? ORDER BY seq DESC LIMIT 1)`,
		at, prID, headSHA)
	if err != nil {
		return fmt.Errorf("store: mark revision others-approved %d %s: %w", prID, headSHA, err)
	}
	return nil
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

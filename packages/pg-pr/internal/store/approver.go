package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Approval is one approver's latest observed review state on a PR
// (pg2-4dz88.1.5, schema v9). Unlike pr_revision.my_review_state (a single
// self-only slot) and pr_revision.others_approved (a single boolean OR across
// every non-self APPROVED review), pr_approval carries one row PER
// (pr_id, approver login) — two teammates approving are two distinct,
// distinguishable rows, and per-approver staleness ("Alice approved head N,
// Bob has not re-approved head N+1") is representable by comparing HeadSHA
// against the PR's current head (see IsStale).
//
// This table is additive alongside the old pr_revision columns: they are
// still written by internal/sync's write path and remain the read path until
// a later leaf cuts readers over to this table.
type Approval struct {
	ID         int64
	PRID       int64
	Approver   string
	State      string // approved|changes-requested|commented
	HeadSHA    string // the head SHA the state was observed at
	ObservedAt string
}

const approvalColumns = `id, pr_id, approver, state, head_sha, observed_at`

func scanApproval(s rowScanner) (Approval, error) {
	var a Approval
	err := s.Scan(&a.ID, &a.PRID, &a.Approver, &a.State, &a.HeadSHA, &a.ObservedAt)
	return a, err
}

// SetApproval upserts the latest observed review state for (prID, approver):
// a later observation from the SAME approver replaces the existing row in
// place (UNIQUE(pr_id, approver)) rather than appending a new one, so
// re-approving a later head UPDATES, not duplicates.
func (db *DB) SetApproval(ctx context.Context, prID int64, approver, headSHA, state, observedAt string) error {
	_, err := db.sql.ExecContext(ctx, `INSERT INTO pr_approval (pr_id, approver, state, head_sha, observed_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(pr_id, approver) DO UPDATE SET
			state=excluded.state, head_sha=excluded.head_sha, observed_at=excluded.observed_at`,
		prID, approver, state, headSHA, observedAt)
	if err != nil {
		return fmt.Errorf("store: set approval %d %s: %w", prID, approver, err)
	}
	return nil
}

// GetApproval returns one approver's latest record on prID, or nil if that
// approver has never been recorded on this PR.
func (db *DB) GetApproval(ctx context.Context, prID int64, approver string) (*Approval, error) {
	row := db.sql.QueryRowContext(ctx, `SELECT `+approvalColumns+`
		FROM pr_approval WHERE pr_id=? AND approver=?`, prID, approver)
	a, err := scanApproval(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get approval %d %s: %w", prID, approver, err)
	}
	return &a, nil
}

// ListApprovals returns every approver's latest record on prID, ordered by
// approver login for deterministic output.
func (db *DB) ListApprovals(ctx context.Context, prID int64) ([]Approval, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+approvalColumns+`
		FROM pr_approval WHERE pr_id=? ORDER BY approver ASC`, prID)
	if err != nil {
		return nil, fmt.Errorf("store: list approvals %d: %w", prID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan approval: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// IsStale reports whether this approval's HeadSHA differs from currentHeadSHA
// — i.e. the approver reviewed an earlier head and has not reviewed again
// since. An empty currentHeadSHA has nothing to compare against and is never
// reported stale.
func (a Approval) IsStale(currentHeadSHA string) bool {
	if currentHeadSHA == "" {
		return false
	}
	return a.HeadSHA != currentHeadSHA
}

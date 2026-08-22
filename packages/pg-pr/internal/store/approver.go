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
// This table is THE read path for approvals as of pg2-4dz88.1.9: both
// snapshot.classifyApprovals and the shared snapshot.NeedsAttention predicate
// read it, and neither reads pr_revision.others_approved or
// pr_revision.my_review_state any more. Those columns are still WRITTEN by
// internal/sync's write path but no longer read by anything outside this
// package; dropping them is a separate migration leaf.
type Approval struct {
	ID         int64
	PRID       int64
	Approver   string
	State      string // approved|changes-requested|commented
	HeadSHA    string // the head SHA the state was observed at
	ObservedAt string
	// Dismissed is true when the code host reported this approver's review as
	// DISMISSED (schema v10, pg2-4dz88.1.7). Such a row is a STALE approval,
	// never an absent one (INV-APPROVAL-3): State stays "approved" so a reader
	// asking "did this approver approve?" still sees it, and IsStale reports
	// it stale REGARDLESS of HeadSHA — the host can dismiss a review without
	// the head moving, so head comparison alone cannot detect it.
	Dismissed bool
}

const approvalColumns = `id, pr_id, approver, state, head_sha, observed_at, dismissed`

func scanApproval(s rowScanner) (Approval, error) {
	var a Approval
	err := s.Scan(&a.ID, &a.PRID, &a.Approver, &a.State, &a.HeadSHA, &a.ObservedAt, &a.Dismissed)
	return a, err
}

// SetApproval upserts the latest observed review state for (prID, approver):
// a later observation from the SAME approver replaces the existing row in
// place (UNIQUE(pr_id, approver)) rather than appending a new one, so
// re-approving a later head UPDATES, not duplicates. The row is recorded as
// NOT dismissed, so a fresh observation from an approver whose earlier review
// had been dismissed CLEARS that dismissal.
func (db *DB) SetApproval(ctx context.Context, prID int64, approver, headSHA, state, observedAt string) error {
	return db.setApproval(ctx, prID, approver, headSHA, state, observedAt, false)
}

// SetDismissedApproval records a review the code host reports as DISMISSED for
// (prID, approver) as a STALE approval (pg2-4dz88.1.7). Per INV-APPROVAL-3 a
// dismissed review MUST be read as a stale approval, never as an absent one,
// so the row is written with state "approved" plus the dismissed marker rather
// than being dropped: the approver DID approve, and their approval no longer
// stands. The host does not report what the review said before it was
// dismissed, which is why "approved" is the only state it can carry.
//
// It shares SetApproval's upsert semantics, so a later re-approval from the
// same approver replaces this row and clears the dismissal.
func (db *DB) SetDismissedApproval(ctx context.Context, prID int64, approver, headSHA, observedAt string) error {
	return db.setApproval(ctx, prID, approver, headSHA, "approved", observedAt, true)
}

func (db *DB) setApproval(ctx context.Context, prID int64, approver, headSHA, state, observedAt string, dismissed bool) error {
	_, err := db.sql.ExecContext(ctx, `INSERT INTO pr_approval (pr_id, approver, state, head_sha, observed_at, dismissed)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(pr_id, approver) DO UPDATE SET
			state=excluded.state, head_sha=excluded.head_sha, observed_at=excluded.observed_at,
			dismissed=excluded.dismissed`,
		prID, approver, state, headSHA, observedAt, b2i(dismissed))
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

// IsStale reports whether this approval no longer stands for the PR's current
// head, for either of two independent reasons:
//
//   - the code host DISMISSED it (Dismissed) — head-INDEPENDENT, since a review
//     can be dismissed without the head moving, so this dominates the head
//     comparison below and even an empty currentHeadSHA reports stale; and
//   - this approval's HeadSHA differs from currentHeadSHA — the approver
//     reviewed an earlier head and has not reviewed again since.
//
// An empty currentHeadSHA has nothing to compare against, so a non-dismissed
// approval is never reported stale for that reason alone.
//
// Both readings are "stale, not absent" (INV-APPROVAL-3): the row itself is
// the record that this approver DID approve.
func (a Approval) IsStale(currentHeadSHA string) bool {
	if a.Dismissed {
		return true
	}
	if currentHeadSHA == "" {
		return false
	}
	return a.HeadSHA != currentHeadSHA
}

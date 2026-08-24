package store

import (
	"context"
	"fmt"
)

// SetHidden writes ONLY the USER_HIDDEN flag (+ optional reason) for an
// existing PR row via a targeted UPDATE, mirroring SetEnrichment's shape
// (pull_request.go). These columns are deliberately absent from UpsertPR's ON
// CONFLICT clause, so a sync tick can never clobber a user's hide decision
// (schema v14, pg2-4dz88.4.2).
//
// USER_HIDDEN is a display-layer-only concept: it never affects ingestion,
// and it is intentionally a distinct name from the pre-existing "hidden TEAM
// draft" mechanism in internal/sync/refresh.go and
// internal/snapshot/attention.go, which DOES skip ingestion (operator
// ruling, fork #7 — see pg2-4dz88.4.2's comments).
//
// Unhiding ALWAYS clears the recorded reason: passing hidden=false forces
// reason="" regardless of what is supplied (operator ruling, fork #5) — the
// reason is meaningless once the PR is no longer hidden, and retaining a
// stale one would let a later re-hide silently inherit unrelated context.
//
// Returns an error if no row matches (repo, number), matching
// SetDisposition's fail-loud pattern (feedback.go) rather than
// SetEnrichment's silent no-op (operator ruling, fork #6).
func (db *DB) SetHidden(ctx context.Context, repo string, number int, hidden bool, reason string) error {
	if !hidden {
		reason = ""
	}
	res, err := db.sql.ExecContext(ctx, `
UPDATE pull_request SET user_hidden=?, user_hidden_reason=?, updated_at=?
WHERE repo=? AND number=?`,
		b2i(hidden), reason, nowRFC3339(), repo, number)
	if err != nil {
		return fmt.Errorf("store: set hidden %s#%d: %w", repo, number, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("store: pr %s#%d not found", repo, number)
	}
	return nil
}

// SetWIP writes ONLY the WIP flag for an existing PR row via a targeted
// UPDATE, mirroring SetEnrichment's shape. Deliberately absent from
// UpsertPR's ON CONFLICT clause, so a sync tick can never clobber it (schema
// v14, pg2-4dz88.4.2).
//
// WIP is store-only and is never synced to beads. Toggling it drives the
// ready<->draft conversions described in pg2-4dz88.4's "WIP semantics" — a
// sibling leaf, not implemented here.
//
// Returns an error if no row matches (repo, number), matching
// SetDisposition's fail-loud pattern (feedback.go) rather than
// SetEnrichment's silent no-op (operator ruling, fork #6 — the same ruling
// SetHidden follows).
func (db *DB) SetWIP(ctx context.Context, repo string, number int, wip bool) error {
	res, err := db.sql.ExecContext(ctx, `
UPDATE pull_request SET wip=?, updated_at=?
WHERE repo=? AND number=?`,
		b2i(wip), nowRFC3339(), repo, number)
	if err != nil {
		return fmt.Errorf("store: set wip %s#%d: %w", repo, number, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("store: pr %s#%d not found", repo, number)
	}
	return nil
}

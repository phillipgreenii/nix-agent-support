package store

import (
	"database/sql"
	"fmt"
)

// LedgerEntry is one row of the ledger table: the mapping from an entity to
// a bead id by kind (e.g. "anchor", "feedback-cycle", "review-request"),
// plus sync bookkeeping (last synced content hash/time, last reviewed head
// SHA), per the design doc's section 7.6.
//
// This phase (Phase 9) creates the ledger table and its writer/reader only
// — the sync stage that actually populates/consumes it (advancing
// LastSyncedContentHash, LastSyncedAt, LastReviewedHeadSHA as PRs change)
// is Phase 10 (this packet's Out of scope section). Nothing in this
// package writes ledger rows outside of tests.
type LedgerEntry struct {
	Repo       string
	EntityType string
	EntityID   string
	Kind       string
	BeadID     string

	LastSyncedContentHash string
	LastSyncedAt          string // RFC3339 timestamp
	LastReviewedHeadSHA   string
}

// UpsertLedger inserts or replaces the ledger row keyed by
// (Repo, EntityType, EntityID, Kind).
func (s *Store) UpsertLedger(l LedgerEntry) error {
	_, err := s.sql.Exec(
		`INSERT INTO ledger (repo, entity_type, entity_id, kind, bead_id, last_synced_content_hash, last_synced_at, last_reviewed_head_sha)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (repo, entity_type, entity_id, kind) DO UPDATE SET
		   bead_id = excluded.bead_id,
		   last_synced_content_hash = excluded.last_synced_content_hash,
		   last_synced_at = excluded.last_synced_at,
		   last_reviewed_head_sha = excluded.last_reviewed_head_sha`,
		l.Repo, l.EntityType, l.EntityID, l.Kind, l.BeadID,
		nullableString(l.LastSyncedContentHash), nullableString(l.LastSyncedAt), nullableString(l.LastReviewedHeadSHA),
	)
	if err != nil {
		return fmt.Errorf("store: upsert ledger (%s,%s,%s,%s): %w", l.Repo, l.EntityType, l.EntityID, l.Kind, err)
	}
	return nil
}

// ListLedger returns every ledger row, ordered by (repo, entity_type,
// entity_id, kind) for a deterministic result — mirrors
// interpretation.go's own ListInterpretations exactly. Added by docket
// pg2-2j5ac.34 (Phase 10, sync) for `pg-desk status`'s "planned sync rows
// by kind" surface [design 7.7]: a per-PR read (GetLedger, by repo/type/id/
// kind) cannot answer an aggregate "how many planned rows exist" question,
// and status's own scope is the whole store, not one PR.
func (s *Store) ListLedger() ([]LedgerEntry, error) {
	rows, err := s.sql.Query(
		`SELECT repo, entity_type, entity_id, kind, bead_id, last_synced_content_hash, last_synced_at, last_reviewed_head_sha
		 FROM ledger ORDER BY repo, entity_type, entity_id, kind`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list ledger: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []LedgerEntry
	for rows.Next() {
		var entry LedgerEntry
		var lastSyncedContentHash, lastSyncedAt, lastReviewedHeadSHA sql.NullString
		if err := rows.Scan(&entry.Repo, &entry.EntityType, &entry.EntityID, &entry.Kind, &entry.BeadID,
			&lastSyncedContentHash, &lastSyncedAt, &lastReviewedHeadSHA); err != nil {
			return nil, fmt.Errorf("store: scan ledger row: %w", err)
		}
		entry.LastSyncedContentHash = lastSyncedContentHash.String
		entry.LastSyncedAt = lastSyncedAt.String
		entry.LastReviewedHeadSHA = lastReviewedHeadSHA.String
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list ledger: %w", err)
	}
	return out, nil
}

// GetLedger returns the ledger row for
// (repo, entityType, entityID, kind), or found=false if no such row exists.
func (s *Store) GetLedger(repo, entityType, entityID, kind string) (entry LedgerEntry, found bool, err error) {
	var lastSyncedContentHash, lastSyncedAt, lastReviewedHeadSHA sql.NullString
	row := s.sql.QueryRow(
		`SELECT repo, entity_type, entity_id, kind, bead_id, last_synced_content_hash, last_synced_at, last_reviewed_head_sha
		 FROM ledger WHERE repo = ? AND entity_type = ? AND entity_id = ? AND kind = ?`,
		repo, entityType, entityID, kind,
	)
	if err := row.Scan(&entry.Repo, &entry.EntityType, &entry.EntityID, &entry.Kind, &entry.BeadID,
		&lastSyncedContentHash, &lastSyncedAt, &lastReviewedHeadSHA); err != nil {
		if err == sql.ErrNoRows {
			return LedgerEntry{}, false, nil
		}
		return LedgerEntry{}, false, fmt.Errorf("store: get ledger (%s,%s,%s,%s): %w", repo, entityType, entityID, kind, err)
	}
	entry.LastSyncedContentHash = lastSyncedContentHash.String
	entry.LastSyncedAt = lastSyncedAt.String
	entry.LastReviewedHeadSHA = lastReviewedHeadSHA.String
	return entry, true, nil
}

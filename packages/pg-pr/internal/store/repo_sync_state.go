package store

import (
	"context"
	"fmt"
)

// RepoSyncState is the sync engine's per-repo cursor: when a repo was last
// synced, and — if that attempt failed — the resulting error. It replaces
// the old $XDG_STATE_HOME/pg-pr/repo-state.json file (pg2-ynhr.8, schema
// v17): sync state now lives in the same SQLite database as the PR data it
// describes, so the two can never drift out of sync from a crash between two
// independent file writes.
//
// LastErrorCode/LastErrorMessage are both "" when the most recent sync
// attempt for this repo succeeded — there is no separate "no error" flag.
type RepoSyncState struct {
	Repo             string
	LastSyncedAt     string
	LastErrorCode    string
	LastErrorMessage string
}

// UpsertRepoSyncState records one repo's sync outcome for this tick,
// creating the row on first observation and overwriting it thereafter.
func (db *DB) UpsertRepoSyncState(ctx context.Context, st RepoSyncState) error {
	_, err := db.sql.ExecContext(ctx, `
INSERT INTO repo_sync_state (repo, last_synced_at, last_error_code, last_error_message)
VALUES (?, ?, ?, ?)
ON CONFLICT (repo) DO UPDATE SET
    last_synced_at = excluded.last_synced_at,
    last_error_code = excluded.last_error_code,
    last_error_message = excluded.last_error_message`,
		st.Repo, st.LastSyncedAt, st.LastErrorCode, st.LastErrorMessage)
	if err != nil {
		return fmt.Errorf("store: upsert repo sync state %s: %w", st.Repo, err)
	}
	return nil
}

// RepoSyncStates returns every repo's recorded sync state, ordered by repo
// name for deterministic output.
func (db *DB) RepoSyncStates(ctx context.Context) ([]RepoSyncState, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT repo, last_synced_at, last_error_code, last_error_message
FROM repo_sync_state
ORDER BY repo`)
	if err != nil {
		return nil, fmt.Errorf("store: list repo sync states: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []RepoSyncState
	for rows.Next() {
		var st RepoSyncState
		if err := rows.Scan(&st.Repo, &st.LastSyncedAt, &st.LastErrorCode, &st.LastErrorMessage); err != nil {
			return nil, fmt.Errorf("store: scan repo sync state: %w", err)
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate repo sync states: %w", err)
	}
	return out, nil
}

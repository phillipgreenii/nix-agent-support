package store

import (
	"context"
	"testing"
)

// Schema v17 folds the sync engine's per-repo cursor (previously a separate
// $XDG_STATE_HOME/pg-pr/repo-state.json file) into this store (pg2-ynhr.8).
// The table is brand new (no prior column to migrate), so a fresh
// (from-empty) database — every OpenForTest call in this package — migrates
// straight through this step to schemaVersion.
func TestMigrate_V17RepoSyncStateTable(t *testing.T) {
	db := OpenForTest(t)

	var v int
	if err := db.sql.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if v != schemaVersion || schemaVersion < 17 {
		t.Fatalf("user_version=%d schemaVersion=%d; want both >= 17", v, schemaVersion)
	}

	var cnt int
	if err := db.sql.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='repo_sync_state'",
	).Scan(&cnt); err != nil {
		t.Fatalf("sqlite_master lookup: %v", err)
	}
	if cnt != 1 {
		t.Fatal("repo_sync_state table missing after migration")
	}

	// Idempotent re-migrate.
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestUpsertRepoSyncState_CreatesRow(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)

	if err := db.UpsertRepoSyncState(ctx, RepoSyncState{
		Repo:         "foo/bar",
		LastSyncedAt: "2026-08-29T00:00:00Z",
	}); err != nil {
		t.Fatalf("UpsertRepoSyncState: %v", err)
	}

	states, err := db.RepoSyncStates(ctx)
	if err != nil {
		t.Fatalf("RepoSyncStates: %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("want 1 repo sync state, got %d (%+v)", len(states), states)
	}
	got := states[0]
	if got.Repo != "foo/bar" || got.LastSyncedAt != "2026-08-29T00:00:00Z" {
		t.Fatalf("unexpected row: %+v", got)
	}
	if got.LastErrorCode != "" || got.LastErrorMessage != "" {
		t.Fatalf("expected no error on a clean sync, got %+v", got)
	}
}

func TestUpsertRepoSyncState_RecordsError(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)

	if err := db.UpsertRepoSyncState(ctx, RepoSyncState{
		Repo:             "foo/bar",
		LastSyncedAt:     "2026-08-29T00:00:00Z",
		LastErrorCode:    "enum_failed",
		LastErrorMessage: "gh auth required",
	}); err != nil {
		t.Fatalf("UpsertRepoSyncState: %v", err)
	}

	states, err := db.RepoSyncStates(ctx)
	if err != nil {
		t.Fatalf("RepoSyncStates: %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("want 1 repo sync state, got %d", len(states))
	}
	if states[0].LastErrorCode != "enum_failed" || states[0].LastErrorMessage != "gh auth required" {
		t.Fatalf("unexpected error fields: %+v", states[0])
	}
}

// A later UPSERT for the same repo overwrites the row in place rather than
// creating a second one — this is what lets a repo recover from a failed
// tick to a clean one (and vice versa) without ever accumulating history.
func TestUpsertRepoSyncState_OverwritesOnRepeat(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)

	if err := db.UpsertRepoSyncState(ctx, RepoSyncState{
		Repo:             "foo/bar",
		LastSyncedAt:     "2026-08-29T00:00:00Z",
		LastErrorCode:    "enum_failed",
		LastErrorMessage: "gh auth required",
	}); err != nil {
		t.Fatalf("first UpsertRepoSyncState: %v", err)
	}
	// Recovers on the next tick: no error this time.
	if err := db.UpsertRepoSyncState(ctx, RepoSyncState{
		Repo:         "foo/bar",
		LastSyncedAt: "2026-08-29T00:05:00Z",
	}); err != nil {
		t.Fatalf("second UpsertRepoSyncState: %v", err)
	}

	states, err := db.RepoSyncStates(ctx)
	if err != nil {
		t.Fatalf("RepoSyncStates: %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("want exactly 1 row (upsert, not insert), got %d: %+v", len(states), states)
	}
	got := states[0]
	if got.LastSyncedAt != "2026-08-29T00:05:00Z" {
		t.Fatalf("LastSyncedAt not overwritten: %+v", got)
	}
	if got.LastErrorCode != "" || got.LastErrorMessage != "" {
		t.Fatalf("recovered tick should clear the previous error, got %+v", got)
	}
}

// A repo this tick never touched keeps whatever row a previous tick wrote —
// there is no merge-with-previous-state step (unlike the old JSON file's
// read-modify-write-whole-file cycle) because each repo's row is independent.
func TestRepoSyncStates_PreservesUntouchedRepos(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)

	if err := db.UpsertRepoSyncState(ctx, RepoSyncState{Repo: "mono/a", LastSyncedAt: "t1"}); err != nil {
		t.Fatalf("seed mono/a: %v", err)
	}
	if err := db.UpsertRepoSyncState(ctx, RepoSyncState{Repo: "mono/b", LastSyncedAt: "t1"}); err != nil {
		t.Fatalf("seed mono/b: %v", err)
	}

	// Only mono/b is re-synced this tick.
	if err := db.UpsertRepoSyncState(ctx, RepoSyncState{Repo: "mono/b", LastSyncedAt: "t2"}); err != nil {
		t.Fatalf("re-sync mono/b: %v", err)
	}

	states, err := db.RepoSyncStates(ctx)
	if err != nil {
		t.Fatalf("RepoSyncStates: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("want 2 repos, got %d: %+v", len(states), states)
	}
	// Ordered by repo name.
	if states[0].Repo != "mono/a" || states[0].LastSyncedAt != "t1" {
		t.Fatalf("mono/a untouched-repo row: %+v", states[0])
	}
	if states[1].Repo != "mono/b" || states[1].LastSyncedAt != "t2" {
		t.Fatalf("mono/b re-synced row: %+v", states[1])
	}
}

func TestRepoSyncStates_EmptyWhenNoneRecorded(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)

	states, err := db.RepoSyncStates(ctx)
	if err != nil {
		t.Fatalf("RepoSyncStates: %v", err)
	}
	if len(states) != 0 {
		t.Fatalf("want 0 repo sync states, got %d: %+v", len(states), states)
	}
}

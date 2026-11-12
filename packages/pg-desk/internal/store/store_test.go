package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenAppliesPragmas(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var journalMode string
	if err := s.sql.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}

	var busyTimeout int
	if err := s.sql.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if busyTimeout < 5000 {
		t.Fatalf("busy_timeout = %d, want >= 5000", busyTimeout)
	}
}

func TestOpenCreatesFileAndParentDir(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "nested", "pg-desk", "store.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("stat %s: %v", dbPath, err)
	}
}

// TestDefaultPathHonoursXDGStateHomeAndCreatesOnFirstRun is the smoke test
// this packet's Validation section requires: confirm that opening the
// store at DefaultPath(), under a temp XDG_STATE_HOME, actually creates
// $XDG_STATE_HOME/pg-desk/store.db on first run.
func TestDefaultPathHonoursXDGStateHomeAndCreatesOnFirstRun(t *testing.T) {
	xdgStateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdgStateHome)

	wantPath := filepath.Join(xdgStateHome, "pg-desk", "store.db")
	if got := DefaultPath(); got != wantPath {
		t.Fatalf("DefaultPath() = %q, want %q", got, wantPath)
	}

	if _, err := os.Stat(wantPath); err == nil {
		t.Fatalf("precondition: %s already exists before first run", wantPath)
	}

	s, err := Open(DefaultPath())
	if err != nil {
		t.Fatalf("Open(DefaultPath()): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("first run did not create %s: %v", wantPath, err)
	}
}

// TestOpenCreatesEveryTable asserts that a fresh store has every table of
// schema 2 (the full column shape is compared with a golden by
// TestFreshStoreShape).
func TestOpenCreatesEveryTable(t *testing.T) {
	s := OpenForTest(t)

	want := []string{"entity", "interpretation", "xref", "annotation", "meta", "change_log", "consumer", "ingest_failed", "focus_period", "focus_selection", "focus_draft", "focus_run"}
	for _, table := range want {
		var name string
		err := s.sql.QueryRowContext(context.Background(), "SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&name)
		if err != nil {
			t.Fatalf("table %q missing: %v", table, err)
		}
	}
}

// TestReopenIsIdempotentAndKeepsSchemaVersion re-opens an already created
// database (a store opened when user_version already equals schemaVersion must
// not re-create anything or error) and checks that meta carries the version.
func TestReopenIsIdempotentAndKeepsSchemaVersion(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open (re-migrate): %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	var userVersion int
	if err := s2.sql.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&userVersion); err != nil {
		t.Fatalf("query user_version: %v", err)
	}
	if userVersion != schemaVersion {
		t.Fatalf("user_version = %d, want %d", userVersion, schemaVersion)
	}

	value, found, err := s2.GetMeta(MetaKeySchemaVersion)
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if !found {
		t.Fatalf("meta.schema_version not set")
	}
	if value != "2" {
		t.Fatalf("meta.schema_version = %q, want %q", value, "2")
	}
}

func TestEntityRoundTrip(t *testing.T) {
	s := OpenForTest(t)

	e := Entity{
		Repo:        "owner/repo",
		EntityType:  "pull_request",
		EntityID:    "42",
		Facts:       `{"title":"fix bug"}`,
		AsOf:        "2026-09-16T00:00:00Z",
		Stale:       false,
		ContentHash: "abc123",
		HeadSHA:     "deadbeef",
	}
	if err := s.UpsertEntity(e); err != nil {
		t.Fatalf("UpsertEntity: %v", err)
	}

	got, found, err := s.GetEntity(e.Repo, e.EntityType, e.EntityID)
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if !found {
		t.Fatalf("GetEntity: not found")
	}
	// first_seen_at is stamped once, from as_of, by the first insert.
	e.FirstSeenAt = e.AsOf
	if got != e {
		t.Fatalf("GetEntity round-trip = %+v, want %+v", got, e)
	}

	// Upsert again with different facts to exercise the update path.
	e.Facts = `{"title":"fix bug v2"}`
	e.Stale = true
	if err := s.UpsertEntity(e); err != nil {
		t.Fatalf("UpsertEntity (update): %v", err)
	}
	got, found, err = s.GetEntity(e.Repo, e.EntityType, e.EntityID)
	if err != nil {
		t.Fatalf("GetEntity (after update): %v", err)
	}
	if !found || got != e {
		t.Fatalf("GetEntity after update = %+v, found=%v, want %+v", got, found, e)
	}
}

func TestEntityGetMissingNotFound(t *testing.T) {
	s := OpenForTest(t)
	_, found, err := s.GetEntity("owner/repo", "pull_request", "999")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if found {
		t.Fatalf("GetEntity: found = true, want false for missing row")
	}
}

// TestListEntitiesReturnsEveryRowOrdered proves ListEntities' own
// contract (added for `pg-desk sweep`, bead pg2-gznpe): every entity row
// currently in the store comes back, ordered deterministically by
// (repo, entity_type, entity_id) regardless of insertion order — mirrors
// TestInterpretationRoundTrip's sibling ListInterpretations ordering
// proof, one table over.
func TestListEntitiesReturnsEveryRowOrdered(t *testing.T) {
	s := OpenForTest(t)

	// Inserted out of order on purpose, so a passing test cannot be
	// explained by incidental SQLite scan order matching insertion order.
	third := Entity{Repo: "owner/repo", EntityType: "pr", EntityID: "9", Facts: `{}`, AsOf: "2026-09-25T00:00:00Z", ContentHash: "c9"}
	first := Entity{Repo: "owner/repo", EntityType: "pr", EntityID: "1", Facts: `{}`, AsOf: "2026-09-25T00:00:00Z", ContentHash: "c1"}
	second := Entity{Repo: "owner/repo", EntityType: "pr", EntityID: "5", Facts: `{}`, AsOf: "2026-09-25T00:00:00Z", ContentHash: "c5"}
	for _, e := range []Entity{third, first, second} {
		if err := s.UpsertEntity(e); err != nil {
			t.Fatalf("UpsertEntity(%s): %v", e.EntityID, err)
		}
	}

	got, err := s.ListEntities()
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListEntities returned %d rows, want 3: %+v", len(got), got)
	}
	wantOrder := []string{"1", "5", "9"}
	for i, want := range wantOrder {
		if got[i].EntityID != want {
			t.Fatalf("ListEntities[%d].EntityID = %q, want %q (full result: %+v)", i, got[i].EntityID, want, got)
		}
	}
}

// TestListEntitiesEmptyStoreReturnsNoRowsNoError proves the empty case —
// `pg-desk sweep` against a fresh store (nothing gathered yet) must not
// error, just report zero entities to sweep.
func TestListEntitiesEmptyStoreReturnsNoRowsNoError(t *testing.T) {
	s := OpenForTest(t)
	got, err := s.ListEntities()
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ListEntities on an empty store returned %d rows, want 0: %+v", len(got), got)
	}
}

func TestInterpretationRoundTrip(t *testing.T) {
	s := OpenForTest(t)

	i := Interpretation{
		Repo:           "owner/repo",
		EntityType:     "pull_request",
		EntityID:       "42",
		Ownership:      "mine",
		Enrichment:     `{"kind":"feature"}`,
		Urgency:        `{"level":"low"}`,
		Category:       "feature",
		Dispositions:   `[]`,
		Approvals:      `[]`,
		GateState:      "open",
		MatchReasons:   `["watch_label"]`,
		Panel:          "mine_awaiting_me",
		ReadyToPromote: false,
		Degraded:       false,
		AsOf:           "2026-09-16T00:00:00Z",
	}
	if err := s.UpsertInterpretation(i); err != nil {
		t.Fatalf("UpsertInterpretation: %v", err)
	}

	got, found, err := s.GetInterpretation(i.Repo, i.EntityType, i.EntityID)
	if err != nil {
		t.Fatalf("GetInterpretation: %v", err)
	}
	if !found {
		t.Fatalf("GetInterpretation: not found")
	}
	if got != i {
		t.Fatalf("GetInterpretation round-trip = %+v, want %+v", got, i)
	}
}

func TestXrefRoundTrip(t *testing.T) {
	s := OpenForTest(t)

	x := Xref{
		Repo:          "owner/repo",
		FromType:      "pull_request",
		FromID:        "42",
		ToType:        "issue",
		ToID:          "7",
		Evidence:      "branch name contains issue number",
		FirstSeen:     "2026-09-16T00:00:00Z",
		LastConfirmed: "2026-09-16T00:00:00Z",
	}
	if err := s.UpsertXref(x); err != nil {
		t.Fatalf("UpsertXref: %v", err)
	}

	got, found, err := s.GetXref(x.Repo, x.FromType, x.FromID, x.ToType, x.ToID)
	if err != nil {
		t.Fatalf("GetXref: %v", err)
	}
	if !found {
		t.Fatalf("GetXref: not found")
	}
	if got != x {
		t.Fatalf("GetXref round-trip = %+v, want %+v", got, x)
	}
}

func TestMetaRoundTrip(t *testing.T) {
	s := OpenForTest(t)

	if err := s.SetMeta(MetaKeyLastHeartbeat, "2026-09-16T00:00:00Z"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	got, found, err := s.GetMeta(MetaKeyLastHeartbeat)
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if !found {
		t.Fatalf("GetMeta: not found")
	}
	if got != "2026-09-16T00:00:00Z" {
		t.Fatalf("GetMeta = %q, want %q", got, "2026-09-16T00:00:00Z")
	}

	// Overwrite exercises the update path.
	if err := s.SetMeta(MetaKeyLastHeartbeat, "2026-09-16T01:00:00Z"); err != nil {
		t.Fatalf("SetMeta (overwrite): %v", err)
	}
	got, _, err = s.GetMeta(MetaKeyLastHeartbeat)
	if err != nil {
		t.Fatalf("GetMeta (after overwrite): %v", err)
	}
	if got != "2026-09-16T01:00:00Z" {
		t.Fatalf("GetMeta after overwrite = %q, want %q", got, "2026-09-16T01:00:00Z")
	}
}

func TestMetaGetMissingNotFound(t *testing.T) {
	s := OpenForTest(t)
	_, found, err := s.GetMeta("no-such-key")
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if found {
		t.Fatalf("GetMeta: found = true, want false for missing key")
	}
}

package store

import (
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
	if err := s.sql.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}

	var busyTimeout int
	if err := s.sql.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
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

// TestMigrateCreatesAllSixTables asserts the acceptance criterion "All six
// tables exist per migration; xref exists but is never written by this
// packet."
func TestMigrateCreatesAllSixTables(t *testing.T) {
	s := OpenForTest(t)

	want := []string{"entity", "interpretation", "xref", "annotation", "ledger", "meta"}
	for _, table := range want {
		var name string
		err := s.sql.QueryRow("SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&name)
		if err != nil {
			t.Fatalf("table %q missing: %v", table, err)
		}
	}
}

// TestMigrateIsIdempotentAndMirrorsSchemaVersion re-opens an already
// migrated database (the "migration ladder" case: a store opened when
// user_version already equals schemaVersion must not re-apply migrations
// or error) and checks that migrate mirrors schemaVersion into meta.
func TestMigrateIsIdempotentAndMirrorsSchemaVersion(t *testing.T) {
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
	if err := s2.sql.QueryRow("PRAGMA user_version").Scan(&userVersion); err != nil {
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
	if value != "1" {
		t.Fatalf("meta.schema_version = %q, want %q", value, "1")
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
		SyncError:      "", // Phase 9 never writes this; must round-trip as empty.
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
	if got.SyncError != "" {
		t.Fatalf("SyncError = %q, want empty (Phase 9 never writes it)", got.SyncError)
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

func TestAnnotationRoundTripPRLevelAndCommentLevel(t *testing.T) {
	s := OpenForTest(t)

	hidden := true
	prLevel := Annotation{
		Repo:         "owner/repo",
		EntityType:   "pull_request",
		EntityID:     "42",
		CommentID:    "",
		Hidden:       &hidden,
		HiddenReason: "not ready",
		SetBy:        "phillipgreenii",
		SetAt:        "2026-09-16T00:00:00Z",
	}
	if err := s.UpsertAnnotation(prLevel); err != nil {
		t.Fatalf("UpsertAnnotation (PR-level): %v", err)
	}

	got, found, err := s.GetPRAnnotation(prLevel.Repo, prLevel.EntityType, prLevel.EntityID)
	if err != nil {
		t.Fatalf("GetPRAnnotation: %v", err)
	}
	if !found {
		t.Fatalf("GetPRAnnotation: not found")
	}
	if got.Repo != prLevel.Repo || got.HiddenReason != prLevel.HiddenReason ||
		got.Hidden == nil || *got.Hidden != true || got.WIP != nil {
		t.Fatalf("GetPRAnnotation round-trip = %+v, want %+v", got, prLevel)
	}

	commentLevel := Annotation{
		Repo:        "owner/repo",
		EntityType:  "pull_request",
		EntityID:    "42",
		CommentID:   "c-123",
		Disposition: "will-fix",
		SetBy:       "phillipgreenii",
		SetAt:       "2026-09-16T00:05:00Z",
	}
	if err := s.UpsertAnnotation(commentLevel); err != nil {
		t.Fatalf("UpsertAnnotation (comment-level): %v", err)
	}

	got2, found2, err := s.GetAnnotation(commentLevel.Repo, commentLevel.EntityType, commentLevel.EntityID, commentLevel.CommentID)
	if err != nil {
		t.Fatalf("GetAnnotation: %v", err)
	}
	if !found2 {
		t.Fatalf("GetAnnotation: not found")
	}
	if got2.Disposition != commentLevel.Disposition || got2.Hidden != nil || got2.WIP != nil {
		t.Fatalf("GetAnnotation round-trip = %+v, want %+v", got2, commentLevel)
	}

	// The PR-level and comment-level rows must be independent.
	prAgain, found3, err := s.GetPRAnnotation(prLevel.Repo, prLevel.EntityType, prLevel.EntityID)
	if err != nil || !found3 {
		t.Fatalf("GetPRAnnotation after comment-level write: found=%v err=%v", found3, err)
	}
	if prAgain.HiddenReason != prLevel.HiddenReason {
		t.Fatalf("PR-level annotation mutated by comment-level write: %+v", prAgain)
	}
}

func TestLedgerRoundTrip(t *testing.T) {
	s := OpenForTest(t)

	l := LedgerEntry{
		Repo:                  "owner/repo",
		EntityType:            "pull_request",
		EntityID:              "42",
		Kind:                  "anchor",
		BeadID:                "pg2-abcde",
		LastSyncedContentHash: "abc123",
		LastSyncedAt:          "2026-09-16T00:00:00Z",
		LastReviewedHeadSHA:   "deadbeef",
	}
	if err := s.UpsertLedger(l); err != nil {
		t.Fatalf("UpsertLedger: %v", err)
	}

	got, found, err := s.GetLedger(l.Repo, l.EntityType, l.EntityID, l.Kind)
	if err != nil {
		t.Fatalf("GetLedger: %v", err)
	}
	if !found {
		t.Fatalf("GetLedger: not found")
	}
	if got != l {
		t.Fatalf("GetLedger round-trip = %+v, want %+v", got, l)
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

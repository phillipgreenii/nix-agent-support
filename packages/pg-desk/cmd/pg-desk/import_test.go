package main

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// fixturePR is one row inserted into the fixture pg-pr store's
// pull_request table by newFixturePgPrStore.
type fixturePR struct {
	repo         string
	number       int
	hidden       bool
	hiddenReason string
	wip          bool
}

// newFixturePgPrStore creates a minimal pg-pr store.db under t.TempDir()
// containing a pull_request table with exactly the three source columns
// pinned by this packet's Contract (user_hidden, user_hidden_reason,
// wip) — column names/types confirmed against
// packages/pg-pr/internal/store/migrate.go's ALTER TABLE statements
// (INTEGER for the two bool-shaped flags, TEXT for the reason) — then
// inserts rows. Deliberately independent of packages/pg-pr's own store
// package (see import_pg_pr_annotations.go's package doc comment for why):
// this fixture is built with the same modernc.org/sqlite driver pg-desk's
// own internal/store already depends on. Per this repo's unit-test
// isolation convention, the fixture lives entirely in a temp directory.
func newFixturePgPrStore(t *testing.T, rows []fixturePR) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pg-pr-store.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture pg-pr store: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Exec(`
CREATE TABLE pull_request (
    repo               TEXT    NOT NULL,
    number             INTEGER NOT NULL,
    user_hidden        INTEGER NOT NULL DEFAULT 0,
    user_hidden_reason TEXT    NOT NULL DEFAULT '',
    wip                INTEGER NOT NULL DEFAULT 0
)`); err != nil {
		t.Fatalf("create fixture pull_request table: %v", err)
	}

	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO pull_request (repo, number, user_hidden, user_hidden_reason, wip) VALUES (?, ?, ?, ?, ?)`,
			r.repo, r.number, boolToInt(r.hidden), r.hiddenReason, boolToInt(r.wip),
		); err != nil {
			t.Fatalf("insert fixture pull_request row: %v", err)
		}
	}
	return path
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// boolPtrEqual compares two *bool by value (nil-safe). store.Annotation's
// Hidden/WIP fields are *bool, and every read through
// Store.GetPRAnnotation allocates a fresh pointer (internal/store/annotation.go's
// GetAnnotation), so comparing two Annotation values read at different
// times with == would compare pointer identity, not the underlying flag —
// always "unequal" even when the stored value hasn't changed. Tests MUST
// compare through this helper (or annotationsEqual) instead of a bare `==`.
func boolPtrEqual(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// annotationsEqual compares two store.Annotation values field-by-field,
// dereferencing the pointer fields per boolPtrEqual's doc comment.
func annotationsEqual(a, b store.Annotation) bool {
	return a.Repo == b.Repo &&
		a.EntityType == b.EntityType &&
		a.EntityID == b.EntityID &&
		a.CommentID == b.CommentID &&
		boolPtrEqual(a.Hidden, b.Hidden) &&
		a.HiddenReason == b.HiddenReason &&
		boolPtrEqual(a.WIP, b.WIP) &&
		a.Disposition == b.Disposition &&
		a.SetBy == b.SetBy &&
		a.SetAt == b.SetAt
}

// TestImportPgPrAnnotationsRoundTrip is the acceptance criterion "fixture
// pg-pr store.db round-trips into annotation correctly": the three pinned
// columns (user_hidden, user_hidden_reason, wip) land in pg-desk's
// annotation table as a PR-level row keyed by (repo, "pr", "<repo>#<n>") —
// the same qualified entity id resolvePRRef reconstructs for every
// hide/unhide/wip/feedback write (pg2-tlwh2), not a bare number — attributed
// to this command, and no other pull_request column leaks through.
func TestImportPgPrAnnotationsRoundTrip(t *testing.T) {
	const fixedNow = "2026-09-16T00:00:00Z"
	origNow := importPgPrNow
	importPgPrNow = func() string { return fixedNow }
	t.Cleanup(func() { importPgPrNow = origNow })

	const repo = "acme/widgets"
	sourcePath := newFixturePgPrStore(t, []fixturePR{
		{repo: repo, number: 1, hidden: true, hiddenReason: "flaky", wip: false},
		{repo: repo, number: 2, hidden: false, hiddenReason: "", wip: true},
		{repo: repo, number: 3, hidden: false, hiddenReason: "", wip: false},
	})

	desk := store.OpenForTest(t)

	copied, err := importPgPrAnnotations(sourcePath, desk)
	if err != nil {
		t.Fatalf("importPgPrAnnotations: %v", err)
	}
	if copied != 3 {
		t.Fatalf("copied = %d, want 3", copied)
	}

	cases := []struct {
		number     int
		wantHidden bool
		wantReason string
		wantWIP    bool
	}{
		{1, true, "flaky", false},
		{2, false, "", true},
		{3, false, "", false},
	}
	for _, c := range cases {
		wantEntityID := repo + "#" + strconv.Itoa(c.number)
		a, found, err := desk.GetPRAnnotation(repo, importPgPrEntityType, wantEntityID)
		if err != nil {
			t.Fatalf("GetPRAnnotation(#%d): %v", c.number, err)
		}
		if !found {
			t.Fatalf("GetPRAnnotation(#%d): not found", c.number)
		}
		if a.EntityID != wantEntityID {
			t.Errorf("#%d: entity_id = %q, want qualified form %q", c.number, a.EntityID, wantEntityID)
		}

		// Negative check: the pre-fix bare-number key MUST NOT resolve —
		// proves the row is keyed by the qualified id, not both/either.
		if _, bareFound, err := desk.GetPRAnnotation(repo, importPgPrEntityType, strconv.Itoa(c.number)); err != nil {
			t.Fatalf("GetPRAnnotation(bare #%d): %v", c.number, err)
		} else if bareFound {
			t.Errorf("#%d: bare-number entity_id %q unexpectedly resolved; row must be keyed by the qualified form only", c.number, strconv.Itoa(c.number))
		}

		if a.Hidden == nil || *a.Hidden != c.wantHidden {
			t.Errorf("#%d: hidden = %v, want %v", c.number, a.Hidden, c.wantHidden)
		}
		if a.HiddenReason != c.wantReason {
			t.Errorf("#%d: hidden_reason = %q, want %q", c.number, a.HiddenReason, c.wantReason)
		}
		if a.WIP == nil || *a.WIP != c.wantWIP {
			t.Errorf("#%d: wip = %v, want %v", c.number, a.WIP, c.wantWIP)
		}
		if a.SetBy != importPgPrSetBy {
			t.Errorf("#%d: set_by = %q, want %q", c.number, a.SetBy, importPgPrSetBy)
		}
		if a.SetAt != fixedNow {
			t.Errorf("#%d: set_at = %q, want %q", c.number, a.SetAt, fixedNow)
		}
	}
}

// TestImportPgPrAnnotationsRoundTripsThroughHide is the acceptance
// criterion "a PR annotation written by the import tool round-trips
// through hide/wip/feedback after pg2-276sg's fix (same qualified-id
// convention on both sides)" (pg2-tlwh2): the row this import tool writes
// must be the SAME row the real `hide` CLI command (via resolvePRRef)
// reads and updates — not a second, orphaned row under a different key.
// Before this fix, importPgPrAnnotations wrote a bare-number entity id
// while resolvePRRef always queries the qualified "<repo>#<n>" id, so
// hide's read of the existing annotation would find nothing and silently
// clobber the wip flag this import tool had set; this test would have
// caught that by asserting wip survives the hide call.
func TestImportPgPrAnnotationsRoundTripsThroughHide(t *testing.T) {
	origNow := importPgPrNow
	importPgPrNow = func() string { return "2026-09-16T00:00:00Z" }
	t.Cleanup(func() { importPgPrNow = origNow })

	const repo = "o/r"
	sourcePath := newFixturePgPrStore(t, []fixturePR{
		{repo: repo, number: 42, hidden: false, hiddenReason: "", wip: true},
	})

	st, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: repo}}}
	withOpenSeams(t, cfg, openFresh)
	// hide's own "does the entity exist at all" resolution check
	// (setHiddenAnnotation) needs an entity row independent of the
	// annotation row this import tool writes.
	seedBareEntity(t, st, repo, repo+"#42")

	if _, err := importPgPrAnnotations(sourcePath, st); err != nil {
		t.Fatalf("importPgPrAnnotations: %v", err)
	}

	// hide via the real CLI path, which resolves "42" through
	// resolvePRRef to the qualified "o/r#42" id.
	if err := runCmdArgs(t, "hide", []string{"42", "reason"}); err != nil {
		t.Fatalf("hide: %v", err)
	}

	ann, found, err := st.GetPRAnnotation(repo, entityTypePR, repo+"#42")
	if err != nil || !found {
		t.Fatalf("GetPRAnnotation after hide: found=%v err=%v", found, err)
	}
	if ann.Hidden == nil || !*ann.Hidden {
		t.Fatalf("hidden after hide = %v, want true", ann.Hidden)
	}
	if ann.WIP == nil || !*ann.WIP {
		t.Fatalf("wip after hide = %v, want true (preserved from import, same row)", ann.WIP)
	}
}

// TestImportPgPrAnnotationsIdempotent is the acceptance criterion "a
// second run against the same source is a no-op (idempotent)": running
// importPgPrAnnotations twice against an unchanged source leaves the same
// rows with the same values — no duplicate rows, no double-toggling.
func TestImportPgPrAnnotationsIdempotent(t *testing.T) {
	origNow := importPgPrNow
	importPgPrNow = func() string { return "2026-09-16T00:00:00Z" }
	t.Cleanup(func() { importPgPrNow = origNow })

	const repo = "acme/widgets"
	numbers := []int{1, 2}
	sourcePath := newFixturePgPrStore(t, []fixturePR{
		{repo: repo, number: 1, hidden: true, hiddenReason: "flaky", wip: false},
		{repo: repo, number: 2, hidden: false, hiddenReason: "", wip: true},
	})

	desk := store.OpenForTest(t)

	firstCopied, err := importPgPrAnnotations(sourcePath, desk)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	first := map[int]store.Annotation{}
	for _, n := range numbers {
		a, found, err := desk.GetPRAnnotation(repo, importPgPrEntityType, repo+"#"+strconv.Itoa(n))
		if err != nil || !found {
			t.Fatalf("first run: GetPRAnnotation(#%d): found=%v err=%v", n, found, err)
		}
		first[n] = a
	}

	secondCopied, err := importPgPrAnnotations(sourcePath, desk)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if secondCopied != firstCopied {
		t.Fatalf("second run copied %d row(s), want %d (same as first run)", secondCopied, firstCopied)
	}
	for _, n := range numbers {
		a, found, err := desk.GetPRAnnotation(repo, importPgPrEntityType, repo+"#"+strconv.Itoa(n))
		if err != nil || !found {
			t.Fatalf("second run: GetPRAnnotation(#%d): found=%v err=%v", n, found, err)
		}
		if !annotationsEqual(a, first[n]) {
			t.Fatalf("second run changed the annotation for #%d: got %+v (hidden=%v wip=%v), want %+v (hidden=%v wip=%v) (same as first run)",
				n, a, derefBool(a.Hidden), derefBool(a.WIP), first[n], derefBool(first[n].Hidden), derefBool(first[n].WIP))
		}
	}
}

func derefBool(b *bool) bool {
	if b == nil {
		return false
	}
	return *b
}

// TestImportPgPrAnnotationsSourceUnreadable proves the "1 when the given
// --store path cannot be read as a pg-pr store" exit-code contract at the
// function level: a missing source file is an error, not a silent 0-row
// success.
func TestImportPgPrAnnotationsSourceUnreadable(t *testing.T) {
	desk := store.OpenForTest(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist.db")

	if _, err := importPgPrAnnotations(missing, desk); err == nil {
		t.Fatal("expected an error for a missing/unreadable pg-pr store, got nil")
	}
}

// TestImportPgPrAnnotationsCmdRequiresStore proves the CLI wiring: the
// subcommand is reachable on rootCmd and refuses to run without --store,
// per docs/behavior/pg-desk/import-pg-pr-annotations.md's documented
// signature (`--store <pg-pr store.db>`). It deliberately never calls
// importPgPrDeskStoreOpen (the guard returns first), so this test never
// touches the real $XDG_STATE_HOME/pg-desk/store.db.
func TestImportPgPrAnnotationsCmdRequiresStore(t *testing.T) {
	ipaF.sourceStore = ""
	t.Cleanup(func() { ipaF.sourceStore = "" })

	cmd, _, err := rootCmd.Find([]string{"import-pg-pr-annotations"})
	if err != nil {
		t.Fatalf("rootCmd has no import-pg-pr-annotations subcommand: %v", err)
	}
	runErr := cmd.RunE(cmd, nil)
	if runErr == nil {
		t.Fatal("expected an error when --store is not set, got nil")
	}
	if !strings.Contains(runErr.Error(), "--store") {
		t.Fatalf("expected an error mentioning --store, got %q", runErr.Error())
	}
}

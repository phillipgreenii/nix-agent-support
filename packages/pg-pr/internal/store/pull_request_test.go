package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestUpsertPRInsertsThenUpdates(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	pr := PullRequest{
		Repo: "owner/repo", Number: 42, Ownership: "mine",
		Author: "phillipg", State: "open", HeadSHA: "abc123",
	}
	id, err := db.UpsertPR(ctx, pr)
	if err != nil {
		t.Fatalf("UpsertPR insert: %v", err)
	}
	if id == 0 {
		t.Fatal("UpsertPR returned id 0")
	}

	pr.HeadSHA = "def456"
	id2, err := db.UpsertPR(ctx, pr)
	if err != nil {
		t.Fatalf("UpsertPR update: %v", err)
	}
	if id2 != id {
		t.Fatalf("upsert created a new row: id=%d id2=%d", id, id2)
	}

	got, err := db.GetPR(ctx, "owner/repo", 42)
	if err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if got == nil || got.HeadSHA != "def456" {
		t.Fatalf("GetPR = %+v, want head_sha def456", got)
	}
}

func TestListOpenPRs(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 1, Ownership: "mine", State: "open"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 2, Ownership: "team", State: "closed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertPR(ctx, PullRequest{Repo: "o/other", Number: 3, Ownership: "mine", State: "open"}); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListOpenPRs(ctx, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Number != 1 {
		t.Fatalf("want only o/r#1 open, got %+v", got)
	}
}

func TestGetPRByID(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	pr := PullRequest{
		Repo: "owner/repo", Number: 7, Ownership: "mine", State: "open",
	}
	id, err := db.UpsertPR(ctx, pr)
	if err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}

	got, err := db.GetPRByID(ctx, id)
	if err != nil {
		t.Fatalf("GetPRByID: %v", err)
	}
	if got == nil {
		t.Fatal("GetPRByID returned nil, want row")
	}
	if got.Repo != "owner/repo" || got.Number != 7 {
		t.Fatalf("GetPRByID = %+v, want repo=owner/repo number=7", got)
	}

	// Unknown id returns nil, no error.
	missing, err := db.GetPRByID(ctx, 99999)
	if err != nil {
		t.Fatalf("GetPRByID(unknown): %v", err)
	}
	if missing != nil {
		t.Fatalf("GetPRByID(unknown) = %+v, want nil", missing)
	}
}

func TestSetEnrichment_RoundTripAndNoClobber(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)

	base := PullRequest{Repo: "o/r", Number: 5, Ownership: "mine", Author: "me", State: "open", Branch: "b"}
	if _, err := db.UpsertPR(ctx, base); err != nil {
		t.Fatalf("seed UpsertPR: %v", err)
	}

	enr := Enrichment{
		Kind: "bugfix", Languages: []string{"Go", "Nix"}, Size: "M",
		Urgency: "high", UrgencyScore: 5, UrgencyReasons: []string{"label:p0", "ci-failing"},
	}
	if err := db.SetEnrichment(ctx, "o/r", 5, enr); err != nil {
		t.Fatalf("SetEnrichment: %v", err)
	}

	got, err := db.GetPR(ctx, "o/r", 5)
	if err != nil || got == nil {
		t.Fatalf("GetPR: %v %v", got, err)
	}
	if got.Kind != "bugfix" || got.Size != "M" || got.Urgency != "high" || got.UrgencyScore != 5 {
		t.Fatalf("enrichment not persisted: %+v", got)
	}
	if !reflect.DeepEqual(got.Languages, []string{"Go", "Nix"}) || !reflect.DeepEqual(got.UrgencyReasons, []string{"label:p0", "ci-failing"}) {
		t.Fatalf("json columns not persisted: langs=%v reasons=%v", got.Languages, got.UrgencyReasons)
	}

	// A subsequent plain UpsertPR (as the lifecycle emit / ingest does) MUST
	// NOT clobber the enrichment columns.
	if _, err := db.UpsertPR(ctx, base); err != nil {
		t.Fatalf("re-UpsertPR: %v", err)
	}
	got2, err := db.GetPR(ctx, "o/r", 5)
	if err != nil || got2 == nil {
		t.Fatalf("GetPR2: %v %v", got2, err)
	}
	if got2.Kind != "bugfix" || got2.Urgency != "high" || !reflect.DeepEqual(got2.Languages, []string{"Go", "Nix"}) {
		t.Fatalf("UpsertPR clobbered enrichment: %+v", got2)
	}
}

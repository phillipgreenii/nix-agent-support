package store

import (
	"context"
	"path/filepath"
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

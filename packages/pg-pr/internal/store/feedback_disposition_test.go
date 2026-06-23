package store

import (
	"context"
	"testing"
)

func TestSetDispositionValidatesAction(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	prID, _ := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 1, Ownership: "mine", State: "open"})
	id, _ := db.UpsertFeedback(ctx, Feedback{PRID: prID, Kind: "pr-comments", Fingerprint: "fp-v"})

	// Invalid action must be rejected before touching the DB.
	if err := db.SetDisposition(ctx, id, "frobnicate", "", ""); err == nil {
		t.Fatal("SetDisposition with invalid action: expected error, got nil")
	}

	// Valid action must succeed.
	if err := db.SetDisposition(ctx, id, "no-action", "noted", ""); err != nil {
		t.Fatalf("SetDisposition with valid action %q: %v", "no-action", err)
	}
	got, _ := db.GetFeedback(ctx, id)
	if got.DispositionAction != "no-action" {
		t.Fatalf("DispositionAction = %q, want no-action", got.DispositionAction)
	}
}

func TestSetDispositionAndListPendingReplies(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	prID, _ := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 1, Ownership: "mine", State: "open"})
	id, _ := db.UpsertFeedback(ctx, Feedback{PRID: prID, Kind: "pr-comments", Fingerprint: "fp"})

	if err := db.SetDisposition(ctx, id, "will-fix", "addressing", "queued reply"); err != nil {
		t.Fatalf("SetDisposition: %v", err)
	}
	got, _ := db.GetFeedback(ctx, id)
	if got.DispositionAction != "will-fix" || got.ReplyBody != "queued reply" || got.Status != "dispositioned" {
		t.Fatalf("disposition not applied: %+v", got)
	}

	pending, err := db.ListPendingReplies(ctx)
	if err != nil {
		t.Fatalf("ListPendingReplies: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != id {
		t.Fatalf("pending = %+v, want the one item", pending)
	}

	if err := db.MarkReplied(ctx, id, "resp-123"); err != nil {
		t.Fatalf("MarkReplied: %v", err)
	}
	pending, _ = db.ListPendingReplies(ctx)
	if len(pending) != 0 {
		t.Fatalf("after MarkReplied pending = %d, want 0", len(pending))
	}
}

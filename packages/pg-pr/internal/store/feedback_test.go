package store

import (
	"context"
	"testing"
)

func TestUpsertFeedbackDedupsByFingerprint(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	prID, _ := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 1, Ownership: "mine", State: "open"})

	fb := Feedback{
		PRID: prID, Kind: "pr-comments", Fingerprint: "fp-1",
		Body: "first", AuthorKind: "human", AuthorRole: "team_member",
	}
	id, err := db.UpsertFeedback(ctx, fb)
	if err != nil {
		t.Fatalf("UpsertFeedback: %v", err)
	}

	fb.Body = "second"
	id2, err := db.UpsertFeedback(ctx, fb)
	if err != nil {
		t.Fatalf("UpsertFeedback 2: %v", err)
	}
	if id2 != id {
		t.Fatalf("dedup failed: id=%d id2=%d", id, id2)
	}

	got, err := db.GetFeedback(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("GetFeedback: %v / %v", got, err)
	}
	if got.Body != "second" {
		t.Fatalf("Body = %q, want second", got.Body)
	}

	list, err := db.ListFeedback(ctx, prID, ListFilter{})
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListFeedback len = %d, want 1", len(list))
	}
}

func TestCodeCommentThreadRequiresFile(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	prID, _ := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 1, Ownership: "team", State: "open"})

	_, err := db.UpsertFeedback(ctx, Feedback{
		PRID: prID, Kind: "code-comment-thread", Fingerprint: "fp-x",
	})
	if err == nil {
		t.Fatal("expected CHECK violation for code-comment-thread without file")
	}
}

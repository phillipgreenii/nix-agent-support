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

// TestListMessagesEmpty verifies that ListMessages returns an empty (non-error)
// result for a feedback item that has no rows in code_comment_message.
func TestListMessagesEmpty(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	prID, _ := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 1, Ownership: "mine", State: "open"})
	fbID, err := db.UpsertFeedback(ctx, Feedback{
		PRID: prID, Kind: "pr-comments", Fingerprint: "fp-m",
	})
	if err != nil {
		t.Fatalf("UpsertFeedback: %v", err)
	}

	msgs, err := db.ListMessages(ctx, fbID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages (none ingested yet), got %d", len(msgs))
	}
}

// TestReplaceMessages verifies that ReplaceMessages writes and idempotently
// updates code_comment_message rows inside a transaction.
func TestReplaceMessages(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	prID, _ := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 2, Ownership: "team", State: "open"})

	var fbID int64
	err := db.InTx(ctx, func(tx *Tx) error {
		id, err := tx.UpsertFeedback(Feedback{
			PRID:        prID,
			Kind:        "code-comment-thread",
			Fingerprint: "fp-thread",
			File:        "main.go",
			AuthorKind:  "human",
		})
		if err != nil {
			return err
		}
		fbID = id
		return tx.ReplaceMessages(fbID, []Message{
			{ExternalID: "m1", AuthorLogin: "alice", AuthorKind: "human", Body: "first comment", IsOurs: false},
			{ExternalID: "m2", AuthorLogin: "bob", AuthorKind: "human", Body: "second comment", IsOurs: false},
		})
	})
	if err != nil {
		t.Fatalf("InTx (initial write): %v", err)
	}

	msgs, err := db.ListMessages(ctx, fbID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].ExternalID != "m1" || msgs[0].AuthorLogin != "alice" {
		t.Errorf("msgs[0]: got %+v", msgs[0])
	}
	if msgs[1].ExternalID != "m2" || msgs[1].Body != "second comment" {
		t.Errorf("msgs[1]: got %+v", msgs[1])
	}

	// Re-ingestion (same external_ids) must be idempotent — no duplicate rows,
	// updated body is reflected.
	err = db.InTx(ctx, func(tx *Tx) error {
		return tx.ReplaceMessages(fbID, []Message{
			{ExternalID: "m1", AuthorLogin: "alice", AuthorKind: "human", Body: "first comment (edited)", IsOurs: false},
			{ExternalID: "m2", AuthorLogin: "bob", AuthorKind: "human", Body: "second comment", IsOurs: false},
		})
	})
	if err != nil {
		t.Fatalf("InTx (re-ingest): %v", err)
	}

	msgs2, err := db.ListMessages(ctx, fbID)
	if err != nil {
		t.Fatalf("ListMessages after re-ingest: %v", err)
	}
	if len(msgs2) != 2 {
		t.Fatalf("re-ingest must not duplicate: expected 2 messages, got %d", len(msgs2))
	}
	if msgs2[0].Body != "first comment (edited)" {
		t.Errorf("re-ingest must update body: got %q", msgs2[0].Body)
	}
}

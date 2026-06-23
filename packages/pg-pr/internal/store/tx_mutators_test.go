package store

import (
	"context"
	"testing"
)

func TestTxUpsertAndEnqueueAreAtomic(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	prID, _ := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 1, Ownership: "mine", State: "open"})

	// Rollback: neither the feedback row nor the outbox row survives.
	_ = db.InTx(ctx, func(tx *Tx) error {
		if _, err := tx.UpsertFeedback(Feedback{PRID: prID, Kind: "pr-comments", Fingerprint: "f1"}); err != nil {
			return err
		}
		if err := tx.EnqueueEvent(EventFeedbackCreated, []byte(`{}`)); err != nil {
			return err
		}
		return errForceRollback // defined in outbox_test.go
	})
	var fn, on int
	_ = db.sql.QueryRow("SELECT COUNT(*) FROM feedback").Scan(&fn)
	_ = db.sql.QueryRow("SELECT COUNT(*) FROM outbox").Scan(&on)
	if fn != 0 || on != 0 {
		t.Fatalf("rollback left feedback=%d outbox=%d, want 0/0", fn, on)
	}

	// Commit: both land.
	_ = db.InTx(ctx, func(tx *Tx) error {
		if _, err := tx.UpsertFeedback(Feedback{PRID: prID, Kind: "pr-comments", Fingerprint: "f2"}); err != nil {
			return err
		}
		return tx.EnqueueEvent(EventFeedbackCreated, []byte(`{}`))
	})
	_ = db.sql.QueryRow("SELECT COUNT(*) FROM feedback").Scan(&fn)
	_ = db.sql.QueryRow("SELECT COUNT(*) FROM outbox WHERE status='pending'").Scan(&on)
	if fn != 1 || on != 1 {
		t.Fatalf("commit left feedback=%d pending-outbox=%d, want 1/1", fn, on)
	}
}

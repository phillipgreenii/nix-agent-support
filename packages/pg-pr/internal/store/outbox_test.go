package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestOutboxRollbackThenCommitDispatch(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	err := db.InTx(ctx, func(tx *Tx) error {
		if err := tx.EnqueueEvent(EventPROpened, json.RawMessage(`{"pr":1}`)); err != nil {
			return err
		}
		return errForceRollback
	})
	if err == nil {
		t.Fatal("expected rollback error")
	}
	var n int
	_ = db.sql.QueryRow("SELECT COUNT(*) FROM outbox").Scan(&n)
	if n != 0 {
		t.Fatalf("rolled-back txn left %d outbox rows, want 0", n)
	}

	_ = db.InTx(ctx, func(tx *Tx) error {
		return tx.EnqueueEvent(EventPROpened, json.RawMessage(`{"pr":2}`))
	})
	var dispatched []Event
	if err := db.RunOutbox(ctx, func(ctx context.Context, e Event) error {
		dispatched = append(dispatched, e)
		return nil
	}); err != nil {
		t.Fatalf("RunOutbox: %v", err)
	}
	if len(dispatched) != 1 || dispatched[0].Type != EventPROpened {
		t.Fatalf("dispatched = %+v", dispatched)
	}
	_ = db.sql.QueryRow("SELECT COUNT(*) FROM outbox WHERE status='pending'").Scan(&n)
	if n != 0 {
		t.Fatalf("pending rows after run = %d, want 0", n)
	}
}

func TestOutboxCompletesEvenWhenDispatchErrors(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	_ = db.InTx(ctx, func(tx *Tx) error {
		return tx.EnqueueEvent(EventFeedbackCreated, json.RawMessage(`{}`))
	})
	_ = db.RunOutbox(ctx, func(ctx context.Context, e Event) error { return errForceRollback })
	var n int
	_ = db.sql.QueryRow("SELECT COUNT(*) FROM outbox WHERE status='pending'").Scan(&n)
	if n != 0 {
		t.Fatalf("pending after erroring dispatch = %d, want 0 (fire-once)", n)
	}
}

var errForceRollback = errTest("forced")

type errTest string

func (e errTest) Error() string { return string(e) }

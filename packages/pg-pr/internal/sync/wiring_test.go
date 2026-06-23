package sync

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
)

func TestFlushOutboxDrainsThroughDispatch(t *testing.T) {
	db := store.OpenForTest(t)
	dispatched := 0
	disp := func(ctx context.Context, e store.Event) error { dispatched++; return nil }

	_ = db.InTx(context.Background(), func(tx *store.Tx) error {
		return tx.EnqueueEvent(store.EventPROpened, json.RawMessage(`{}`))
	})

	flushOutbox(context.Background(), db, disp)
	if dispatched != 1 {
		t.Fatalf("dispatched = %d, want 1", dispatched)
	}
}

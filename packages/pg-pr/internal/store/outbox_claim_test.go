package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"testing"
)

// numPayload is a tiny per-row-distinguishable payload so a test can tell
// which outbox row a given dispatch call is for.
type numPayload struct {
	N int `json:"n"`
}

// TestRunOutboxCrossProcessClaimPreventsDoubleDispatch is the regression test
// for pg2-g42k5: RunOutbox used to select pending rows and dispatch them with
// no claim of any kind, so two independent connections/processes flushing the
// SAME outbox concurrently — e.g. a daemon and a one-shot `pg-pr sync`, which
// share no lock — could both select and dispatch the same row.
//
// dbA and dbB are two SEPARATE *DB handles (separate *sql.DB, separate
// connections) opened against the SAME on-disk file, standing in for two
// separate OS processes; the store's own single-process serialization
// (SetMaxOpenConns(1), see store.go) does not apply ACROSS them. Both drain
// the shared outbox concurrently; every row must be dispatched EXACTLY once
// no matter how the two drainers interleave.
func TestRunOutboxCrossProcessClaimPreventsDoubleDispatch(t *testing.T) {
	SetSynchronousForTests("OFF")
	dbPath := t.TempDir() + "/shared.db"

	dbA, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open dbA: %v", err)
	}
	t.Cleanup(func() { _ = dbA.Close() })

	dbB, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open dbB: %v", err)
	}
	t.Cleanup(func() { _ = dbB.Close() })

	ctx := context.Background()
	const n = 25
	for i := 0; i < n; i++ {
		payload, err := json.Marshal(numPayload{N: i})
		if err != nil {
			t.Fatalf("marshal payload %d: %v", i, err)
		}
		if err := dbA.InTx(ctx, func(tx *Tx) error {
			return tx.EnqueueEvent(EventPROpened, payload)
		}); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	var mu sync.Mutex
	dispatchCount := map[int]int{}
	countingDispatch := func(_ context.Context, e Event) error {
		var p numPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Errorf("unmarshal dispatched payload: %v", err)
			return nil
		}
		mu.Lock()
		dispatchCount[p.N]++
		mu.Unlock()
		return nil
	}

	var wg sync.WaitGroup
	wg.Add(2)
	errs := make(chan error, 2)
	go func() {
		defer wg.Done()
		errs <- dbA.RunOutbox(ctx, countingDispatch)
	}()
	go func() {
		defer wg.Done()
		errs <- dbB.RunOutbox(ctx, countingDispatch)
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("RunOutbox: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	total := 0
	for i := 0; i < n; i++ {
		if got := dispatchCount[i]; got != 1 {
			t.Errorf("row n=%d dispatched %d times, want exactly 1", i, got)
		}
		total += dispatchCount[i]
	}
	if total != n {
		t.Fatalf("total dispatches = %d, want %d", total, n)
	}

	// Every row must have reached status='complete' with its claim released.
	var pendingCount, unreleasedCount int
	if err := dbA.sql.QueryRow("SELECT COUNT(*) FROM outbox WHERE status != 'complete'").Scan(&pendingCount); err != nil {
		t.Fatalf("count non-complete: %v", err)
	}
	if pendingCount != 0 {
		t.Fatalf("%d rows not complete after both drainers finished", pendingCount)
	}
	if err := dbA.sql.QueryRow("SELECT COUNT(*) FROM outbox WHERE claimed_by IS NOT NULL").Scan(&unreleasedCount); err != nil {
		t.Fatalf("count still-claimed: %v", err)
	}
	if unreleasedCount != 0 {
		t.Fatalf("%d rows still carry a claim after completion", unreleasedCount)
	}
}

// TestRunOutboxSkipsFreshlyClaimedRow proves the claim actually blocks a
// second caller: a row claimed "just now" by someone else must not be
// dispatched or touched by this call.
func TestRunOutboxSkipsFreshlyClaimedRow(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.InTx(ctx, func(tx *Tx) error {
		return tx.EnqueueEvent(EventPROpened, json.RawMessage(`{"n":1}`))
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if _, err := db.sql.Exec(
		"UPDATE outbox SET claimed_by='other-process', claimed_at=?", nowRFC3339(),
	); err != nil {
		t.Fatalf("simulate fresh claim: %v", err)
	}

	dispatched := false
	if err := db.RunOutbox(ctx, func(_ context.Context, _ Event) error {
		dispatched = true
		return nil
	}); err != nil {
		t.Fatalf("RunOutbox: %v", err)
	}
	if dispatched {
		t.Fatal("dispatched a row that another caller freshly claimed")
	}

	var status string
	var claimedBy sql.NullString
	if err := db.sql.QueryRow("SELECT status, claimed_by FROM outbox").Scan(&status, &claimedBy); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if status != "pending" {
		t.Fatalf("status = %q, want pending (untouched)", status)
	}
	if !claimedBy.Valid || claimedBy.String != "other-process" {
		t.Fatalf("claimed_by = %v, want unchanged 'other-process'", claimedBy)
	}
}

// TestRunOutboxReclaimsExpiredLease proves a lease from a caller that never
// completed (simulating a crash between the claiming and completing UPDATE)
// is eventually reclaimed and dispatched, rather than stranding the row
// forever.
func TestRunOutboxReclaimsExpiredLease(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.InTx(ctx, func(tx *Tx) error {
		return tx.EnqueueEvent(EventPROpened, json.RawMessage(`{"n":1}`))
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// A claim from long enough ago to be past outboxLeaseDuration, with no
	// completing UPDATE ever having run — the stranded-claim scenario.
	staleClaimedAt := leaseCutoff(outboxLeaseDuration * 2)
	if _, err := db.sql.Exec(
		"UPDATE outbox SET claimed_by='dead-process', claimed_at=?", staleClaimedAt,
	); err != nil {
		t.Fatalf("simulate stale claim: %v", err)
	}

	dispatched := 0
	if err := db.RunOutbox(ctx, func(_ context.Context, _ Event) error {
		dispatched++
		return nil
	}); err != nil {
		t.Fatalf("RunOutbox: %v", err)
	}
	if dispatched != 1 {
		t.Fatalf("dispatched = %d, want 1 (stale lease should be reclaimed)", dispatched)
	}

	var status string
	if err := db.sql.QueryRow("SELECT status FROM outbox").Scan(&status); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if status != "complete" {
		t.Fatalf("status = %q, want complete", status)
	}
}

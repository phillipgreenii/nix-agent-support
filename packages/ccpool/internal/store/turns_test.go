package store

import (
	"context"
	"testing"
)

func TestOpen_migratesTurnsTable(t *testing.T) {
	st := newTestStore(t)
	var n int
	err := st.db.QueryRowContext(context.Background(),
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name='turns'").Scan(&n)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Fatalf("turns table count = %d, want 1", n)
	}
}

func TestInsertTurnAndGet(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	in := Turn{TurnID: "t-1", Name: "alpha", Prompt: "do the thing"}
	if err := st.InsertTurn(ctx, in); err != nil {
		t.Fatalf("InsertTurn: %v", err)
	}

	got, ok, err := st.GetTurn(ctx, "t-1")
	if err != nil || !ok {
		t.Fatalf("GetTurn: ok=%v err=%v", ok, err)
	}
	if got.Name != "alpha" || got.Prompt != "do the thing" {
		t.Errorf("got %+v", got)
	}
	if got.Status != TurnPending {
		t.Errorf("Status = %q, want pending (default on insert)", got.Status)
	}
	if got.CreatedAt != 1000 {
		t.Errorf("CreatedAt = %d, want 1000 (fake clock)", got.CreatedAt)
	}
	if got.TranscriptPath != "" || got.ResolvedAt != 0 {
		t.Errorf("pending turn should have no transcript/resolved_at: %+v", got)
	}
}

func TestGetTurn_unknown(t *testing.T) {
	st := newTestStore(t)
	_, ok, err := st.GetTurn(context.Background(), "nope")
	if err != nil || ok {
		t.Fatalf("GetTurn(unknown): ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

func TestResolveOldestPendingTurn_FIFO(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	// Insert three pending turns for the same name at increasing times.
	if err := st.InsertTurn(ctx, Turn{TurnID: "t-1", Name: "alpha", Prompt: "first"}); err != nil {
		t.Fatal(err)
	}
	bumpClock(t, st, 10)
	if err := st.InsertTurn(ctx, Turn{TurnID: "t-2", Name: "alpha", Prompt: "second"}); err != nil {
		t.Fatal(err)
	}
	bumpClock(t, st, 10)
	if err := st.InsertTurn(ctx, Turn{TurnID: "t-3", Name: "alpha", Prompt: "third"}); err != nil {
		t.Fatal(err)
	}
	// A different name's pending turn must not be picked.
	if err := st.InsertTurn(ctx, Turn{TurnID: "t-other", Name: "beta", Prompt: "other"}); err != nil {
		t.Fatal(err)
	}

	bumpClock(t, st, 5) // resolve happens at 1025
	id, ok, err := st.ResolveOldestPendingTurn(ctx, "alpha", "/p/anchor.jsonl")
	if err != nil || !ok {
		t.Fatalf("ResolveOldestPendingTurn: id=%q ok=%v err=%v", id, ok, err)
	}
	if id != "t-1" {
		t.Fatalf("resolved %q, want oldest t-1", id)
	}

	got, _, _ := st.GetTurn(ctx, "t-1")
	if got.Status != TurnResolved {
		t.Errorf("Status = %q, want resolved", got.Status)
	}
	if got.TranscriptPath != "/p/anchor.jsonl" {
		t.Errorf("TranscriptPath = %q, want stamped anchor", got.TranscriptPath)
	}
	if got.ResolvedAt != 1025 {
		t.Errorf("ResolvedAt = %d, want 1025 (fake clock at resolve)", got.ResolvedAt)
	}

	// Next resolve picks the next-oldest still-pending turn (t-2).
	id, ok, err = st.ResolveOldestPendingTurn(ctx, "alpha", "/p/anchor2.jsonl")
	if err != nil || !ok || id != "t-2" {
		t.Fatalf("second resolve: id=%q ok=%v err=%v, want t-2", id, ok, err)
	}
}

func TestResolveOldestPendingTurn_noPending(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	// No pending turn for this name → ok=false, no error.
	id, ok, err := st.ResolveOldestPendingTurn(ctx, "alpha", "/p/x.jsonl")
	if err != nil {
		t.Fatalf("ResolveOldestPendingTurn: %v", err)
	}
	if ok || id != "" {
		t.Fatalf("got id=%q ok=%v, want \"\" false", id, ok)
	}

	// An already-resolved turn does not count as pending.
	if err := st.InsertTurn(ctx, Turn{TurnID: "t-1", Name: "alpha", Prompt: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.ResolveOldestPendingTurn(ctx, "alpha", "/p/x.jsonl"); !ok {
		t.Fatal("expected first resolve to succeed")
	}
	if _, ok, err := st.ResolveOldestPendingTurn(ctx, "alpha", "/p/y.jsonl"); ok || err != nil {
		t.Fatalf("resolve after all-resolved: ok=%v err=%v, want false nil", ok, err)
	}
}

package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

func TestSessionStore_UpsertThenGet(t *testing.T) {
	db := openTestDB(t)
	ss := NewSessionStore(db)
	ctx := context.Background()
	now := time.Now().UTC()
	pid := 12345

	s := store.Session{
		SessionID:       "sid-1",
		PID:             &pid,
		CommandHash:     "abc123",
		Cwd:             "/work",
		Name:            "feature-x",
		Model:           "claude-opus-4-7",
		Status:          "working",
		Labels:          map[string]string{"workspace.scope": "personal"},
		LastProcessedAt: now,
		UpdatedAt:       now,
		CreatedAt:       now,
	}
	if err := ss.Upsert(ctx, s); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := ss.GetByID(ctx, "sid-1", store.DefaultFreshness())
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("GetByID returned nil")
	}
	if got.SessionID != "sid-1" {
		t.Errorf("SessionID = %q", got.SessionID)
	}
	if got.PID == nil || *got.PID != pid {
		t.Errorf("PID = %v, want %d", got.PID, pid)
	}
	if got.Labels["workspace.scope"] != "personal" {
		t.Errorf("Labels[workspace.scope] = %q", got.Labels["workspace.scope"])
	}
}

func TestSessionStore_UpsertThenGet_PreservesLastErrorFromSubagent(t *testing.T) {
	db := openTestDB(t)
	ss := NewSessionStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	s := store.Session{
		SessionID:             "sid-subagent",
		LastErrorKind:         "server_error",
		LastErrorText:         "API Error: Stream idle timeout",
		LastErrorAt:           now,
		LastErrorTerminal:     true,
		LastErrorRetryable:    false,
		LastErrorFromSubagent: true,
		LastProcessedAt:       now,
		UpdatedAt:             now,
		CreatedAt:             now,
	}
	if err := ss.Upsert(ctx, s); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := ss.GetByID(ctx, "sid-subagent", store.DefaultFreshness())
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("GetByID returned nil")
	}
	if !got.LastErrorFromSubagent {
		t.Error("LastErrorFromSubagent = false after round-trip, want true (the '(in subagent)' provenance must survive persistence)")
	}
}

func TestSessionStore_GetByID_StaleReturnsNil(t *testing.T) {
	db := openTestDB(t)
	ss := NewSessionStore(db)
	ctx := context.Background()
	stale := time.Now().UTC().Add(-10 * time.Minute)

	if err := ss.Upsert(ctx, store.Session{
		SessionID:       "sid-stale",
		LastProcessedAt: stale,
		UpdatedAt:       stale,
		CreatedAt:       stale,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := ss.GetByID(ctx, "sid-stale", store.DefaultFreshness())
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got != nil {
		t.Errorf("GetByID for stale row returned %+v, want nil", got)
	}
}

func TestSessionStore_MarkDeletedThenRevived(t *testing.T) {
	db := openTestDB(t)
	ss := NewSessionStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, sid := range []string{"sid-a", "sid-b", "sid-c"} {
		if err := ss.Upsert(ctx, store.Session{
			SessionID:       sid,
			LastProcessedAt: now,
			UpdatedAt:       now,
			CreatedAt:       now,
		}); err != nil {
			t.Fatalf("Upsert %s: %v", sid, err)
		}
	}

	// keep only sid-a → sid-b and sid-c get soft-deleted
	if err := ss.MarkDeleted(ctx, []string{"sid-a"}, now); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}

	ids, err := ss.AllSessionIDs(ctx)
	if err != nil {
		t.Fatalf("AllSessionIDs: %v", err)
	}
	if len(ids) != 3 {
		t.Errorf("AllSessionIDs returned %d, want 3 (soft-delete should not remove rows)", len(ids))
	}

	// revive sid-b
	if err := ss.MarkRevived(ctx, []string{"sid-b"}); err != nil {
		t.Fatalf("MarkRevived: %v", err)
	}

	// sid-b should be retrievable again
	got, err := ss.GetByID(ctx, "sid-b", store.DefaultFreshness())
	if err != nil {
		t.Fatalf("GetByID sid-b: %v", err)
	}
	if got == nil {
		t.Error("sid-b should be retrievable after MarkRevived")
	}

	// sid-c should still be soft-deleted
	got, err = ss.GetByID(ctx, "sid-c", store.DefaultFreshness())
	if err != nil {
		t.Fatalf("GetByID sid-c: %v", err)
	}
	if got != nil {
		t.Error("sid-c should remain soft-deleted")
	}
}

func TestSessionStore_HardDelete(t *testing.T) {
	db := openTestDB(t)
	ss := NewSessionStore(db)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-48 * time.Hour)

	if err := ss.Upsert(ctx, store.Session{
		SessionID:       "sid-old",
		LastProcessedAt: old,
		UpdatedAt:       old,
		CreatedAt:       old,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := ss.MarkDeleted(ctx, nil, old); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}

	n, err := ss.HardDelete(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("HardDelete: %v", err)
	}
	if n != 1 {
		t.Errorf("HardDelete deleted %d, want 1", n)
	}

	ids, _ := ss.AllSessionIDs(ctx)
	if len(ids) != 0 {
		t.Errorf("AllSessionIDs after HardDelete = %v, want empty", ids)
	}
}

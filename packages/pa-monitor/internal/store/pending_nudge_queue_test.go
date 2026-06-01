package store

import (
	"context"
	"testing"
)

func TestInMemoryPendingNudgeQueue(t *testing.T) {
	q := NewInMemoryPendingNudgeQueue()
	ctx := context.Background()

	if err := q.Enqueue(ctx, PendingNudge{SessionID: "sid-1", Source: "manual", Text: "go"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Duplicate enqueue is a no-op (idempotent — same session+source)
	if err := q.Enqueue(ctx, PendingNudge{SessionID: "sid-1", Source: "manual", Text: "ignored"}); err != nil {
		t.Fatalf("Enqueue dup: %v", err)
	}

	got, err := q.ForSession(ctx, "sid-1")
	if err != nil {
		t.Fatalf("ForSession: %v", err)
	}
	if len(got) != 1 || got[0].Text != "go" {
		t.Errorf("ForSession = %v, want [{sid-1 manual go}]", got)
	}

	if err := q.Cancel(ctx, "sid-1", "manual"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	got, _ = q.ForSession(ctx, "sid-1")
	if len(got) != 0 {
		t.Errorf("after Cancel: ForSession = %v, want empty", got)
	}
}

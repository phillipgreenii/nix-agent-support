package sqlite

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

func TestNudgeStore_RecordAndLatest(t *testing.T) {
	db := openTestDB(t)
	ss := NewSessionStore(db)
	ns := NewNudgeStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := ss.Upsert(ctx, store.Session{SessionID: "sid-1", LastProcessedAt: now, UpdatedAt: now, CreatedAt: now}); err != nil {
		t.Fatalf("session: %v", err)
	}
	var sessionID int64
	_ = db.QueryRowContext(ctx, "SELECT id FROM sessions WHERE session_id = 'sid-1'").Scan(&sessionID)

	earlier := now.Add(-5 * time.Minute)
	if err := ns.Record(ctx, store.NudgeEvent{
		SessionID: sessionID,
		Text:      "first nudge",
		Result:    "sent",
		FiredAt:   earlier,
		Sources:   []string{"disrupted"},
	}); err != nil {
		t.Fatalf("Record earlier: %v", err)
	}
	if err := ns.Record(ctx, store.NudgeEvent{
		SessionID: sessionID,
		Text:      "second nudge",
		Result:    "sent",
		FiredAt:   now,
		Sources:   []string{"manual", "disrupted"},
	}); err != nil {
		t.Fatalf("Record now: %v", err)
	}

	latest, err := ns.LatestForSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("LatestForSession: %v", err)
	}
	if latest == nil {
		t.Fatal("LatestForSession returned nil")
	}
	if latest.Text != "second nudge" {
		t.Errorf("latest.Text = %q, want second nudge", latest.Text)
	}
	sort.Strings(latest.Sources)
	want := []string{"disrupted", "manual"}
	for i, s := range want {
		if i >= len(latest.Sources) || latest.Sources[i] != s {
			t.Errorf("Sources = %v, want %v", latest.Sources, want)
			break
		}
	}

	// LatestForSessionWithSource: "manual" → only the second event has it
	withManual, err := ns.LatestForSessionWithSource(ctx, sessionID, "manual")
	if err != nil {
		t.Fatalf("LatestForSessionWithSource manual: %v", err)
	}
	if withManual == nil || withManual.Text != "second nudge" {
		t.Errorf("LatestForSessionWithSource manual = %+v", withManual)
	}

	// LatestForSessionWithSource: "disrupted" → both have it, returns latest
	withDisrupt, err := ns.LatestForSessionWithSource(ctx, sessionID, "disrupted")
	if err != nil {
		t.Fatalf("LatestForSessionWithSource disrupted: %v", err)
	}
	if withDisrupt == nil || withDisrupt.Text != "second nudge" {
		t.Errorf("LatestForSessionWithSource disrupted = %+v", withDisrupt)
	}
}

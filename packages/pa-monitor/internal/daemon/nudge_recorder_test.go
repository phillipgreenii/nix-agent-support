package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/store"
	"github.com/phillipgreenii/pa-monitor/internal/store/sqlite"
)

// newNudgeRecorderTestDB builds an in-memory, migrated SQLite DB plus a started
// WriteService wired to it, returning the recorder under test. Cleanup is
// registered on t.
func newNudgeRecorderTestDB(t *testing.T) (*nudgeRecorder, *sqlite.NudgeStore) {
	t.Helper()
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ws := service.NewWriteService(service.WriteDeps{
		Sessions: sqlite.NewSessionStore(db),
		Nudges:   sqlite.NewNudgeStore(db),
	})
	ws.Start(context.Background())
	t.Cleanup(ws.Stop)
	return &nudgeRecorder{ws: ws, db: db}, sqlite.NewNudgeStore(db)
}

// countNudgeRows returns the number of nudge_history rows for the given string
// session id (joined through the sessions surrogate id).
func countNudgeRows(t *testing.T, r *nudgeRecorder, sessionID string) int {
	t.Helper()
	var n int
	if err := r.db.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM nudge_history h
		JOIN sessions s ON s.id = h.session_id
		WHERE s.session_id = ?`, sessionID).Scan(&n); err != nil {
		t.Fatalf("count nudge rows: %v", err)
	}
	return n
}

// TestNudgeRecorder_Record_CapturesFailureForUnregisteredSession is the
// regression test for pg2-evwy: a Signaler.Send failure whose target session is
// NOT yet in the sessions table must still be durably captured (persisted
// nudge_history row) carrying the reason label + full error_text. On the
// pre-fix code the recorder silently returns nil and drops the row.
func TestNudgeRecorder_Record_CapturesFailureForUnregisteredSession(t *testing.T) {
	rec, nudges := newNudgeRecorderTestDB(t)
	ctx := context.Background()

	const sid = "68c087dc-a47f-4136-8bcf-f761aa4719ce"
	const errText = "cmux send failed: dial unix /tmp/cmux.sock: connect: connection refused"
	firedAt := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	causeAt := firedAt.Add(-time.Minute)

	// Session deliberately NOT upserted first — this is the not-yet-registered case.
	if err := rec.Record(ctx, nudger.RecordEvent{
		SessionID:       sid,
		Text:            "continue",
		Result:          "failed",
		ErrorText:       errText,
		CausedByErrorAt: &causeAt,
		Escalated:       false,
		FiredAt:         firedAt,
		Sources:         []string{"disrupted"},
	}); err != nil {
		t.Fatalf("Record returned error: %v", err)
	}

	if got := countNudgeRows(t, rec, sid); got != 1 {
		t.Fatalf("nudge_history rows for unregistered session = %d, want 1 (failure was silently dropped)", got)
	}

	// Resolve the surrogate id and assert the captured row carries the reason
	// label + full error_text.
	var rowID int64
	if err := rec.db.QueryRowContext(ctx,
		"SELECT id FROM sessions WHERE session_id = ?", sid).Scan(&rowID); err != nil {
		t.Fatalf("resolve session surrogate id: %v", err)
	}
	ev, err := nudges.LatestForSession(ctx, rowID)
	if err != nil {
		t.Fatalf("LatestForSession: %v", err)
	}
	if ev == nil {
		t.Fatal("LatestForSession returned nil; captured row not found")
	}
	if ev.Result != "failed" {
		t.Errorf("Result = %q, want %q", ev.Result, "failed")
	}
	if ev.ErrorText != errText {
		t.Errorf("ErrorText = %q, want %q", ev.ErrorText, errText)
	}
	if len(ev.Sources) != 1 || ev.Sources[0] != "disrupted" {
		t.Errorf("Sources = %v, want [disrupted]", ev.Sources)
	}
}

// TestNudgeRecorder_Record_RegisteredSessionUnchanged proves the fix preserves
// existing behavior: when the session IS already registered, the row is
// recorded against the existing surrogate id (no duplicate/placeholder session
// created).
func TestNudgeRecorder_Record_RegisteredSessionUnchanged(t *testing.T) {
	rec, nudges := newNudgeRecorderTestDB(t)
	ctx := context.Background()

	const sid = "already-registered-session"
	if err := rec.ws.UpsertSession(ctx, store.Session{
		SessionID: sid,
		Status:    "idle",
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	var wantRowID int64
	if err := rec.db.QueryRowContext(ctx,
		"SELECT id FROM sessions WHERE session_id = ?", sid).Scan(&wantRowID); err != nil {
		t.Fatalf("resolve session surrogate id: %v", err)
	}

	if err := rec.Record(ctx, nudger.RecordEvent{
		SessionID: sid,
		Text:      "continue",
		Result:    "sent",
		FiredAt:   time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC),
		Sources:   []string{"disrupted"},
	}); err != nil {
		t.Fatalf("Record returned error: %v", err)
	}

	// Exactly one session row must exist for this id (no placeholder duplicate).
	var sessionRows int
	if err := rec.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sessions WHERE session_id = ?", sid).Scan(&sessionRows); err != nil {
		t.Fatalf("count session rows: %v", err)
	}
	if sessionRows != 1 {
		t.Errorf("session rows for %q = %d, want 1", sid, sessionRows)
	}

	ev, err := nudges.LatestForSession(ctx, wantRowID)
	if err != nil {
		t.Fatalf("LatestForSession: %v", err)
	}
	if ev == nil {
		t.Fatal("LatestForSession returned nil; row recorded against wrong surrogate id")
	}
	if ev.Result != "sent" {
		t.Errorf("Result = %q, want %q", ev.Result, "sent")
	}
}

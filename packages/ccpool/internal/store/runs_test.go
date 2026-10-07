package store

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/clock"
)

func insertSess(t *testing.T, st *Store, id string) {
	t.Helper()
	if err := st.Insert(context.Background(), Session{ExternalID: id, ClaudeSessionID: "csid-" + id, State: Ready}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRun_recordsStartAndIsOpen(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	insertSess(t, st, "a")
	if _, err := st.OpenRun(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	r, ok, err := st.OpenRunFor(ctx, "a")
	if err != nil || !ok {
		t.Fatalf("OpenRunFor ok=%v err=%v", ok, err)
	}
	if r.StartedAt != 1000 || r.EndedAt != 0 || !r.Open() {
		t.Errorf("run = %+v, want started_at=1000 open", r)
	}
}

func TestOpenRun_noRowIsError(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.OpenRun(context.Background(), "ghost"); err == nil {
		t.Fatal("OpenRun on a missing session must error")
	}
}

func TestFinalizeRun_idempotentAndPendingReasonWins(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	insertSess(t, st, "a")
	id, _ := st.OpenRun(ctx, "a")
	if err := st.SetRunPendingReason(ctx, "a", "operator"); err != nil {
		t.Fatal(err)
	}
	won, err := st.FinalizeRun(ctx, id, "exited", RunEndHook, 1500)
	if err != nil || !won {
		t.Fatalf("first finalize won=%v err=%v", won, err)
	}
	// A later reaper finalize must not overwrite the hook's end.
	won, err = st.FinalizeRun(ctx, id, "exited", RunEndReaper, 9999)
	if err != nil || won {
		t.Fatalf("second finalize won=%v err=%v, want lost without error", won, err)
	}
	runs, _ := st.RunsFor(ctx, "a")
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	if r := runs[0]; r.EndReason != "operator" || r.EndSource != RunEndHook || r.EndedAt != 1500 {
		t.Errorf("run = %+v, want pending reason operator kept, source hook, ended 1500", r)
	}
}

func TestFinalizeRun_concurrentDoubleFinalizeHasOneWinner(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.db"), &clock.Fake{T: time.Unix(1000, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	insertSess(t, st, "a")
	id, _ := st.OpenRun(ctx, "a")
	var wins int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if won, err := st.FinalizeRun(ctx, id, "exited", RunEndHook, 1200); err == nil && won {
				atomic.AddInt32(&wins, 1)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("winners = %d, want exactly 1", wins)
	}
}

func TestRunsFor_readsOpenRunWithPendingReason(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	insertSess(t, st, "a")
	_, _ = st.OpenRun(ctx, "a")
	_ = st.SetRunPendingReason(ctx, "a", "handler")
	runs, err := st.RunsFor(ctx, "a")
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%v err=%v", runs, err)
	}
	if !runs[0].Open() || runs[0].EndReason != "handler" {
		t.Errorf("run = %+v, want open with pending reason handler", runs[0])
	}
}

func TestDelete_removesRunsExplicitlyNoOrphans(t *testing.T) {
	st := newTestStore(t) // no foreign_keys pragma, as in the live store
	ctx := context.Background()
	insertSess(t, st, "a")
	insertSess(t, st, "b")
	_, _ = st.OpenRun(ctx, "a")
	_, _ = st.OpenRun(ctx, "b")
	if err := st.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM session_runs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("session_runs rows = %d, want 1 (only b's run remains)", n)
	}
	if runs, _ := st.RunsFor(ctx, "b"); len(runs) != 1 {
		t.Errorf("b's runs = %d, want 1", len(runs))
	}
}

func TestMigration009_appliesOnExistingDBLeavingRowsUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	clk := &clock.Fake{T: time.Unix(1000, 0).UTC()}
	st, err := Open(path, clk)
	if err != nil {
		t.Fatal(err)
	}
	insertSess(t, st, "a")
	// Roll the DB back to its pre-009 shape.
	if _, err := st.db.Exec(`DROP TABLE session_runs; DELETE FROM schema_migrations WHERE version = 9`); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	st, err = Open(path, clk)
	if err != nil {
		t.Fatalf("reopen (migrate 009): %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, ok, _ := st.GetByExternalID(context.Background(), "a"); !ok {
		t.Fatal("existing session row lost")
	}
	if _, err := st.OpenRun(context.Background(), "a"); err != nil {
		t.Fatalf("session_runs unusable after migration: %v", err)
	}
}

func TestCloseReasons_includesExited(t *testing.T) {
	if !CloseReasons["exited"] {
		t.Fatal(`CloseReasons must include "exited"`)
	}
}

func TestMigration010_endedRunsDefaultEmittedLaterRunsStartAtZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	clk := &clock.Fake{T: time.Unix(1000, 0).UTC()}
	st, err := Open(path, clk)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	insertSess(t, st, "a")
	endedID, _ := st.OpenRun(ctx, "a")
	if _, err := st.FinalizeRun(ctx, endedID, "exited", RunEndHook, 1100); err != nil {
		t.Fatal(err)
	}
	_, _ = st.OpenRun(ctx, "a") // still open when 010 applies
	// Roll the DB back to its pre-010 shape (drop the column, forget the version).
	if _, err := st.db.Exec(`ALTER TABLE session_runs DROP COLUMN metrics_emitted; DELETE FROM schema_migrations WHERE version = 10`); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	st, err = Open(path, clk)
	if err != nil {
		t.Fatalf("reopen (migrate 010): %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	runs, err := st.RunsFor(ctx, "a")
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %+v err=%v", runs, err)
	}
	if !runs[0].MetricsEmitted {
		t.Errorf("run already ended at migration time must default to emitted: %+v", runs[0])
	}
	if runs[1].MetricsEmitted {
		t.Errorf("open run must start unemitted: %+v", runs[1])
	}
	// A run ended after the migration is pending emission.
	if _, err := st.FinalizeRun(ctx, runs[1].ID, "exited", RunEndHook, 1200); err != nil {
		t.Fatal(err)
	}
	pend, err := st.RunsPendingEmission(ctx)
	if err != nil || len(pend) != 1 || pend[0].Run.ID != runs[1].ID || pend[0].ExternalID != "a" {
		t.Fatalf("pending = %+v err=%v, want only the post-migration run", pend, err)
	}
}

func TestClaimRunEmission_onlyEndedRunsAndExactlyOneWinner(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.db"), &clock.Fake{T: time.Unix(1000, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	insertSess(t, st, "a")
	id, _ := st.OpenRun(ctx, "a")
	if won, err := st.ClaimRunEmission(ctx, id); err != nil || won {
		t.Fatalf("open run claimed: won=%v err=%v", won, err)
	}
	_, _ = st.FinalizeRun(ctx, id, "exited", RunEndHook, 1100)
	var wins int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			won, err := st.ClaimRunEmission(ctx, id)
			if err != nil {
				t.Errorf("claim: %v", err)
			}
			if won {
				atomic.AddInt32(&wins, 1)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("overlapping emitters: %d winners, want exactly 1", wins)
	}
	if pend, _ := st.RunsPendingEmission(ctx); len(pend) != 0 {
		t.Fatalf("claimed run still pending: %+v", pend)
	}
}

// RecordRunOutputTokens attributes only the tokens produced since the session's
// previous run snapshot: a resume appends to the SAME transcript, so each run's
// delta is its transcript total minus what earlier runs already accounted for.
func TestRecordRunOutputTokens_deltaSincePreviousRunSnapshot(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	insertSess(t, st, "a")
	insertSess(t, st, "b")
	r1, _ := st.OpenRun(ctx, "a")
	d, err := st.RecordRunOutputTokens(ctx, r1, 100)
	if err != nil || d != 100 {
		t.Fatalf("run1 delta=%d err=%v, want 100", d, err)
	}
	r2, _ := st.OpenRun(ctx, "a")
	if d, err = st.RecordRunOutputTokens(ctx, r2, 250); err != nil || d != 150 {
		t.Fatalf("run2 delta=%d err=%v, want 150", d, err)
	}
	// A shrunken transcript total never yields a negative delta and never
	// lowers the high-water mark.
	r3, _ := st.OpenRun(ctx, "a")
	if d, err = st.RecordRunOutputTokens(ctx, r3, 10); err != nil || d != 0 {
		t.Fatalf("run3 delta=%d err=%v, want 0", d, err)
	}
	r4, _ := st.OpenRun(ctx, "a")
	if d, err = st.RecordRunOutputTokens(ctx, r4, 300); err != nil || d != 50 {
		t.Fatalf("run4 delta=%d err=%v, want 50 (300-250)", d, err)
	}
	// Another session is independent.
	rb, _ := st.OpenRun(ctx, "b")
	if d, err = st.RecordRunOutputTokens(ctx, rb, 7); err != nil || d != 7 {
		t.Fatalf("session b delta=%d err=%v, want 7", d, err)
	}
}

func TestRecordRunOutputTokens_unknownRunIsError(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.RecordRunOutputTokens(context.Background(), 9999, 5); err == nil {
		t.Fatal("want error for an unknown run id")
	}
}

func TestMigration011_addsOutputTokensColumnDefaultingToZero(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	insertSess(t, st, "a")
	id, _ := st.OpenRun(ctx, "a")
	var v int64
	if err := st.db.QueryRowContext(ctx, `SELECT output_tokens_total FROM session_runs WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatalf("column missing: %v", err)
	}
	if v != 0 {
		t.Errorf("default output_tokens_total = %d, want 0", v)
	}
}

// TestCloseReasons_includesDeadNeedsInputTTL: reap's dead-needs_input backstop
// stamps this reason (ADR 0086); SetCloseReason must accept it.
func TestCloseReasons_includesDeadNeedsInputTTL(t *testing.T) {
	if !CloseReasons["dead_needs_input_ttl"] {
		t.Fatal(`CloseReasons must include "dead_needs_input_ttl"`)
	}
}

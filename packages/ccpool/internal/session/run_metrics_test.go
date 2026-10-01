package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// closedCall is one recordSessionClosed call (one run's counter increment plus
// histogram observation).
type closedCall struct {
	dur    float64
	result string
	attrs  []attribute.KeyValue
}

func spyClosed(t *testing.T) *[]closedCall {
	t.Helper()
	orig := recordSessionClosed
	t.Cleanup(func() { recordSessionClosed = orig })
	var calls []closedCall
	recordSessionClosed = func(d float64, result string, attrs []attribute.KeyValue) {
		calls = append(calls, closedCall{d, result, attrs})
	}
	return &calls
}

func wantOneClosed(t *testing.T, calls *[]closedCall, dur float64, result string) {
	t.Helper()
	if len(*calls) != 1 || (*calls)[0].dur != dur || (*calls)[0].result != result {
		t.Fatalf("closed calls = %+v, want exactly one {dur=%v result=%s}", *calls, dur, result)
	}
}

func closeSvc(st Store, tm Tmux, now int64) *Service {
	return New(Deps{
		Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Exister: fakeExister{ok: true},
		Now: func() time.Time { return time.Unix(now, 0) },
	})
}

// Invariant 1 + purge versus hook: the hook ends the run (end_source=hook,
// metrics_emitted=0) while the purge is in flight; the delete helper emits it
// exactly once before the runs are removed.
func TestRunMetrics_purgeVersusHookEmitsOnceBeforeDelete(t *testing.T) {
	calls := spyClosed(t)
	ctx := context.Background()
	st := newMemStore(t)
	seedIdleRow(t, st, 50)
	_, _ = st.OpenRun(ctx, "a") // started_at = store clock 100
	tm := &closeTmux{live: true}
	tm.onKill = func(string) {
		run, _, _ := st.OpenRunFor(ctx, "a")
		_, _ = st.FinalizeRun(ctx, run.ID, "exited", store.RunEndHook, 200)
	}
	if err := closeSvc(st, tm, 300).CloseReason(ctx, "a", "operator", true); err != nil {
		t.Fatal(err)
	}
	wantOneClosed(t, calls, 100, "operator") // purge reason wins over the hook's guess
	if runs := mustRuns(t, st, "a"); len(runs) != 0 {
		t.Errorf("orphan runs after delete: %+v", runs)
	}
	// A following sweep emits nothing more.
	tmR := &reapTmux{live: map[string]bool{}, closed: map[string]bool{}}
	if err := reap0Service(st, tmR, time.Unix(1000, 0), true).Reap(ctx, 6, time.Hour); err != nil {
		t.Fatal(err)
	}
	wantOneClosed(t, calls, 100, "operator")
}

// A purge where the run is still open with a pending reason: finalized with it
// (no double end), emitted once; the later sweep emits nothing.
func TestRunMetrics_purgeOpenRunPendingReasonThenSweepEmitsNothing(t *testing.T) {
	calls := spyClosed(t)
	ctx := context.Background()
	st := newMemStore(t)
	seedIdleRow(t, st, 50)
	_, _ = st.OpenRun(ctx, "a")
	spy := &spyStore{Store: st}
	tm := &closeTmux{live: false}
	if err := closeSvc(spy, tm, 400).CloseReason(ctx, "a", "cap_eviction", true); err != nil {
		t.Fatal(err)
	}
	wantOneClosed(t, calls, 300, "cap_eviction")
	if len(spy.calls) != 1 {
		t.Errorf("finalize calls = %+v, want exactly one (no double end)", spy.calls)
	}
	tmR := &reapTmux{live: map[string]bool{}, closed: map[string]bool{}}
	_ = reap0Service(st, tmR, time.Unix(1000, 0), true).Reap(ctx, 6, time.Hour)
	wantOneClosed(t, calls, 300, "cap_eviction")
}

// A purge whose teardown fails before the delete leaves the pending reason on
// the open run, emits nothing and never reaches the delete helper.
type failPasteTmux struct{ closeTmux }

func (f *failPasteTmux) Paste(string, string) error { return errors.New("paste failed") }

func TestRunMetrics_purgeTeardownFailureKeepsPendingReasonAndEmitsNothing(t *testing.T) {
	calls := spyClosed(t)
	ctx := context.Background()
	st := newMemStore(t)
	seedIdleRow(t, st, 50)
	_, _ = st.OpenRun(ctx, "a")
	tm := &failPasteTmux{closeTmux{live: true}}
	if err := closeSvc(st, tm, 400).CloseReason(ctx, "a", "handler", true); err == nil {
		t.Fatal("want teardown error")
	}
	if _, ok, _ := st.GetByExternalID(ctx, "a"); !ok {
		t.Fatal("row deleted despite failed teardown")
	}
	run, ok, _ := st.OpenRunFor(ctx, "a")
	if !ok || run.EndReason != "handler" {
		t.Errorf("run = %+v ok=%v, want open with pending handler", run, ok)
	}
	if len(*calls) != 0 {
		t.Errorf("emitted %+v on a failed teardown", *calls)
	}
}

// Non-purge close: emitted once inside the close; a retry after a delivery
// error does not double count; closed-resumed-closed yields two observations.
func TestRunMetrics_nonPurgeCloseEmitsOncePerRun(t *testing.T) {
	calls := spyClosed(t)
	ctx := context.Background()
	st := newMemStore(t)
	seedIdleRow(t, st, 50)
	_, _ = st.OpenRun(ctx, "a")
	// First attempt fails delivering /exit: nothing emitted, pending reason kept.
	if err := closeSvc(st, &failPasteTmux{closeTmux{live: true}}, 300).CloseReason(ctx, "a", "idle_ttl", false); err == nil {
		t.Fatal("want delivery error")
	}
	if len(*calls) != 0 {
		t.Fatalf("emitted before teardown succeeded: %+v", *calls)
	}
	if err := closeSvc(st, &closeTmux{live: false}, 300).CloseReason(ctx, "a", "idle_ttl", false); err != nil {
		t.Fatal(err)
	}
	wantOneClosed(t, calls, 200, "idle_ttl")
	// Closing again must not re-emit the already-ended run.
	_ = closeSvc(st, &closeTmux{live: false}, 310).CloseReason(ctx, "a", "idle_ttl", false)
	wantOneClosed(t, calls, 200, "idle_ttl")
	// Resume then close again: a second run, a second observation with its own duration.
	if _, err := resumeService(st).Ensure(ctx, "a", "/tmp/proj", "", EnsureOpts{}); err != nil {
		t.Fatal(err)
	}
	if err := closeSvc(st, &closeTmux{live: false}, 800).CloseReason(ctx, "a", "operator", false); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 || (*calls)[1].result != "operator" || (*calls)[1].dur != 700 {
		t.Fatalf("calls = %+v, want a second {dur=700 result=operator} (store clock is fixed at 100)", *calls)
	}
}

// The launch-time phantom prune with an OPEN run: finalized exited/reaper at
// last_activity_at, emitted once before the row is deleted.
func TestRunMetrics_phantomPruneOpenRun(t *testing.T) {
	calls := spyClosed(t)
	ctx := context.Background()
	st := newMemStore(t)
	seedIdleRow(t, st, 160)
	_, _ = st.OpenRun(ctx, "a")
	if _, err := phantomPruneService(st).Ensure(ctx, "a", "/tmp/proj", "", EnsureOpts{}); err != nil {
		t.Fatal(err)
	}
	wantOneClosed(t, calls, 60, "exited")
}

// ... and with a run the hook already ended (unemitted).
func TestRunMetrics_phantomPruneHookEndedRun(t *testing.T) {
	calls := spyClosed(t)
	ctx := context.Background()
	st := newMemStore(t)
	seedIdleRow(t, st, 160)
	id, _ := st.OpenRun(ctx, "a")
	_, _ = st.FinalizeRun(ctx, id, "exited", store.RunEndHook, 190)
	if _, err := phantomPruneService(st).Ensure(ctx, "a", "/tmp/proj", "", EnsureOpts{}); err != nil {
		t.Fatal(err)
	}
	wantOneClosed(t, calls, 90, "exited")
}

func phantomPruneService(st *store.Store) *Service {
	s := resumeService(st)
	s.d.Exister = fakeExister{ok: false}
	return s
}

// Launch/resume over a stale open run: the run is ended on a KEPT row, so the
// next sweep (not the launch) emits it, exactly once.
func TestRunMetrics_resumeOverStaleOpenRunEmittedByNextSweep(t *testing.T) {
	calls := spyClosed(t)
	ctx := context.Background()
	st := newMemStore(t)
	seedIdleRow(t, st, 160)
	_, _ = st.OpenRun(ctx, "a")
	if _, err := resumeService(st).Ensure(ctx, "a", "/tmp/proj", "", EnsureOpts{}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("launch emitted %+v, want the next sweep to", *calls)
	}
	tm := &reapTmux{live: map[string]bool{"cc-a": true}, closed: map[string]bool{}}
	svc := reap0Service(st, tm, time.Unix(1000, 0), true)
	_ = svc.Reap(ctx, 6, 0)
	wantOneClosed(t, calls, 60, "exited")
	_ = svc.Reap(ctx, 6, 0)
	wantOneClosed(t, calls, 60, "exited") // a second sweep emits nothing
}

// The reaper sweep emits a hook-ended run at its START, exactly once, even on a
// kept row; Pass 0 over a hook-ended unemitted phantom emits before deleting.
func TestRunMetrics_sweepEmitsHookEndedRunOnceKeptRow(t *testing.T) {
	calls := spyClosed(t)
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{
		ExternalID: "k", ClaudeSessionID: "csid-k", TranscriptPath: "/p/k", State: store.Idle,
		TmuxSession: "cc-k", CreatedAt: 1, LastActivityAt: 555,
	})
	id, _ := st.OpenRun(ctx, "k")
	_, _ = st.FinalizeRun(ctx, id, "exited", store.RunEndHook, 600)
	tm := &reapTmux{live: map[string]bool{}, closed: map[string]bool{}}
	svc := reap0Service(st, tm, time.Unix(10_000, 0), true)
	_ = svc.Reap(ctx, 6, time.Hour)
	wantOneClosed(t, calls, 500, "exited")
	_ = svc.Reap(ctx, 6, time.Hour)
	wantOneClosed(t, calls, 500, "exited")
}

func TestRunMetrics_pass0HookEndedUnemittedPhantomEmittedOnce(t *testing.T) {
	calls := spyClosed(t)
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{
		ExternalID: "g", ClaudeSessionID: "csid-g", State: store.Idle, TmuxSession: "cc-g",
		CreatedAt: 1, LastActivityAt: 4242,
	})
	id, _ := st.OpenRun(ctx, "g")
	_, _ = st.FinalizeRun(ctx, id, "exited", store.RunEndHook, 700)
	tm := &reapTmux{live: map[string]bool{}, closed: map[string]bool{}}
	if err := reap0Service(st, tm, time.Unix(10_000, 0), false).Reap(ctx, 6, time.Hour); err != nil {
		t.Fatal(err)
	}
	wantOneClosed(t, calls, 600, "exited")
	if _, ok, _ := st.GetByExternalID(ctx, "g"); ok {
		t.Error("phantom must be deleted")
	}
	if runs := mustRuns(t, st, "g"); len(runs) != 0 {
		t.Errorf("orphan runs: %+v", runs)
	}
}

// Pass 0 racing a resume: the row re-read under the lock is no longer a
// phantom, so no delete, no finalize, no emission.
func TestRunMetrics_pass0RacingResumeEmitsNothing(t *testing.T) {
	calls := spyClosed(t)
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{
		ExternalID: "g", ClaudeSessionID: "csid-g", State: store.Idle, TmuxSession: "cc-g",
		CreatedAt: 1, LastActivityAt: 4242,
	})
	_, _ = st.OpenRun(ctx, "g")
	tm := &reapTmux{live: map[string]bool{}, closed: map[string]bool{}}
	s := New(Deps{
		Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Exister: fakeExister{ok: false},
		Now:  func() time.Time { return time.Unix(10_000, 0) },
		Lock: lockHook{before: func() { tm.live["cc-g"] = true }},
	})
	if err := s.Reap(ctx, 6, 0); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Errorf("emitted %+v for a resumed row", *calls)
	}
	if _, ok, _ := st.GetByExternalID(ctx, "g"); !ok {
		t.Error("resumed row deleted")
	}
}

// Two overlapping emitters (a sweep and a close) count the run exactly once.
func TestRunMetrics_overlappingEmittersCountOnce(t *testing.T) {
	calls := spyClosed(t)
	ctx := context.Background()
	st := newMemStore(t)
	seedIdleRow(t, st, 50)
	id, _ := st.OpenRun(ctx, "a")
	_, _ = st.FinalizeRun(ctx, id, "exited", store.RunEndHook, 200)
	svc := closeSvc(st, &closeTmux{}, 300)
	run := mustRuns(t, st, "a")[0]
	// Both emitters read the run as unemitted, then both try to claim.
	_ = svc.emitRun(ctx, run, nil)
	_ = svc.emitRun(ctx, run, nil)
	wantOneClosed(t, calls, 100, "exited")
}

// No Store.Delete call exists outside the shared helper.
func TestNoStoreDeleteOutsideHelper(t *testing.T) {
	re := regexp.MustCompile(`\bStore\.Delete\(`)
	root := filepath.Join("..", "..")
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if re.Match(b) && filepath.Base(path) != "delete.go" {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("Store.Delete called outside delete.go's helper: %v", offenders)
	}
}

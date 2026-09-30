package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/wait"
)

// finalizeCall records one FinalizeRun invocation seen by spyStore.
type finalizeCall struct {
	reason, source string
	endedAt        int64
}

// spyStore wraps a real store and records FinalizeRun calls (so a phantom-prune
// finalize stays observable after Store.Delete removes the runs).
type spyStore struct {
	*store.Store
	mu    sync.Mutex
	calls []finalizeCall
	ext   string      // when set, the runs of ext are snapshotted right after each finalize
	after []store.Run // snapshot of ext's runs after the last finalize
}

func (p *spyStore) FinalizeRun(ctx context.Context, id int64, reason, source string, endedAt int64) (bool, error) {
	p.mu.Lock()
	p.calls = append(p.calls, finalizeCall{reason, source, endedAt})
	p.mu.Unlock()
	won, err := p.Store.FinalizeRun(ctx, id, reason, source, endedAt)
	if p.ext != "" {
		p.after, _ = p.RunsFor(ctx, p.ext)
	}
	return won, err
}

func mustRuns(t *testing.T, st *store.Store, id string) []store.Run {
	t.Helper()
	runs, err := st.RunsFor(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return runs
}

func resumeService(st *store.Store) *Service {
	waiter := waitFunc(func(_ context.Context, externalID string, _ int64) (wait.Outcome, error) {
		_, _ = st.Transition(context.Background(), externalID, store.Ready, "", "/p/a.jsonl")
		return wait.Outcome{State: store.Ready}, nil
	})
	return New(Deps{
		Tmux: &fakeTmux{live: map[string]bool{}}, Trust: &fakeTrust{}, Store: st, Wait: waiter,
		Exister: fakeExister{ok: true}, Socket: "ccpool", Prefix: "cc-", PluginDir: "/p", ClaudeBin: "claude",
		NewUUID: func() string { return "uuid" }, Now: func() time.Time { return time.Unix(500, 0) },
	})
}

func seedIdleRow(t *testing.T, st *store.Store, lastActivity int64) {
	t.Helper()
	if err := st.Insert(context.Background(), store.Session{
		ExternalID: "a", ClaudeSessionID: "csid-a", TranscriptPath: "/p/a.jsonl", State: store.Idle,
		TmuxSession: "cc-a", LastActivityAt: lastActivity,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEnsure_brandNewOpensOneRun(t *testing.T) {
	st := newMemStore(t)
	if _, err := resumeService(st).Ensure(context.Background(), "a", "/tmp/proj", "", EnsureOpts{}); err != nil {
		t.Fatal(err)
	}
	runs := mustRuns(t, st, "a")
	if len(runs) != 1 || !runs[0].Open() || runs[0].StartedAt != 100 {
		t.Fatalf("runs = %+v, want one open run started at the store clock (100)", runs)
	}
}

func TestEnsure_resumeOverStaleOpenRunEndsItFirst(t *testing.T) {
	for _, tc := range []struct{ pending, want string }{{"", "exited"}, {"cap_eviction", "cap_eviction"}} {
		st := newMemStore(t)
		ctx := context.Background()
		seedIdleRow(t, st, 77)
		_, _ = st.OpenRun(ctx, "a")
		if tc.pending != "" {
			_ = st.SetRunPendingReason(ctx, "a", tc.pending)
		}
		if _, err := resumeService(st).Ensure(ctx, "a", "/tmp/proj", "", EnsureOpts{}); err != nil {
			t.Fatal(err)
		}
		runs := mustRuns(t, st, "a")
		if len(runs) != 2 {
			t.Fatalf("runs = %+v, want 2", runs)
		}
		old := runs[0]
		if old.Open() || old.EndReason != tc.want || old.EndSource != store.RunEndReaper || old.EndedAt != 77 {
			t.Errorf("stale run = %+v, want ended %q/reaper at last_activity_at 77", old, tc.want)
		}
		open := 0
		for _, r := range runs {
			if r.Open() {
				open++
			}
		}
		if open != 1 {
			t.Errorf("open runs = %d, want exactly 1", open)
		}
	}
}

func TestClose_closedResumedClosedYieldsTwoEndedRuns(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	seedIdleRow(t, st, 50)
	_, _ = st.OpenRun(ctx, "a")
	closer := func() {
		s := New(Deps{Tmux: &closeTmux{live: false}, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Now: func() time.Time { return time.Unix(300, 0) }})
		if err := s.CloseReason(ctx, "a", "idle_ttl", false); err != nil {
			t.Fatal(err)
		}
	}
	closer()
	if _, err := resumeService(st).Ensure(ctx, "a", "/tmp/proj", "", EnsureOpts{}); err != nil {
		t.Fatal(err)
	}
	closer()
	runs := mustRuns(t, st, "a")
	if len(runs) != 2 {
		t.Fatalf("runs = %+v, want 2", runs)
	}
	for _, r := range runs {
		if r.Open() || r.EndReason != "idle_ttl" || r.EndSource != store.RunEndClose || r.EndedAt != 300 {
			t.Errorf("run = %+v, want ended idle_ttl/close at 300", r)
		}
	}
}

// SessionEnd firing DURING a ccpool-initiated close keeps the close reason; the
// later closeWithReason end is a no-op.
func TestClose_hookDuringClose_keepsCloseReason(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	seedIdleRow(t, st, 50)
	_, _ = st.OpenRun(ctx, "a")
	tm := &closeTmux{live: true}
	tm.onKill = func(string) { // the hook fires while /exit is being torn down
		run, ok, _ := st.OpenRunFor(ctx, "a")
		if !ok || run.EndReason != "cap_eviction" {
			t.Errorf("pending reason at hook time = %+v ok=%v, want cap_eviction", run, ok)
			return
		}
		_, _ = st.FinalizeRun(ctx, run.ID, "exited", store.RunEndHook, 200)
	}
	s := New(Deps{Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Now: func() time.Time { return time.Unix(300, 0) }})
	if err := s.CloseReason(ctx, "a", "cap_eviction", false); err != nil {
		t.Fatal(err)
	}
	runs := mustRuns(t, st, "a")
	if len(runs) != 1 || runs[0].EndReason != "cap_eviction" || runs[0].EndSource != store.RunEndHook || runs[0].EndedAt != 200 {
		t.Fatalf("runs = %+v, want hook end (200) keeping cap_eviction, untouched by the later close", runs)
	}
}

// --purge: the pending reason is written BEFORE /exit even though the
// close_reason stamp is skipped, so a racing hook records the purge reason
// ("purge wins"), and the run stays readable up to Store.Delete.
func TestClose_purgeWins_pendingReasonBeforeExit(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	seedIdleRow(t, st, 50)
	_, _ = st.OpenRun(ctx, "a")
	tm := &closeTmux{live: true}
	var seen store.Run
	tm.onKill = func(string) {
		run, _, _ := st.OpenRunFor(ctx, "a")
		_, _ = st.FinalizeRun(ctx, run.ID, "exited", store.RunEndHook, 200)
		runs := mustRuns(t, st, "a")
		seen = runs[0]
	}
	s := New(Deps{Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Now: func() time.Time { return time.Unix(300, 0) }})
	if err := s.CloseReason(ctx, "a", "operator", true); err != nil {
		t.Fatal(err)
	}
	if seen.EndReason != "operator" || seen.EndSource != store.RunEndHook || seen.Open() {
		t.Errorf("run at delete time = %+v, want hook-ended with the purge reason operator", seen)
	}
	if runs := mustRuns(t, st, "a"); len(runs) != 0 {
		t.Errorf("orphan runs after purge: %+v", runs)
	}
}

func reap0Service(st Store, tm *reapTmux, now time.Time, ok bool) *Service {
	return New(Deps{
		Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Exister: fakeExister{ok: ok},
		Now: func() time.Time { return now },
	})
}

func TestReap_pass0FinalizesPhantomRunBeforeDelete(t *testing.T) {
	for _, tc := range []struct{ pending, want string }{{"", "exited"}, {"handler", "handler"}} {
		ctx := context.Background()
		now := time.Unix(10_000, 0)
		st := newMemStore(t)
		_ = st.Insert(ctx, store.Session{
			ExternalID: "g", ClaudeSessionID: "csid-g", State: store.Idle, TmuxSession: "cc-g",
			CreatedAt: 1, LastActivityAt: 4242,
		})
		_, _ = st.OpenRun(ctx, "g")
		if tc.pending != "" {
			_ = st.SetRunPendingReason(ctx, "g", tc.pending)
		}
		spy := &spyStore{Store: st, ext: "g"}
		tm := &reapTmux{live: map[string]bool{}, closed: map[string]bool{}}
		if err := reap0Service(spy, tm, now, false).Reap(ctx, 6, time.Hour); err != nil {
			t.Fatal(err)
		}
		if len(spy.calls) != 1 || spy.calls[0] != (finalizeCall{"exited", store.RunEndReaper, 4242}) {
			t.Errorf("finalize calls = %+v, want one {exited reaper 4242}", spy.calls)
		}
		if len(spy.after) != 1 || spy.after[0].Open() || spy.after[0].EndReason != tc.want || spy.after[0].EndedAt != 4242 {
			t.Errorf("run at delete time = %+v, want ended %q at 4242 (pending reason wins)", spy.after, tc.want)
		}
		if _, ok, _ := st.GetByExternalID(ctx, "g"); ok {
			t.Error("phantom must be deleted")
		}
	}
}

func TestReap_pass0FinalizesDeadKeptRowButNotFreshStarting(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(10_000, 0)
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{
		ExternalID: "kept", ClaudeSessionID: "csid-k", TranscriptPath: "/p/k", State: store.Idle,
		TmuxSession: "cc-kept", CreatedAt: 1, LastActivityAt: 555,
	})
	_ = st.Insert(ctx, store.Session{
		ExternalID: "fresh", ClaudeSessionID: "csid-f", State: store.Starting,
		TmuxSession: "cc-fresh", CreatedAt: now.Unix(), LastActivityAt: now.Unix(),
	})
	_, _ = st.OpenRun(ctx, "kept")
	_, _ = st.OpenRun(ctx, "fresh")
	tm := &reapTmux{live: map[string]bool{}, closed: map[string]bool{}}
	// Exister ok=true only for the kept row's transcript (fresh has none anyway).
	if err := reap0Service(st, tm, now, true).Reap(ctx, 6, time.Hour); err != nil {
		t.Fatal(err)
	}
	if r := mustRuns(t, st, "kept")[0]; r.Open() || r.EndReason != "exited" || r.EndSource != store.RunEndReaper || r.EndedAt != 555 {
		t.Errorf("kept run = %+v, want exited/reaper/555", r)
	}
	if r := mustRuns(t, st, "fresh")[0]; !r.Open() {
		t.Errorf("fresh starting row's run was finalized: %+v", r)
	}
}

func TestReap_pass0DoesNotOverwriteHookEnd(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(10_000, 0)
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{
		ExternalID: "kept", ClaudeSessionID: "csid-k", TranscriptPath: "/p/k", State: store.Idle,
		TmuxSession: "cc-kept", CreatedAt: 1, LastActivityAt: 555,
	})
	id, _ := st.OpenRun(ctx, "kept")
	_, _ = st.FinalizeRun(ctx, id, "exited", store.RunEndHook, 600)
	tm := &reapTmux{live: map[string]bool{}, closed: map[string]bool{}}
	if err := reap0Service(st, tm, now, true).Reap(ctx, 6, time.Hour); err != nil {
		t.Fatal(err)
	}
	if r := mustRuns(t, st, "kept")[0]; r.EndSource != store.RunEndHook || r.EndedAt != 600 {
		t.Errorf("run = %+v, want the hook's end untouched", r)
	}
}

// lockHook is a Locker whose Lock runs a callback first, modelling a resume that
// completes between Reap's unlocked liveness check and its locked re-read.
type lockHook struct{ before func() }

func (l lockHook) Lock(string) (func(), error) {
	if l.before != nil {
		l.before()
	}
	return func() {}, nil
}

func TestReap_pass0RacingResumeFinalizesNothing(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(10_000, 0)
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{
		ExternalID: "g", ClaudeSessionID: "csid-g", State: store.Idle, TmuxSession: "cc-g",
		CreatedAt: 1, LastActivityAt: 4242,
	})
	_, _ = st.OpenRun(ctx, "g")
	spy := &spyStore{Store: st}
	tm := &reapTmux{live: map[string]bool{}, closed: map[string]bool{}}
	s := New(Deps{
		Tmux: tm, Trust: &fakeTrust{}, Store: spy, Prefix: "cc-", Exister: fakeExister{ok: false},
		Now:  func() time.Time { return now },
		Lock: lockHook{before: func() { tm.live["cc-g"] = true }}, // the resume lands first
	})
	if err := s.Reap(ctx, 6, 0); err != nil {
		t.Fatal(err)
	}
	if len(spy.calls) != 0 {
		t.Errorf("finalize calls = %+v, want none (row no longer a phantom)", spy.calls)
	}
	if _, ok, _ := st.GetByExternalID(ctx, "g"); !ok {
		t.Error("resumed row must not be deleted")
	}
	if r := mustRuns(t, st, "g")[0]; !r.Open() {
		t.Errorf("run = %+v, want still open", r)
	}
}

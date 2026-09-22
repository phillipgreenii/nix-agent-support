package session

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

type reapTmux struct {
	live   map[string]bool
	closed map[string]bool
	pane   string
}

func (r *reapTmux) HasSession(name string) bool                                  { return r.live[name] }
func (r *reapTmux) NewSession(string, string, map[string]string, []string) error { return nil }
func (r *reapTmux) SendKeys(string, ...string) error                             { return nil }
func (r *reapTmux) Paste(name, body string) error {
	if body == "/exit" { // simulate graceful exit so waitGone returns fast
		r.live[name] = false
		r.closed[name] = true
	}
	return nil
}

func (r *reapTmux) KillSession(name string) error {
	r.closed[name] = true
	r.live[name] = false
	return nil
}
func (r *reapTmux) CapturePane(string) (string, error) { return r.pane, nil }

// reapFixture builds N live sessions with the given ages (seconds idle) and a
// service whose tmux reports them all live and records closures. All sessions
// are resumable (Exister ok=true) so the prune pass leaves the live rows alone.
// Every row is `ready`; use reapFixtureStates to vary the state.
func reapFixture(t *testing.T, now time.Time, ages map[string]int64) (*Service, map[string]bool) {
	t.Helper()
	return reapFixtureStates(t, now, ages, nil)
}

// reapFixtureStates is reapFixture with a per-session state override; any
// external_id absent from states gets `ready`. Used by the human-paused
// preservation tests (ADR 0037), which need a `needs_input` row.
func reapFixtureStates(t *testing.T, now time.Time, ages map[string]int64, states map[string]store.State) (*Service, map[string]bool) {
	t.Helper()
	ctx := context.Background()
	st := newMemStore(t)
	liveMap := map[string]bool{}
	for externalID, ageSec := range ages {
		state := store.Ready
		if s, ok := states[externalID]; ok {
			state = s
		}
		_ = st.Insert(ctx, store.Session{
			ExternalID: externalID, ClaudeSessionID: "csid-" + externalID, State: state,
			TmuxSession: "cc-" + externalID, LastActivityAt: now.Unix() - ageSec,
		})
		liveMap["cc-"+externalID] = true
	}
	closed := map[string]bool{}
	tm := &reapTmux{live: liveMap, closed: closed}
	s := New(Deps{
		Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Exister: fakeExister{ok: true},
		Now: func() time.Time { return now },
	})
	return s, closed
}

// TTL closures count toward the cap: after the stale one is reaped by idle_ttl,
// the pool is at the cap, so nothing more is closed (no over-reaping).
func TestReap_ttlClosuresCountTowardCap(t *testing.T) {
	now := time.Unix(10_000, 0)
	s, closed := reapFixture(t, now, map[string]int64{"fresh": 10, "mid": 100, "stale": 7200})
	if err := s.Reap(context.Background(), 2, time.Hour); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if !closed["cc-stale"] {
		t.Error("stale (idle past ttl) must be closed")
	}
	if closed["cc-mid"] {
		t.Error("mid must NOT be closed — after the TTL closure the pool is already at cap=2 (no over-reap)")
	}
	if closed["cc-fresh"] {
		t.Error("freshest session must survive")
	}
}

// Over cap with no TTL pressure: close the oldest-activity sessions until at cap.
// old/mid are modeled as Idle (turn ended) since cap eviction is no longer
// permitted to target a Ready (in-progress) row (ADR 0072).
func TestReap_overCapClosesOldestFirst(t *testing.T) {
	now := time.Unix(10_000, 0)
	s, closed := reapFixtureStates(t, now, map[string]int64{"fresh": 10, "mid": 100, "old": 1000},
		map[string]store.State{"mid": store.Idle, "old": store.Idle})
	// idleTTL=0 disables TTL; cap=1 → close the 2 oldest (old, mid); fresh survives.
	if err := s.Reap(context.Background(), 1, 0); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if !closed["cc-old"] || !closed["cc-mid"] {
		t.Errorf("cap=1 must close the 2 oldest (old, mid); closed=%v", closed)
	}
	if closed["cc-fresh"] {
		t.Error("most-recently-active session must survive")
	}
}

// TestReap_ttlSparesHumanPausedSession: a `needs_input` session idle FAR past
// idle_ttl is PRESERVED, not closed — it is parked awaiting a human decision and
// must survive so they can still `ccpool attach` (ADR 0037, ZR INV-CCPOOL-6).
// A non-paused session of the same age is still closed, so the carve-out is
// state-scoped and does not disable the TTL pass.
func TestReap_ttlSparesHumanPausedSession(t *testing.T) {
	now := time.Unix(10_000, 0)
	s, closed := reapFixtureStates(t, now,
		map[string]int64{"paused": 7200, "stale": 7200, "fresh": 10},
		map[string]store.State{"paused": store.NeedsInput})
	// cap=6 leaves the pool under cap, so ONLY the TTL pass is exercised here.
	if err := s.Reap(context.Background(), 6, time.Hour); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if closed["cc-paused"] {
		t.Error("a needs_input session idle past idle_ttl MUST be preserved, not reaped (ADR 0037)")
	}
	if !closed["cc-stale"] {
		t.Error("a NON-paused session idle past idle_ttl must still be closed")
	}
	if closed["cc-fresh"] {
		t.Error("freshest session must survive")
	}
}

// TestReap_capEvictionSparesHumanPausedSession: the cap-eviction pass honours the
// SAME carve-out as the TTL pass (ADR 0037) — the oldest-activity session is
// `needs_input`, so eviction skips it and falls through to the oldest NON-paused
// session instead. Preserved sessions no longer count toward the cap at all
// (ADR 0072 Decision 1), so pressure here comes purely from the two non-preserved
// rows against cap=1.
func TestReap_capEvictionSparesHumanPausedSession(t *testing.T) {
	now := time.Unix(10_000, 0)
	// oldest-activity first: paused(3000s) < mid(1000s) < fresh(10s). idleTTL=0
	// disables the TTL pass, so this is purely cap eviction. mid is modeled as
	// Idle (turn ended) since cap eviction is no longer permitted to target a
	// Ready (in-progress) row (ADR 0072).
	s, closed := reapFixtureStates(t, now,
		map[string]int64{"paused": 3000, "mid": 1000, "fresh": 10},
		map[string]store.State{"paused": store.NeedsInput, "mid": store.Idle})
	if err := s.Reap(context.Background(), 1, 0); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if closed["cc-paused"] {
		t.Error("cap eviction MUST spare a human-paused session, even as oldest-activity (ADR 0037)")
	}
	if !closed["cc-mid"] {
		t.Errorf("cap eviction must fall through to the oldest NON-paused session; closed=%v", closed)
	}
	if closed["cc-fresh"] {
		t.Error("most-recently-active session must survive")
	}
}

// TestReap_capEvictionLeavesPoolOverCapWhenAllPaused pins the ACCEPTED FAILURE
// MODE of ADR 0037: when every live session is human-paused, reap closes NOTHING
// and the pool is deliberately left ABOVE max_sessions. Cap eviction gets no
// last-resort override — continuity beats efficiency (INV-PREC-1), and the cap is
// not an admission gate (Ensure never consults max_sessions), so an over-cap pool
// cannot starve new work. Only an operator (`ccpool close`) clears these.
func TestReap_capEvictionLeavesPoolOverCapWhenAllPaused(t *testing.T) {
	now := time.Unix(10_000, 0)
	// All three are past idle_ttl AND over cap=1: both passes want them all gone.
	s, closed := reapFixtureStates(t, now,
		map[string]int64{"p1": 7200, "p2": 5400, "p3": 4000},
		map[string]store.State{"p1": store.NeedsInput, "p2": store.NeedsInput, "p3": store.NeedsInput})
	if err := s.Reap(context.Background(), 1, time.Hour); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(closed) != 0 {
		t.Errorf("an all-paused pool MUST be left over cap (nothing closed); closed=%v", closed)
	}
}

// TestReap_prunesRowsWhoseSessionGone: a DEAD row (no live tmux) whose Claude
// session is gone from disk is removed by reconcile (ADR 0015), while a dead row
// that is still resumable is KEPT (resume later).
func TestReap_prunesRowsWhoseSessionGone(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(10_000, 0)
	st := newMemStore(t)
	// gone: dead + transcript absent → pruned. keep: dead + transcript on disk → kept.
	_ = st.Insert(ctx, store.Session{
		ExternalID: "gone", ClaudeSessionID: "csid-gone", TranscriptPath: "/p/gone.jsonl", State: store.Idle,
		TmuxSession: "cc-gone", CreatedAt: now.Unix() - 7200, LastActivityAt: now.Unix() - 7200,
	})
	_ = st.Insert(ctx, store.Session{
		ExternalID: "keep", ClaudeSessionID: "csid-keep", TranscriptPath: "/p/keep.jsonl", State: store.Idle,
		TmuxSession: "cc-keep", CreatedAt: now.Unix() - 7200, LastActivityAt: now.Unix() - 7200,
	})

	// A per-path exister: keep's transcript is on disk, gone's is not.
	tm := &reapTmux{live: map[string]bool{}, closed: map[string]bool{}}
	s := New(Deps{
		Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-",
		Exister: existerByPath{"/p/keep.jsonl": true}, Now: func() time.Time { return now },
	})

	if err := s.Reap(ctx, 6, time.Hour); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if _, ok, _ := st.GetByExternalID(ctx, "gone"); ok {
		t.Error("a dead row whose Claude session is gone must be pruned")
	}
	if _, ok, _ := st.GetByExternalID(ctx, "keep"); !ok {
		t.Error("a dead but still-resumable row must be KEPT")
	}
}

// TestReap_prunesDeadNeedsInputPhantom pins ADR 0037 decision point 4: the
// human-paused carve-out governs CLOSURE only, never Pass 0's phantom prune. A
// `needs_input` row that is NOT live and whose Claude session is gone from disk
// holds no attachable context, so there is nothing to preserve — it is pruned like
// any other phantom.
func TestReap_prunesDeadNeedsInputPhantom(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(10_000, 0)
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{
		ExternalID: "ghost", ClaudeSessionID: "csid-ghost", TranscriptPath: "/p/ghost.jsonl",
		State: store.NeedsInput, TmuxSession: "cc-ghost",
		CreatedAt: now.Unix() - 7200, LastActivityAt: now.Unix() - 7200,
	})
	tm := &reapTmux{live: map[string]bool{}, closed: map[string]bool{}}
	s := New(Deps{
		Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-",
		Exister: fakeExister{ok: false}, Now: func() time.Time { return now },
	})

	if err := s.Reap(ctx, 6, time.Hour); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if _, ok, _ := st.GetByExternalID(ctx, "ghost"); ok {
		t.Error("a DEAD needs_input row whose Claude session is gone must still be pruned (ADR 0037 point 4)")
	}
}

// TestReap_doesNotPruneFreshStartingDeadRow guards the fresh-session race: a
// young `starting` row with no live tmux and no transcript yet is NOT pruned.
func TestReap_doesNotPruneFreshStartingDeadRow(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(10_000, 0)
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{
		ExternalID: "fresh", ClaudeSessionID: "csid-fresh", State: store.Starting,
		TmuxSession: "cc-fresh", CreatedAt: now.Unix(), LastActivityAt: now.Unix(),
	})
	tm := &reapTmux{live: map[string]bool{}, closed: map[string]bool{}}
	s := New(Deps{
		Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-",
		Exister: fakeExister{ok: false}, Now: func() time.Time { return now },
	})

	if err := s.Reap(ctx, 6, time.Hour); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if _, ok, _ := st.GetByExternalID(ctx, "fresh"); !ok {
		t.Error("a fresh starting row must NOT be pruned (it may not have written a transcript yet)")
	}
}

// existerByPath is a per-transcript-path SessionExister for tests with a mix of
// resumable/gone rows.
type existerByPath map[string]bool

func (e existerByPath) Exists(transcriptPath string) bool { return e[transcriptPath] }

// --- ccpool_reap_closures_total / ccpool_reap_phantom_pruned_total /
// ccpool_sessions_preserved_for_human call-site behavior (design D6/D11,
// pg2-qye99.6). recordReapClosure/recordReapPhantomPruned/
// recordSessionsPreservedForHuman are spied via the package-level
// indirection vars (declared in session.go) so these tests observe exactly
// what Reap chose, without depending on telemetry's own process-wide lazy
// instrument singleton.

// withReapSpies substitutes the three reap-related Record* indirections with
// spies for the duration of fn, then restores the real telemetry-backed
// functions.
func withReapSpies(t *testing.T, fn func(closures *[]string, phantomPruned *int, preserved *[]int64)) {
	t.Helper()
	origClosure, origPhantom, origPreserved := recordReapClosure, recordReapPhantomPruned, recordSessionsPreservedForHuman
	t.Cleanup(func() {
		recordReapClosure, recordReapPhantomPruned, recordSessionsPreservedForHuman = origClosure, origPhantom, origPreserved
	})
	var closures []string
	var phantomPruned int
	var preserved []int64
	recordReapClosure = func(reason string) { closures = append(closures, reason) }
	recordReapPhantomPruned = func() { phantomPruned++ }
	recordSessionsPreservedForHuman = func(count int64) { preserved = append(preserved, count) }
	fn(&closures, &phantomPruned, &preserved)
}

// TestReap_recordsClosureReasonPerPass confirms ccpool_reap_closures_total
// fires with reason=idle_ttl for a TTL closure and reason=cap_eviction for a
// cap-eviction closure, in the SAME invocation, matching each pass's own
// closures exactly (design D6).
func TestReap_recordsClosureReasonPerPass(t *testing.T) {
	withReapSpies(t, func(closures *[]string, phantomPruned *int, preserved *[]int64) {
		now := time.Unix(10_000, 0)
		// "stale" is idle past ttl (idle_ttl); cap=2 then forces one more
		// cap_eviction closure among the remaining sessions ("mid"). "mid" is
		// modeled as Idle since cap eviction is no longer permitted to target
		// a Ready (in-progress) row (ADR 0072).
		s, closed := reapFixtureStates(t, now, map[string]int64{"fresh": 10, "mid": 100, "stale": 7200},
			map[string]store.State{"mid": store.Idle})
		if err := s.Reap(context.Background(), 1, time.Hour); err != nil {
			t.Fatalf("Reap: %v", err)
		}
		if !closed["cc-stale"] || !closed["cc-mid"] {
			t.Fatalf("closed = %v, want both cc-stale and cc-mid closed", closed)
		}
		got := map[string]int{}
		for _, r := range *closures {
			got[r]++
		}
		if got["idle_ttl"] != 1 || got["cap_eviction"] != 1 {
			t.Errorf("recordReapClosure calls = %v, want exactly one idle_ttl and one cap_eviction", *closures)
		}
		if *phantomPruned != 0 {
			t.Errorf("phantomPruned = %d, want 0 (no dead rows in this fixture)", *phantomPruned)
		}
	})
}

// TestReap_recordsPhantomPrune confirms ccpool_reap_phantom_pruned_total
// fires exactly once for a dead row whose Claude session is gone, and that
// closure recording is untouched (a phantom prune is not a closure).
func TestReap_recordsPhantomPrune(t *testing.T) {
	withReapSpies(t, func(closures *[]string, phantomPruned *int, preserved *[]int64) {
		ctx := context.Background()
		now := time.Unix(10_000, 0)
		st := newMemStore(t)
		_ = st.Insert(ctx, store.Session{
			ExternalID: "gone", ClaudeSessionID: "csid-gone", TranscriptPath: "/p/gone.jsonl", State: store.Idle,
			TmuxSession: "cc-gone", CreatedAt: now.Unix() - 7200, LastActivityAt: now.Unix() - 7200,
		})
		tm := &reapTmux{live: map[string]bool{}, closed: map[string]bool{}}
		s := New(Deps{
			Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-",
			Exister: existerByPath{}, Now: func() time.Time { return now },
		})
		if err := s.Reap(ctx, 6, time.Hour); err != nil {
			t.Fatalf("Reap: %v", err)
		}
		if *phantomPruned != 1 {
			t.Errorf("phantomPruned = %d, want 1", *phantomPruned)
		}
		if len(*closures) != 0 {
			t.Errorf("recordReapClosure calls = %v, want none (a phantom prune is not a closure)", *closures)
		}
	})
}

// TestReap_recordsSessionsPreservedForHuman confirms
// ccpool_sessions_preserved_for_human is Set (gauge) to the count of live
// rows for which preservedForHuman is true, even when nothing is closed.
func TestReap_recordsSessionsPreservedForHuman(t *testing.T) {
	withReapSpies(t, func(closures *[]string, phantomPruned *int, preserved *[]int64) {
		now := time.Unix(10_000, 0)
		s, _ := reapFixtureStates(t, now,
			map[string]int64{"paused1": 10, "paused2": 10, "fresh": 10},
			map[string]store.State{"paused1": store.NeedsInput, "paused2": store.NeedsInput})
		if err := s.Reap(context.Background(), 6, time.Hour); err != nil {
			t.Fatalf("Reap: %v", err)
		}
		if len(*preserved) != 1 || (*preserved)[0] != 2 {
			t.Errorf("recordSessionsPreservedForHuman calls = %v, want exactly one call with 2", *preserved)
		}
	})
}

// TestReap_recordsZeroPreservedWhenNoneParked confirms the gauge is still
// recorded (as 0) when no session is human-paused, so a dashboard reading it
// sees an explicit zero rather than a stale prior value.
func TestReap_recordsZeroPreservedWhenNoneParked(t *testing.T) {
	withReapSpies(t, func(closures *[]string, phantomPruned *int, preserved *[]int64) {
		now := time.Unix(10_000, 0)
		s, _ := reapFixture(t, now, map[string]int64{"fresh": 10})
		if err := s.Reap(context.Background(), 6, time.Hour); err != nil {
			t.Fatalf("Reap: %v", err)
		}
		if len(*preserved) != 1 || (*preserved)[0] != 0 {
			t.Errorf("recordSessionsPreservedForHuman calls = %v, want exactly one call with 0", *preserved)
		}
	})
}

// TestReap_capCountsOnlyNonPreservedSessions confirms preserved (needs_input)
// rows sit outside max_sessions entirely: 6 preserved rows plus 2 working rows
// does not exceed a cap of 6, because only the 2 working rows count (ADR 0072
// Decision 1).
func TestReap_capCountsOnlyNonPreservedSessions(t *testing.T) {
	now := time.Unix(10_000, 0)
	ages := map[string]int64{"work-a": 10, "work-b": 20}
	states := map[string]store.State{"work-a": store.Working, "work-b": store.Working}
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("paused-%d", i)
		ages[id] = int64(3000 + i)
		states[id] = store.NeedsInput
	}
	s, closed := reapFixtureStates(t, now, ages, states)
	if err := s.Reap(context.Background(), 6, 0); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	for _, id := range []string{"cc-work-a", "cc-work-b"} {
		if closed[id] {
			t.Fatalf("%s was closed; preserved rows must not count toward the cap (ADR 0072)", id)
		}
	}
}

// TestReap_capEvictionNeverTargetsWorkingSessions confirms cap eviction skips
// a Working row even when it is the oldest-activity candidate and only closes
// the Idle row (ADR 0072 Decision 2).
func TestReap_capEvictionNeverTargetsWorkingSessions(t *testing.T) {
	now := time.Unix(10_000, 0)
	s, closed := reapFixtureStates(t, now,
		map[string]int64{"idle-old": 3000, "work-1": 1000, "work-2": 10},
		map[string]store.State{"idle-old": store.Idle, "work-1": store.Working, "work-2": store.Working})
	if err := s.Reap(context.Background(), 2, 0); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if !closed["cc-idle-old"] {
		t.Error("idle-old must be evicted (only evictable row)")
	}
	if closed["cc-work-1"] || closed["cc-work-2"] {
		t.Errorf("working rows must never be cap-evicted; closed=%v", closed)
	}
}

// TestReap_capEvictionSparesStartingAndReady confirms starting/ready rows are
// not evictable — only the Errored row (oldest among evictable) closes (ADR
// 0072 Decision 2).
func TestReap_capEvictionSparesStartingAndReady(t *testing.T) {
	now := time.Unix(10_000, 0)
	s, closed := reapFixtureStates(t, now,
		map[string]int64{"starting": 3000, "ready": 2000, "errored": 1000},
		map[string]store.State{"starting": store.Starting, "ready": store.Ready, "errored": store.Errored})
	if err := s.Reap(context.Background(), 1, 0); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if closed["cc-starting"] || closed["cc-ready"] {
		t.Errorf("starting/ready rows are not evictable; closed=%v", closed)
	}
	if !closed["cc-errored"] {
		t.Error("errored row is evictable and oldest among evictable; must close")
	}
}

// TestReap_capEvictionLeavesPoolOverCapWhenOnlyWorkingRemain confirms cap
// eviction gets no last-resort override for Working rows: when nothing
// evictable remains, the pool is deliberately left over cap (admission
// control, not eviction, bounds working sessions — ADR 0072 Decision 2).
func TestReap_capEvictionLeavesPoolOverCapWhenOnlyWorkingRemain(t *testing.T) {
	now := time.Unix(10_000, 0)
	s, closed := reapFixtureStates(t, now,
		map[string]int64{"w1": 3000, "w2": 2000, "w3": 1000},
		map[string]store.State{"w1": store.Working, "w2": store.Working, "w3": store.Working})
	if err := s.Reap(context.Background(), 1, 0); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(closed) != 0 {
		t.Fatalf("cap pressure must go unrelieved when only working rows remain; closed=%v", closed)
	}
}

// TestReap_ttlStillClosesHungWorkingSession pins that Pass 1 (idle_ttl) is
// unchanged by this packet: a hung Working row idle past idle_ttl is still
// closed, even though cap eviction (Pass 2) would never target it (ADR 0072).
func TestReap_ttlStillClosesHungWorkingSession(t *testing.T) {
	now := time.Unix(10_000, 0)
	s, closed := reapFixtureStates(t, now,
		map[string]int64{"hung": 7200},
		map[string]store.State{"hung": store.Working})
	if err := s.Reap(context.Background(), 6, 30*time.Minute); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if !closed["cc-hung"] {
		t.Fatal("a working row idle past idle_ttl must still be closed by Pass 1")
	}
}

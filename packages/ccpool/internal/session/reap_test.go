package session

import (
	"context"
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
func TestReap_overCapClosesOldestFirst(t *testing.T) {
	now := time.Unix(10_000, 0)
	s, closed := reapFixture(t, now, map[string]int64{"fresh": 10, "mid": 100, "old": 1000})
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
// session instead. Preserved sessions still COUNT toward the cap, so the pressure
// (one closure) is unchanged; only the victim differs.
func TestReap_capEvictionSparesHumanPausedSession(t *testing.T) {
	now := time.Unix(10_000, 0)
	// oldest-activity first: paused(3000s) < mid(1000s) < fresh(10s). idleTTL=0
	// disables the TTL pass, so this is purely cap eviction.
	s, closed := reapFixtureStates(t, now,
		map[string]int64{"paused": 3000, "mid": 1000, "fresh": 10},
		map[string]store.State{"paused": store.NeedsInput})
	if err := s.Reap(context.Background(), 2, 0); err != nil {
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

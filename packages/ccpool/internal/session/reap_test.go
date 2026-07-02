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
func reapFixture(t *testing.T, now time.Time, ages map[string]int64) (*Service, map[string]bool) {
	t.Helper()
	ctx := context.Background()
	st := newMemStore(t)
	liveMap := map[string]bool{}
	for externalID, ageSec := range ages {
		_ = st.Insert(ctx, store.Session{
			ExternalID: externalID, ClaudeSessionID: "csid-" + externalID, State: store.Ready,
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

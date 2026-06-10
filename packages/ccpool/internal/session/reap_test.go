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

// reapFixture builds N live sessions with the given ages (seconds idle) and a
// service whose tmux reports them all live and records closures.
func reapFixture(t *testing.T, now time.Time, ages map[string]int64) (*Service, map[string]bool) {
	t.Helper()
	ctx := context.Background()
	st := newMemStore(t)
	liveMap := map[string]bool{}
	for name, ageSec := range ages {
		_ = st.Insert(ctx, store.Session{Name: name, UUID: "u-" + name, State: store.Ready,
			TmuxSession: "cc-" + name, LastActivityAt: now.Unix() - ageSec})
		liveMap["cc-"+name] = true
	}
	closed := map[string]bool{}
	tm := &reapTmux{live: liveMap, closed: closed}
	s := New(Deps{Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-",
		Now: func() time.Time { return now }})
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

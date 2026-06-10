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

func (r *reapTmux) HasSession(name string) bool                          { return r.live[name] }
func (r *reapTmux) NewSession(string, map[string]string, []string) error { return nil }
func (r *reapTmux) SendKeys(string, ...string) error                     { return nil }
func (r *reapTmux) Paste(string, string) error                           { return nil }
func (r *reapTmux) KillSession(name string) error {
	r.closed[name] = true
	r.live[name] = false
	return nil
}

func TestReap_closesIdlePastTTL_andOverCap(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	now := time.Unix(10_000, 0)
	// 3 live sessions; cap=2, idle_ttl=1h.
	mk := func(name string, ageSec int64) {
		_ = st.Insert(ctx, store.Session{Name: name, UUID: "u-" + name, State: store.Ready,
			TmuxSession: "cc-" + name, LastActivityAt: now.Unix() - ageSec})
	}
	mk("fresh", 10)              // newest
	mk("mid", 100)               // middle
	mk("stale", 7200)            // 2h idle → past ttl
	tm := &closeTmux{live: true} // all live; records kills
	closed := map[string]bool{}
	tm2 := &reapTmux{live: map[string]bool{"cc-fresh": true, "cc-mid": true, "cc-stale": true}, closed: closed}
	_ = tm

	s := New(Deps{Tmux: tm2, Trust: &fakeTrust{}, Store: st, Prefix: "cc-",
		Now: func() time.Time { return now }})

	if err := s.Reap(ctx, 2, time.Hour); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	// stale closed (idle past ttl); over-cap (3 live, cap 2) closes the LRU among
	// the rest → "mid" (older than "fresh"). "fresh" survives.
	if !closed["cc-stale"] {
		t.Error("stale (idle past ttl) should be closed")
	}
	if !closed["cc-mid"] {
		t.Error("over-cap LRU (mid) should be closed")
	}
	if closed["cc-fresh"] {
		t.Error("freshest session must survive")
	}
}

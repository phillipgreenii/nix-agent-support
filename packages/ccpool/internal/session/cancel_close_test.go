package session

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// closeTmux: lets the test control when HasSession flips to false.
type closeTmux struct {
	live      bool
	killed    bool
	keys      [][]string
	pasted    []string
	goneAfter int // HasSession returns false after this many HasSession calls
	calls     int
}

func (c *closeTmux) HasSession(string) bool {
	c.calls++
	if c.goneAfter > 0 && c.calls > c.goneAfter {
		return false
	}
	return c.live
}
func (c *closeTmux) NewSession(string, string, map[string]string, []string) error { return nil }
func (c *closeTmux) SendKeys(_ string, keys ...string) error {
	c.keys = append(c.keys, keys)
	return nil
}
func (c *closeTmux) Paste(_, body string) error { c.pasted = append(c.pasted, body); return nil }
func (c *closeTmux) KillSession(string) error   { c.killed = true; return nil }

func TestCancel_sendsEscape_resetsToReady(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{Name: "a", UUID: "u", State: store.Working, TmuxSession: "cc-a"})
	tm := &closeTmux{live: true}
	s := New(Deps{Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Now: func() time.Time { return time.Unix(1, 0) }})

	if err := s.Cancel(ctx, "a"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	// Escape sent.
	sawEscape := false
	for _, k := range tm.keys {
		for _, key := range k {
			if key == "Escape" {
				sawEscape = true
			}
		}
	}
	if !sawEscape {
		t.Errorf("Cancel did not send Escape; keys=%v", tm.keys)
	}
	row, _, _ := st.GetByName(ctx, "a")
	if row.State != store.Ready {
		t.Errorf("state after cancel = %q, want ready (no Stop fires on interrupt)", row.State)
	}
}

func TestClose_graceful(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{Name: "a", UUID: "u", State: store.Ready, TmuxSession: "cc-a"})
	tm := &closeTmux{live: true, goneAfter: 1} // vanishes after the first liveness poll
	s := New(Deps{Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Now: func() time.Time { return time.Unix(1, 0) }})

	if err := s.Close(ctx, "a", false); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if tm.killed {
		t.Error("graceful close should not force-kill when /exit worked")
	}
	// /exit delivered (raw command, not space-guarded).
	sawExit := false
	for _, p := range tm.pasted {
		if p == "/exit" {
			sawExit = true
		}
	}
	if !sawExit {
		t.Errorf("close did not deliver /exit; pasted=%v", tm.pasted)
	}
}

func TestClose_forceKillsWhenExitIgnored(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{Name: "a", UUID: "u", State: store.Ready, TmuxSession: "cc-a"})
	tm := &closeTmux{live: true} // never vanishes → force kill
	s := New(Deps{Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Now: func() time.Time { return time.Unix(1, 0) }})

	if err := s.Close(ctx, "a", false); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !tm.killed {
		t.Error("close should force-kill when /exit is ignored")
	}
}

func TestClose_purgeDeletesRow(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{Name: "a", UUID: "u", State: store.Done, TmuxSession: "cc-a"})
	tm := &closeTmux{live: false} // already cold
	s := New(Deps{Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Now: func() time.Time { return time.Unix(1, 0) }})

	if err := s.Close(ctx, "a", true); err != nil {
		t.Fatalf("Close purge: %v", err)
	}
	if _, ok, _ := st.GetByName(ctx, "a"); ok {
		t.Error("--purge should delete the store row")
	}
}

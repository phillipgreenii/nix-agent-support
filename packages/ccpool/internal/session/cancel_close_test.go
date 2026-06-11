package session

import (
	"context"
	"errors"
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
	pane      string // returned by CapturePane
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
func (c *closeTmux) KillSession(string) error                  { c.killed = true; return nil }
func (c *closeTmux) CapturePane(string) (string, error)        { return c.pane, nil }

func TestCancel_burstThenVerify_landed_resetsReady(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{Name: "a", UUID: "u", State: store.Working, TmuxSession: "cc-a"})
	tm := &closeTmux{live: true, pane: "  ⎿  Interrupted · What should Claude do instead?"}
	s := New(Deps{Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Now: func() time.Time { return time.Unix(1, 0) }})

	if err := s.Cancel(ctx, "a"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	// burst of escapeBurst Escapes, then a C-u clear
	escapes := 0
	for _, k := range tm.keys {
		if len(k) == 1 && k[0] == "Escape" {
			escapes++
		}
	}
	if escapes != escapeBurst {
		t.Errorf("sent %d Escapes, want %d", escapes, escapeBurst)
	}
	row, _, _ := st.GetByName(ctx, "a")
	if row.State != store.Ready {
		t.Errorf("state = %s, want ready", row.State)
	}
}

func TestCancel_burstThenVerify_missed_staysWorking_returnsErr(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{Name: "a", UUID: "u", State: store.Working, TmuxSession: "cc-a"})
	tm := &closeTmux{live: true, pane: "  42. still streaming a fact..."}
	s := New(Deps{Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Now: func() time.Time { return time.Unix(1, 0) }})

	err := s.Cancel(ctx, "a")
	if !errors.Is(err, ErrCancelUnconfirmed) {
		t.Fatalf("err = %v, want ErrCancelUnconfirmed", err)
	}
	row, _, _ := st.GetByName(ctx, "a")
	if row.State != store.Working {
		t.Errorf("state = %s, want working (not falsely idle)", row.State)
	}
}

func TestSendInterrupt_abortsOnUnconfirmedCancel(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{Name: "a", UUID: "u", State: store.Working, TmuxSession: "cc-a"})
	tm := &closeTmux{live: true, pane: "still streaming"} // cancel will not confirm
	s := New(Deps{Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Now: func() time.Time { return time.Unix(1, 0) }})

	_, err := s.Send(ctx, "a", "next prompt", ModeInterrupt)
	if !errors.Is(err, ErrCancelUnconfirmed) {
		t.Fatalf("Send(ModeInterrupt) err = %v, want ErrCancelUnconfirmed", err)
	}
	// must NOT have delivered (no paste) into the un-interrupted turn
	if len(tm.pasted) != 0 {
		t.Errorf("delivered %v; must abort before paste on unconfirmed cancel", tm.pasted)
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

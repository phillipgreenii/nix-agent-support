package session

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// closeTmux: lets the test control when HasSession flips to false, and scripts
// the CapturePane sequence so the pane-stability confirmation loop can be driven
// hermetically (no real claude, no clock fake).
type closeTmux struct {
	live      bool
	killed    bool
	keys      [][]string
	pasted    []string
	goneAfter int      // HasSession returns false after this many HasSession calls
	calls     int      // HasSession call count
	panes     []string // scripted CapturePane sequence; last element is sticky
	capCalls  int      // CapturePane call count
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

// CapturePane returns the scripted panes in order; once exhausted it keeps
// returning the LAST element (a stopped turn keeps rendering the same bytes).
// An empty script returns "" (Close* tests never inspect the pane).
func (c *closeTmux) CapturePane(string) (string, error) {
	if len(c.panes) == 0 {
		return "", nil
	}
	i := c.capCalls
	if i >= len(c.panes) {
		i = len(c.panes) - 1 // sticky last value
	}
	c.capCalls++
	return c.panes[i], nil
}

// tickingPanes returns n byte-distinct panes, each carrying a live counter, to
// model a turn that never stops animating. Callers that want "never confirms"
// MUST pass exactly cancelMaxSamples: a shorter slice would let the sticky-last
// value manufacture a run of identical reads at the tail and false-confirm.
func tickingPanes(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("✽ Working… (%ds · ↓ %d tokens · thinking with xhigh effort)", i+1, (i+1)*7)
	}
	return out
}

func TestCancel_confirmsWhenPaneGoesStable(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{Name: "a", UUID: "u", State: store.Working, TmuxSession: "cc-a"})
	// Pane animates (thinking), then goes STATIC to a rewound input box with NO
	// "Interrupted" marker and NO live counter — the thinking-rewind end state
	// (rep1.post.txt). Stability must confirm even though no marker is present.
	thinking := "✽ Envisioning… (5s · ↓ 13 tokens · thinking with xhigh effort)"
	rewound := "❯ Think step by step in extensive detail...\n  -- INSERT --"
	tm := &closeTmux{live: true, panes: []string{thinking, rewound}}
	s := New(Deps{Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Now: func() time.Time { return time.Unix(1, 0) }})

	if err := s.Cancel(ctx, "a"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if tm.capCalls <= 1 {
		t.Errorf("expected confirmStable to poll more than once; capCalls=%d", tm.capCalls)
	}
	// burst of escapeBurst Escapes still happens
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

func TestCancel_neverStableStaysWorking(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{Name: "a", UUID: "u", State: store.Working, TmuxSession: "cc-a"})
	// Always-changing pane (a ticking counter) → never K identical reads →
	// unconfirmed. Exactly cancelMaxSamples distinct panes (see tickingPanes).
	tm := &closeTmux{live: true, panes: tickingPanes(cancelMaxSamples)}
	s := New(Deps{Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Now: func() time.Time { return time.Unix(1, 0) }})

	err := s.Cancel(ctx, "a")
	if !errors.Is(err, ErrCancelUnconfirmed) {
		t.Fatalf("err = %v, want ErrCancelUnconfirmed", err)
	}
	if tm.capCalls != cancelMaxSamples {
		t.Errorf("capCalls = %d, want %d (full sample budget)", tm.capCalls, cancelMaxSamples)
	}
	row, _, _ := st.GetByName(ctx, "a")
	if row.State != store.Working {
		t.Errorf("state = %s, want working (not falsely idle)", row.State)
	}
}

func TestCancel_liveCounterBlocksFalseConfirm(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{Name: "a", UUID: "u", State: store.Working, TmuxSession: "cc-a"})
	// Pathological: the pane is byte-identical on every read (would satisfy
	// stability) yet still carries a live counter line. The defense-in-depth
	// guard must reject it so we never confirm a turn rendering a (frozen) counter.
	frozen := "✽ Envisioning… (5s · ↓ 13 tokens · thinking with xhigh effort)"
	tm := &closeTmux{live: true, panes: []string{frozen}} // single element → sticky → always identical
	s := New(Deps{Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Now: func() time.Time { return time.Unix(1, 0) }})

	err := s.Cancel(ctx, "a")
	if !errors.Is(err, ErrCancelUnconfirmed) {
		t.Fatalf("err = %v, want ErrCancelUnconfirmed (guard must block a frozen live-counter pane)", err)
	}
	row, _, _ := st.GetByName(ctx, "a")
	if row.State != store.Working {
		t.Errorf("state = %s, want working", row.State)
	}
}

func TestSendInterrupt_abortsOnUnconfirmedCancel(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{Name: "a", UUID: "u", State: store.Working, TmuxSession: "cc-a"})
	tm := &closeTmux{live: true, panes: tickingPanes(cancelMaxSamples)} // cancel will not confirm
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

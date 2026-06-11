package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/notify"
	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/wait"
)

// recording tmux for send delivery
type sendTmux struct {
	live   bool
	pasted []string
	keys   [][]string
	pane   string
}

func (s *sendTmux) HasSession(string) bool                                       { return s.live }
func (s *sendTmux) NewSession(string, string, map[string]string, []string) error { return nil }
func (s *sendTmux) SendKeys(_ string, keys ...string) error {
	s.keys = append(s.keys, keys)
	return nil
}
func (s *sendTmux) Paste(_, body string) error { s.pasted = append(s.pasted, body); return nil }
func (s *sendTmux) KillSession(string) error                { return nil }
func (s *sendTmux) CapturePane(string) (string, error)      { return s.pane, nil }

type fakeTranscript struct {
	reply    string
	awaiting bool
}

func (f fakeTranscript) LastAssistantText(string) (string, error) { return f.reply, nil }
func (f fakeTranscript) IsAwaitingInput(string) (bool, error)     { return f.awaiting, nil }

func newSendService(t *testing.T, st *store.Store, tm Tmux, tr Transcript, w Waiter) *Service {
	t.Helper()
	return New(Deps{
		Tmux: tm, Trust: &fakeTrust{}, Store: st, Wait: w, Transcript: tr,
		Socket: "ccpool", Prefix: "cc-", PluginDir: "/p", ClaudeBin: "claude",
		NewUUID: func() string { return "x" }, Now: func() time.Time { return time.Unix(1, 0) },
	})
}

func TestSend_done_returnsReply_andGuardsLeadingSlash(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{Name: "a", UUID: "u", State: store.Ready, TmuxSession: "cc-a", TranscriptPath: "/p/a.jsonl"})
	tm := &sendTmux{live: true}
	tr := fakeTranscript{reply: "the answer"}
	// waiter flips to done when called.
	w := waitFunc(func(_ context.Context, name string, since int64) (wait.Outcome, error) {
		_, _ = st.Transition(ctx, name, store.Done, "", "")
		return wait.Outcome{State: store.Done}, nil
	})
	s := newSendService(t, st, tm, tr, w)

	res, err := s.Send(ctx, "a", "/etc/hosts please review", ModeRefuseIfBusy)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.State != store.Done || res.Reply != "the answer" {
		t.Errorf("res = %+v", res)
	}
	// leading-/ guard: pasted body must start with a space then the slash.
	if len(tm.pasted) != 1 || !strings.HasPrefix(tm.pasted[0], " /etc/hosts") {
		t.Errorf("pasted = %q, want leading-space-guarded", tm.pasted)
	}
	// Enter sent to submit.
	if len(tm.keys) == 0 || tm.keys[len(tm.keys)-1][0] != "Enter" {
		t.Errorf("expected a trailing Enter, keys=%v", tm.keys)
	}
}

func TestSend_refusesWhenBusy(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{Name: "a", UUID: "u", State: store.Working, TmuxSession: "cc-a"})
	s := newSendService(t, st, &sendTmux{live: true}, fakeTranscript{}, waitFunc(nil))
	_, err := s.Send(ctx, "a", "hi", ModeRefuseIfBusy)
	if err == nil {
		t.Fatal("Send must refuse a busy session in default mode")
	}
	if !errors.Is(err, ErrBusy) {
		t.Errorf("busy refusal must wrap ErrBusy (for exit-code 5 mapping); got %v", err)
	}
}

func TestSend_interrupt_cancelsThenDelivers(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{Name: "a", UUID: "u", State: store.Working, TmuxSession: "cc-a", TranscriptPath: "/p/a.jsonl"})
	tm := &sendTmux{live: true}
	tr := fakeTranscript{reply: "done now"}
	w := waitFunc(func(_ context.Context, name string, _ int64) (wait.Outcome, error) {
		_, _ = st.Transition(ctx, name, store.Done, "", "")
		return wait.Outcome{State: store.Done}, nil
	})
	s := newSendService(t, st, tm, tr, w)
	res, err := s.Send(ctx, "a", "hi", ModeInterrupt)
	if err != nil {
		t.Fatalf("Send interrupt: %v", err)
	}
	if res.State != store.Done || res.Reply != "done now" {
		t.Errorf("res = %+v", res)
	}
	// Escape must have been sent (the cancel) before delivery.
	sawEscape := false
	for _, k := range tm.keys {
		for _, key := range k {
			if key == "Escape" {
				sawEscape = true
			}
		}
	}
	if !sawEscape {
		t.Error("ModeInterrupt must Escape the current turn before delivering")
	}
}

func TestSend_queue_deliversWithoutWaiting(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{Name: "a", UUID: "u", State: store.Working, TmuxSession: "cc-a"})
	tm := &sendTmux{live: true}
	// waiter must NOT be called for queue mode; fail if it is.
	w := waitFunc(func(_ context.Context, _ string, _ int64) (wait.Outcome, error) {
		t.Error("ModeQueue must not wait")
		return wait.Outcome{}, nil
	})
	s := newSendService(t, st, tm, fakeTranscript{}, w)
	res, err := s.Send(ctx, "a", "later prompt", ModeQueue)
	if err != nil {
		t.Fatalf("Send queue: %v", err)
	}
	if res.State != store.Working {
		t.Errorf("queue result state = %q, want working (fire-and-forget)", res.State)
	}
	if len(tm.pasted) != 1 {
		t.Errorf("queue must still deliver the prompt; pasted=%v", tm.pasted)
	}
}

func TestSend_timeoutFallsBackToNeedsInput(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{Name: "a", UUID: "u", State: store.Ready, TmuxSession: "cc-a", TranscriptPath: "/p/a.jsonl"})
	tr := fakeTranscript{awaiting: true} // transcript shows a dangling question
	w := waitFunc(func(_ context.Context, _ string, _ int64) (wait.Outcome, error) {
		return wait.Outcome{State: store.Working, TimedOut: true}, nil
	})
	s := newSendService(t, st, &sendTmux{live: true}, tr, w)
	res, err := s.Send(ctx, "a", "pick one", ModeRefuseIfBusy)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.State != store.NeedsInput {
		t.Errorf("timeout+awaiting should yield needs_input, got %+v", res)
	}
}

type recordNotify struct{ events []notify.Event }

func (r *recordNotify) Notify(e notify.Event) error { r.events = append(r.events, e); return nil }

// The §8.3 step-6 fallback (timeout -> awaiting) fires NO Notification hook, so
// Send must drive the notifier itself (spec §10).
func TestSend_fallbackFiresNotifier(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{Name: "a", UUID: "u", State: store.Ready, TmuxSession: "cc-a", TranscriptPath: "/p/a.jsonl"})
	w := waitFunc(func(_ context.Context, _ string, _ int64) (wait.Outcome, error) {
		return wait.Outcome{State: store.Working, TimedOut: true}, nil
	})
	rn := &recordNotify{}
	s := New(Deps{
		Tmux: &sendTmux{live: true}, Trust: &fakeTrust{}, Store: st, Wait: w,
		Transcript: fakeTranscript{awaiting: true},
		Notify:     rn, NotifyOn: []string{"needs_input", "failed"},
		Socket: "ccpool", Prefix: "cc-", PluginDir: "/p", ClaudeBin: "claude",
		NewUUID: func() string { return "x" }, Now: func() time.Time { return time.Unix(1, 0) },
	})
	res, err := s.Send(ctx, "a", "pick one", ModeRefuseIfBusy)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.State != store.NeedsInput {
		t.Fatalf("state = %v, want needs_input", res.State)
	}
	if len(rn.events) != 1 || rn.events[0].State != "needs_input" || rn.events[0].Name != "a" {
		t.Errorf("fallback must fire exactly one needs_input notification; got %+v", rn.events)
	}
}

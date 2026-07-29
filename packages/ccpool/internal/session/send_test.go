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

func (s *sendTmux) Paste(_, body string) error         { s.pasted = append(s.pasted, body); return nil }
func (s *sendTmux) KillSession(string) error           { return nil }
func (s *sendTmux) CapturePane(string) (string, error) { return s.pane, nil }

type fakeTranscript struct {
	reply    string
	awaiting bool
	// firstAt/firstOK back FirstMessageActivity. Zero firstOK means "no model
	// turn yet" (the dropped-prompt case).
	firstAt time.Time
	firstOK bool
}

func (f fakeTranscript) LastAssistantText(string) (string, error) { return f.reply, nil }
func (f fakeTranscript) IsAwaitingInput(string) (bool, error)     { return f.awaiting, nil }
func (f fakeTranscript) FirstMessageActivity(string) (time.Time, bool) {
	return f.firstAt, f.firstOK
}

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
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "u", Name: "a", State: store.Ready, TmuxSession: "cc-a", TranscriptPath: "/p/a.jsonl"})
	tm := &sendTmux{live: true}
	tr := fakeTranscript{reply: "the answer"}
	// waiter flips to done when called.
	w := waitFunc(func(_ context.Context, name string, since int64) (wait.Outcome, error) {
		_, _ = st.Transition(ctx, name, store.Idle, "", "")
		return wait.Outcome{State: store.Idle}, nil
	})
	s := newSendService(t, st, tm, tr, w)

	res, err := s.Send(ctx, "a", "/etc/hosts please review", ModeRefuseIfBusy)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.State != store.Idle || res.Reply != "the answer" {
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
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "u", Name: "a", State: store.Working, TmuxSession: "cc-a"})
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
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "u", Name: "a", State: store.Working, TmuxSession: "cc-a", TranscriptPath: "/p/a.jsonl"})
	tm := &sendTmux{live: true, pane: "Interrupted"}
	tr := fakeTranscript{reply: "done now"}
	w := waitFunc(func(_ context.Context, name string, _ int64) (wait.Outcome, error) {
		_, _ = st.Transition(ctx, name, store.Idle, "", "")
		return wait.Outcome{State: store.Idle}, nil
	})
	s := newSendService(t, st, tm, tr, w)
	res, err := s.Send(ctx, "a", "hi", ModeInterrupt)
	if err != nil {
		t.Fatalf("Send interrupt: %v", err)
	}
	if res.State != store.Idle || res.Reply != "done now" {
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
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "u", Name: "a", State: store.Working, TmuxSession: "cc-a"})
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
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "u", Name: "a", State: store.Ready, TmuxSession: "cc-a", TranscriptPath: "/p/a.jsonl"})
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

// The AskUserQuestion timeout fallback (timeout -> transcript says awaiting)
// fires NO Notification hook, so Send must drive the notifier itself.
func TestSend_fallbackFiresNotifier(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "u", Name: "a", State: store.Ready, TmuxSession: "cc-a", TranscriptPath: "/p/a.jsonl"})
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

func TestSend_confirmIngest_dropped_returnsNotIngested(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "u", Name: "a", State: store.Ready, TmuxSession: "cc-a", TranscriptPath: "/p/a.jsonl"})
	tm := &sendTmux{live: true}
	// firstOK=false ⇒ no model turn ever appears in the window.
	tr := fakeTranscript{firstOK: false}
	s := newSendService(t, st, tm, tr, waitFunc(nil))
	// confirm window 30ms ⇒ a single bounded check that observes no turn.
	_, err := s.SendWithConfirm(ctx, "a", "do the task", ModeNoWait, 30*time.Millisecond)
	if !errors.Is(err, ErrPromptNotIngested) {
		t.Fatalf("dropped prompt must yield ErrPromptNotIngested, got %v", err)
	}
	// The prompt was still pasted (delivery happened; only confirmation failed).
	if len(tm.pasted) != 1 {
		t.Errorf("prompt should still have been delivered; pasted=%v", tm.pasted)
	}
}

func TestSend_confirmIngest_ingested_ok(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "u", Name: "a", State: store.Ready, TmuxSession: "cc-a", TranscriptPath: "/p/a.jsonl"})
	tm := &sendTmux{live: true}
	// firstOK=true ⇒ a model turn is present ⇒ ingested.
	tr := fakeTranscript{firstOK: true, firstAt: time.Unix(100, 0)}
	s := newSendService(t, st, tm, tr, waitFunc(nil))
	res, err := s.SendWithConfirm(ctx, "a", "do the task", ModeNoWait, 30*time.Millisecond)
	if err != nil {
		t.Fatalf("ingested prompt must succeed, got %v", err)
	}
	if res.State != store.Working {
		t.Errorf("no-wait confirmed result state = %q, want working", res.State)
	}
}

func TestSend_noConfirmWindow_skipsCheck(t *testing.T) {
	// A zero window keeps today's fire-and-forget behavior: no transcript read,
	// no ErrPromptNotIngested even when firstOK=false.
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "u", Name: "a", State: store.Ready, TmuxSession: "cc-a", TranscriptPath: "/p/a.jsonl"})
	s := newSendService(t, st, &sendTmux{live: true}, fakeTranscript{firstOK: false}, waitFunc(nil))
	res, err := s.SendWithConfirm(ctx, "a", "do it", ModeNoWait, 0)
	if err != nil {
		t.Fatalf("zero window must skip the ingestion check, got %v", err)
	}
	if res.State != store.Working {
		t.Errorf("state = %q, want working", res.State)
	}
}

func TestErrPromptNotIngested_isExported(t *testing.T) {
	// A sentinel the CLI maps to a distinct exit code; must be a stable value
	// callers can errors.Is against.
	if ErrPromptNotIngested == nil {
		t.Fatal("ErrPromptNotIngested must be a non-nil sentinel error")
	}
	if !strings.Contains(ErrPromptNotIngested.Error(), "ingest") {
		t.Errorf("error text = %q, want it to mention ingest", ErrPromptNotIngested.Error())
	}
}

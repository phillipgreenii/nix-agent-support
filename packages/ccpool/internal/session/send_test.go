package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/wait"
)

// recording tmux for send delivery
type sendTmux struct {
	live   bool
	pasted []string
	keys   [][]string
}

func (s *sendTmux) HasSession(string) bool                               { return s.live }
func (s *sendTmux) NewSession(string, map[string]string, []string) error { return nil }
func (s *sendTmux) SendKeys(_ string, keys ...string) error {
	s.keys = append(s.keys, keys)
	return nil
}
func (s *sendTmux) Paste(_, body string) error { s.pasted = append(s.pasted, body); return nil }
func (s *sendTmux) KillSession(string) error   { return nil }

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
	if _, err := s.Send(ctx, "a", "hi", ModeRefuseIfBusy); err == nil {
		t.Error("Send must refuse a busy session in default mode")
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

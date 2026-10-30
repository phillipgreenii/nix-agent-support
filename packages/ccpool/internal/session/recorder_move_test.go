package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/wait"
)

// feedTranscript is a Transcript that is also a FeedWaiter: the confirmation
// waits on the feed instead of polling the transcript.
type feedTranscript struct {
	fakeTranscript
	appeared bool
	markErr  error
	waited   *time.Duration
	marked   *bool
}

func (f feedTranscript) Mark(context.Context) (string, error) {
	if f.marked != nil {
		*f.marked = true
	}
	return "1:5", f.markErr
}

func (f feedTranscript) WaitMessage(_ context.Context, _, mark string, window time.Duration) (bool, error) {
	if f.waited != nil {
		*f.waited = window
	}
	if mark != "1:5" {
		return false, errors.New("wait did not use the mark taken before delivery")
	}
	return f.appeared, nil
}

func feedService(t *testing.T, tr Transcript, tm *sendTmux) *Service {
	t.Helper()
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "u", Name: "a", State: store.Ready, TmuxSession: "cc-a", TranscriptPath: "/p/a.jsonl"})
	return newSendService(t, st, tm, tr, waitFunc(func(context.Context, string, int64) (wait.Outcome, error) { return wait.Outcome{}, nil }))
}

func TestSend_confirmIngest_waitsOnTheFeed(t *testing.T) {
	var waited time.Duration
	var marked bool
	tr := feedTranscript{appeared: true, waited: &waited, marked: &marked}
	s := feedService(t, tr, &sendTmux{live: true})
	if _, err := s.SendWithConfirm(context.Background(), "a", "do it", ModeNoWait, 5*time.Second); err != nil {
		t.Fatalf("SendWithConfirm: %v", err)
	}
	if !marked || waited != 5*time.Second {
		t.Errorf("marked=%v waited=%v; want a mark before delivery and one wait of the whole window", marked, waited)
	}
}

func TestSend_confirmIngest_feedNoMatchIsNotIngested(t *testing.T) {
	s := feedService(t, feedTranscript{appeared: false}, &sendTmux{live: true})
	_, err := s.SendWithConfirm(context.Background(), "a", "do it", ModeNoWait, 20*time.Millisecond)
	if !errors.Is(err, ErrPromptNotIngested) {
		t.Fatalf("err = %v; want ErrPromptNotIngested", err)
	}
}

// With the recorder down the confirmation fails with its error before anything is
// delivered: no guess from files.
func TestSend_confirmIngest_markErrorFailsBeforeDelivery(t *testing.T) {
	offline := errors.New("daemon offline")
	tm := &sendTmux{live: true}
	s := feedService(t, feedTranscript{markErr: offline}, tm)
	_, err := s.SendWithConfirm(context.Background(), "a", "do it", ModeNoWait, time.Second)
	if !errors.Is(err, offline) {
		t.Fatalf("err = %v; want the mark error", err)
	}
	if len(tm.pasted) != 0 {
		t.Errorf("a prompt was delivered despite the failed mark: %q", tm.pasted)
	}
}

type fakeControl struct {
	submits, interrupts, runs, stops []string
	err                              error
}

func (c *fakeControl) Submit(_ context.Context, id, text string) error {
	c.submits = append(c.submits, id+"|"+text)
	return c.err
}

func (c *fakeControl) Interrupt(_ context.Context, id string) error {
	c.interrupts = append(c.interrupts, id)
	return c.err
}

func (c *fakeControl) Run(_ context.Context, id, cmd string) error {
	c.runs = append(c.runs, id+"|"+cmd)
	return c.err
}

func (c *fakeControl) Stop(_ context.Context, id string) error {
	c.stops = append(c.stops, id)
	return c.err
}

func TestSend_deliversThroughControl(t *testing.T) {
	ctl := &fakeControl{}
	tm := &sendTmux{live: true}
	s := feedService(t, fakeTranscript{}, tm)
	s.d.Control = ctl
	if _, err := s.Send(context.Background(), "a", "/path please", ModeNoWait); err != nil {
		t.Fatal(err)
	}
	if len(ctl.submits) != 1 || ctl.submits[0] != "u| /path please" {
		t.Errorf("submits = %q; want one guarded submit to the Claude session", ctl.submits)
	}
	if len(tm.pasted) != 0 {
		t.Errorf("tmux paste used although control delivered: %q", tm.pasted)
	}
}

func TestSend_fallsBackToTmuxWhenNoControl(t *testing.T) {
	ctl := &fakeControl{err: ErrNoControl}
	tm := &sendTmux{live: true}
	s := feedService(t, fakeTranscript{}, tm)
	s.d.Control = ctl
	if _, err := s.Send(context.Background(), "a", "hello", ModeNoWait); err != nil {
		t.Fatal(err)
	}
	if len(tm.pasted) != 1 {
		t.Errorf("pasted = %q; want the tmux fallback when the Claude has no control mod", tm.pasted)
	}
}

func TestSend_controlFailureIsNotSwallowed(t *testing.T) {
	ctl := &fakeControl{err: errors.New("exit 2")}
	tm := &sendTmux{live: true}
	s := feedService(t, fakeTranscript{}, tm)
	s.d.Control = ctl
	if _, err := s.Send(context.Background(), "a", "hello", ModeNoWait); err == nil {
		t.Fatal("a real control failure must fail the send")
	}
	if len(tm.pasted) != 0 {
		t.Error("must not fall back to tmux on a real control failure")
	}
}

func TestClose_stopsThroughControl(t *testing.T) {
	ctl := &fakeControl{}
	tm := &sendTmux{live: true}
	s := feedService(t, fakeTranscript{}, tm)
	s.d.Control = ctl
	_ = s.Close(context.Background(), "a", false)
	if len(ctl.stops) != 1 || ctl.stops[0] != "u" {
		t.Errorf("stops = %q; want one stop of the Claude session", ctl.stops)
	}
}

type downGate struct{}

func (downGate) Ping(context.Context) error { return errors.New("daemon offline") }

func TestCapacity_refusesWhenGateIsDown(t *testing.T) {
	s, _ := reapFixtureStates(t, time.Unix(10_000, 0),
		map[string]int64{"w": 1}, map[string]store.State{"w": store.Working})
	s.d.Gate = downGate{}
	if _, err := s.Capacity(context.Background(), 6); err == nil {
		t.Fatal("Capacity must fail when the fact source is down")
	}
}

type checkedExister struct {
	ok  bool
	err error
}

func (c checkedExister) Exists(string) bool                 { return c.ok }
func (c checkedExister) ExistsChecked(string) (bool, error) { return c.ok, c.err }

func TestResumable_probeErrorIsNotGone(t *testing.T) {
	s := feedService(t, fakeTranscript{}, &sendTmux{live: true})
	row := store.Session{TranscriptPath: "/p/a.jsonl"}
	s.d.Exister = checkedExister{err: errors.New("daemon offline")}
	if ok, err := s.resumable(row); ok || err == nil {
		t.Fatalf("resumable = %v, %v; want the probe error", ok, err)
	}
	s.d.Exister = checkedExister{ok: true}
	if ok, err := s.resumable(row); !ok || err != nil {
		t.Fatalf("resumable = %v, %v; want true", ok, err)
	}
}

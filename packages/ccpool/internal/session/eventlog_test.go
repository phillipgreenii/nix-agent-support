package session

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/eventlog"
	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/wait"
)

// newLoggedStore builds an in-memory store wired to a real event log at path,
// so both store transitions AND session input actions land in one ordered log.
func newLoggedStore(t *testing.T, el *eventlog.Logger) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:", &clock.Fake{T: time.Unix(100, 0).UTC()}, store.WithEventLog(el))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// kinds/actions/tos extract ordered projections of the log for sequence asserts.
func actions(evs []eventlog.Event) []string {
	var out []string
	for _, e := range evs {
		if e.Kind == "input" {
			out = append(out, e.Action)
		}
	}
	return out
}

// A successful send must record the ordered input sequence clear-input, paste,
// enter (harvest #8 adjacent: turn-stopped / no-rewind-side-effect), interleaved
// with the working/done transitions — all recoverable from the one log.
func TestSend_recordsOrderedInputSequence(t *testing.T) {
	ctx := context.Background()
	events := filepath.Join(t.TempDir(), "events.jsonl")
	el, err := eventlog.Open(events)
	if err != nil {
		t.Fatalf("eventlog.Open: %v", err)
	}
	st := newLoggedStore(t, el)
	_ = st.Insert(ctx, store.Session{Name: "a", UUID: "u", State: store.Ready, TmuxSession: "cc-a", TranscriptPath: "/p/a.jsonl"})

	tm := &sendTmux{live: true}
	tr := fakeTranscript{reply: "ok"}
	w := waitFunc(func(_ context.Context, name string, _ int64) (wait.Outcome, error) {
		_, _ = st.Transition(ctx, name, store.Done, "", "")
		return wait.Outcome{State: store.Done}, nil
	})
	s := New(Deps{
		Tmux: tm, Trust: &fakeTrust{}, Store: st, Wait: w, Transcript: tr,
		Events: el, Socket: "ccpool", Prefix: "cc-", PluginDir: "/p", ClaudeBin: "claude",
		NewUUID: func() string { return "x" }, Now: func() time.Time { return time.Unix(1, 0) },
	})

	if _, err := s.Send(ctx, "a", "hello", ModeRefuseIfBusy); err != nil {
		t.Fatalf("Send: %v", err)
	}

	evs, err := eventlog.Read(events)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// Ordered input actions for the delivery.
	gotActions := actions(evs)
	want := []string{"clear-input", "paste", "enter"}
	if len(gotActions) != len(want) {
		t.Fatalf("input actions = %v, want %v", gotActions, want)
	}
	for i := range want {
		if gotActions[i] != want[i] {
			t.Fatalf("input action[%d] = %q, want %q (full: %v)", i, gotActions[i], want[i], gotActions)
		}
	}
	// The paste detail must NOT leak the prompt body.
	for _, e := range evs {
		if e.Action == "paste" && e.Detail == "hello" {
			t.Errorf("paste event leaked the prompt body: %+v", e)
		}
	}
	// End-to-end ordering: working transition precedes the input burst, which
	// precedes the done transition.
	idxWorking, idxClear, idxDone := -1, -1, -1
	for i, e := range evs {
		switch {
		case e.Kind == "transition" && e.To == string(store.Working):
			idxWorking = i
		case e.Kind == "input" && e.Action == "clear-input":
			idxClear = i
		case e.Kind == "transition" && e.To == string(store.Done):
			idxDone = i
		}
	}
	if !(idxWorking >= 0 && idxClear > idxWorking && idxDone > idxClear) {
		t.Errorf("expected ordered working < clear-input < done; got working=%d clear=%d done=%d (evs=%+v)",
			idxWorking, idxClear, idxDone, evs)
	}
}

// Reap eviction (harvest #7) closes an idle session via Close→Transition(Done).
// The store-wired event log must capture that transition so the eviction is
// recoverable as an ordered event even though the store overwrites the row.
func TestReap_evictionTransitionIsLogged(t *testing.T) {
	ctx := context.Background()
	events := filepath.Join(t.TempDir(), "events.jsonl")
	el, err := eventlog.Open(events)
	if err != nil {
		t.Fatalf("eventlog.Open: %v", err)
	}
	st := newLoggedStore(t, el)

	now := time.Unix(10_000, 0)
	// One stale (idle past TTL) and one fresh session, both live.
	_ = st.Insert(ctx, store.Session{Name: "stale", UUID: "u-stale", State: store.Ready,
		TmuxSession: "cc-stale", LastActivityAt: now.Unix() - 7200})
	_ = st.Insert(ctx, store.Session{Name: "fresh", UUID: "u-fresh", State: store.Ready,
		TmuxSession: "cc-fresh", LastActivityAt: now.Unix() - 10})
	tm := &reapTmux{live: map[string]bool{"cc-stale": true, "cc-fresh": true}, closed: map[string]bool{}}
	s := New(Deps{Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-",
		Events: el, Now: func() time.Time { return now }})

	if err := s.Reap(ctx, 2, time.Hour); err != nil {
		t.Fatalf("Reap: %v", err)
	}

	evs, err := eventlog.Read(events)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// The stale session must have a logged ready→done transition; the fresh one
	// must NOT (it survived the reap).
	sawStaleDone, sawFreshDone := false, false
	for _, e := range evs {
		if e.Kind != "transition" || e.To != string(store.Done) {
			continue
		}
		switch e.Name {
		case "stale":
			sawStaleDone = true
			if e.From != string(store.Ready) {
				t.Errorf("eviction transition from = %q, want ready", e.From)
			}
		case "fresh":
			sawFreshDone = true
		}
	}
	if !sawStaleDone {
		t.Errorf("reap eviction of the stale session must log a done transition; evs=%+v", evs)
	}
	if sawFreshDone {
		t.Errorf("the surviving fresh session must NOT be evicted/logged; evs=%+v", evs)
	}
}

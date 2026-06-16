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
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "u", Name: "a", State: store.Ready, TmuxSession: "cc-a", TranscriptPath: "/p/a.jsonl"})

	tm := &sendTmux{live: true}
	tr := fakeTranscript{reply: "ok"}
	w := waitFunc(func(_ context.Context, name string, _ int64) (wait.Outcome, error) {
		_, _ = st.Transition(ctx, name, store.Idle, "", "")
		return wait.Outcome{State: store.Idle}, nil
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
		case e.Kind == "transition" && e.To == string(store.Idle):
			idxDone = i
		}
	}
	if !(idxWorking >= 0 && idxClear > idxWorking && idxDone > idxClear) {
		t.Errorf("expected ordered working < clear-input < done; got working=%d clear=%d done=%d (evs=%+v)",
			idxWorking, idxClear, idxDone, evs)
	}
}

// Reap eviction tears down the stale session's tmux but, per ADR 0015, does NOT
// fabricate a settled state: Close no longer reconciles to idle/done, so the row
// keeps its last OBSERVED state and the event log records NO eviction transition.
// The fresh session must survive untouched.
func TestReap_evictionTearsDownTmux_noFabricatedTransition(t *testing.T) {
	ctx := context.Background()
	events := filepath.Join(t.TempDir(), "events.jsonl")
	el, err := eventlog.Open(events)
	if err != nil {
		t.Fatalf("eventlog.Open: %v", err)
	}
	st := newLoggedStore(t, el)

	now := time.Unix(10_000, 0)
	// One stale (idle past TTL) and one fresh session, both live and resumable so
	// the prune pass leaves them alone (eviction is by TTL, not prune).
	_ = st.Insert(ctx, store.Session{ExternalID: "stale", ClaudeSessionID: "u-stale", Name: "stale", State: store.Ready,
		TmuxSession: "cc-stale", LastActivityAt: now.Unix() - 7200})
	_ = st.Insert(ctx, store.Session{ExternalID: "fresh", ClaudeSessionID: "u-fresh", Name: "fresh", State: store.Ready,
		TmuxSession: "cc-fresh", LastActivityAt: now.Unix() - 10})
	tm := &reapTmux{live: map[string]bool{"cc-stale": true, "cc-fresh": true}, closed: map[string]bool{}}
	s := New(Deps{Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Exister: fakeExister{ok: true},
		Events: el, Now: func() time.Time { return now }})

	if err := s.Reap(ctx, 2, time.Hour); err != nil {
		t.Fatalf("Reap: %v", err)
	}

	// The stale session's tmux must be torn down; the fresh one must remain live.
	if tm.live["cc-stale"] {
		t.Error("stale (idle past ttl) session must have its tmux torn down")
	}
	if !tm.live["cc-fresh"] {
		t.Error("fresh session must survive the reap")
	}
	// Both rows must KEEP their last observed state (close fabricates nothing).
	for _, id := range []string{"stale", "fresh"} {
		if row, ok, _ := st.GetByExternalID(ctx, id); !ok || row.State != store.Ready {
			t.Errorf("row %q state = %v (ok=%v), want ready unchanged (no fabricated eviction state)", id, row.State, ok)
		}
	}
	// No eviction transition may be logged — Close stopped fabricating state.
	evs, err := eventlog.Read(events)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, e := range evs {
		if e.Kind == "transition" {
			t.Errorf("reap eviction must NOT log a fabricated transition; got %+v", e)
		}
	}
}

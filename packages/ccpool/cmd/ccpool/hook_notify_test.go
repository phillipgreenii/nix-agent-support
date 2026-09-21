package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/notify"
	"github.com/phillipgreenii/ccpool/internal/store"
)

type recordNotifier struct{ events []notify.Event }

func (r *recordNotifier) Notify(e notify.Event) error { r.events = append(r.events, e); return nil }

func TestHook_notify_firesOnEdgeIntoNeedsInput(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "csid-x", State: store.Working})
	rn := &recordNotifier{}
	const p = `{"session_id":"csid-x","transcript_path":"/p/x.jsonl","cwd":"/x","hook_event_name":"Notification","notification_type":"permission_prompt"}`
	if err := handleHookN("notify", strings.NewReader(p), st, "", rn, []string{"needs_input", "errored"}, nil, false, io.Discard); err != nil {
		t.Fatalf("handleHookN: %v", err)
	}
	if len(rn.events) != 1 || rn.events[0].State != "needs_input" || rn.events[0].Name != "a" {
		t.Errorf("expected one needs_input event, got %+v", rn.events)
	}
}

// TestHook_notify_idlePromptIsNoOp proves the pg2-8p1om fix: an idle_prompt
// notify (Claude Code's benign "still sitting at the prompt" ping) must leave
// the session's state untouched and must NOT fire the notifier, unlike a
// genuine permission_prompt (TestHook_notify_firesOnEdgeIntoNeedsInput above).
// Reproduces the live incident's exact shape: a session that finished cleanly
// (idle) got flipped back to needs_input ~60s later with zero real activity.
func TestHook_notify_idlePromptIsNoOp(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "csid-x", State: store.Idle})
	rn := &recordNotifier{}
	const p = `{"session_id":"csid-x","transcript_path":"/p/x.jsonl","cwd":"/x","hook_event_name":"Notification","notification_type":"idle_prompt"}`
	if err := handleHookN("notify", strings.NewReader(p), st, "", rn, []string{"needs_input", "errored"}, nil, false, io.Discard); err != nil {
		t.Fatalf("handleHookN: %v", err)
	}
	if len(rn.events) != 0 {
		t.Errorf("idle_prompt must not fire the notifier; got %+v", rn.events)
	}
	got, ok, err := st.GetByExternalID(ctx, "a")
	if err != nil || !ok {
		t.Fatalf("GetByExternalID: %v, ok=%v", err, ok)
	}
	if got.State != store.Idle {
		t.Errorf("idle_prompt must not transition state; got %q, want %q", got.State, store.Idle)
	}
}

// TestHook_ask_firesNotifierOnEdgeIntoNeedsInput proves the `ask` event drives the
// notifier on the working→needs_input edge exactly like the `notify` event does
// (the AskUserQuestion hook is the deterministic source of that edge, pg2-7a5b).
func TestHook_ask_firesNotifierOnEdgeIntoNeedsInput(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	_ = st.Insert(ctx, store.Session{ExternalID: "ext-alpha", ClaudeSessionID: "csid-x", State: store.Working})
	rn := &recordNotifier{}
	if err := handleHookN("ask", strings.NewReader(askPayload), st, "", rn, []string{"needs_input", "errored"}, nil, false, io.Discard); err != nil {
		t.Fatalf("handleHookN ask: %v", err)
	}
	if len(rn.events) != 1 || rn.events[0].State != "needs_input" || rn.events[0].Name != "ext-alpha" {
		t.Errorf("expected one needs_input event, got %+v", rn.events)
	}
}

func TestHook_notify_noEdgeNoFire(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "csid-x", State: store.NeedsInput}) // already needs_input
	rn := &recordNotifier{}
	const p = `{"session_id":"csid-x","hook_event_name":"Notification"}`
	if err := handleHookN("notify", strings.NewReader(p), st, "", rn, []string{"needs_input"}, nil, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(rn.events) != 0 {
		t.Errorf("no edge (needs_input→needs_input) must not fire; got %+v", rn.events)
	}
}

package main

import (
	"context"
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
	const p = `{"session_id":"csid-x","transcript_path":"/p/x.jsonl","cwd":"/x","hook_event_name":"Notification"}`
	if err := handleHookN("notify", strings.NewReader(p), st, "", rn, []string{"needs_input", "errored"}); err != nil {
		t.Fatalf("handleHookN: %v", err)
	}
	if len(rn.events) != 1 || rn.events[0].State != "needs_input" || rn.events[0].Name != "a" {
		t.Errorf("expected one needs_input event, got %+v", rn.events)
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
	if err := handleHookN("ask", strings.NewReader(askPayload), st, "", rn, []string{"needs_input", "errored"}); err != nil {
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
	if err := handleHookN("notify", strings.NewReader(p), st, "", rn, []string{"needs_input"}); err != nil {
		t.Fatal(err)
	}
	if len(rn.events) != 0 {
		t.Errorf("no edge (needs_input→needs_input) must not fire; got %+v", rn.events)
	}
}

package main

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/notify"
	"github.com/phillipgreenii/ccpool/internal/notify/notifytest"
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

// TestHook_fail_notifiesUnderDefaultConfig pins pg2-ix0txf: with the DEFAULT
// notify.on (loaded from an empty config home), a StopFailure hook that moves
// a session into errored posts exactly one notification. The default used to
// name a "failed" state the store never produces, so this never fired.
func TestHook_fail_notifiesUnderDefaultConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	st, _ := openTestStore(t)
	ctx := context.Background()
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "csid-x", State: store.Working})
	rn := &recordNotifier{}
	const p = `{"session_id":"csid-x","hook_event_name":"StopFailure","error":"server_error"}`
	if err := handleHookN("fail", strings.NewReader(p), st, "", rn, cfg.Notify.On, nil, false, io.Discard); err != nil {
		t.Fatalf("handleHookN: %v", err)
	}
	if len(rn.events) != 1 || rn.events[0].State != "errored" || rn.events[0].Name != "a" {
		t.Errorf("expected one errored event under default config, got %+v", rn.events)
	}
}

// CP-2/CP-4/CP-6: through the real Desktop adapter and a fake daemon socket,
// the hook posts exactly one banner with the contract fields.
func TestHook_desktop_postsThroughDaemon(t *testing.T) {
	cases := []struct {
		name, event, payload string
		from                 store.State
		wantTitle, wantBody  string
	}{
		{"notify", "notify", `{"session_id":"csid-x","cwd":"/x","hook_event_name":"Notification","notification_type":"permission_prompt"}`, store.Working, "ccpool: a", "needs_input — /x"},
		{"ask", "ask", askPayload, store.Working, "ccpool: a", "needs_input — /x"},
		{"errored", "fail", `{"session_id":"csid-x","hook_event_name":"StopFailure","error":"server_error"}`, store.Working, "ccpool: a", "errored"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := notifytest.Start(t)
			st, _ := openTestStore(t)
			_ = st.Insert(context.Background(), store.Session{ExternalID: "a", ClaudeSessionID: "csid-x", State: tc.from})
			n := notify.FromConfig("desktop", "")
			if err := handleHookN(tc.event, strings.NewReader(tc.payload), st, "", n, []string{"needs_input", "errored"}, nil, false, io.Discard); err != nil {
				t.Fatalf("handleHookN: %v", err)
			}
			reqs := srv.Requests()
			if len(reqs) != 1 {
				t.Fatalf("posts = %d, want 1", len(reqs))
			}
			a := reqs[0].Args
			if a["title"] != tc.wantTitle || a["body"] != tc.wantBody || a["source"] != "ccpool" || len(a) != 3 {
				t.Errorf("args = %v", a)
			}
		})
	}
}

// CP-3 and CP-1 through the daemon: no post for idle_prompt or a non-edge.
func TestHook_desktop_noPostWithoutEdge(t *testing.T) {
	srv := notifytest.Start(t)
	st, _ := openTestStore(t)
	_ = st.Insert(context.Background(), store.Session{ExternalID: "a", ClaudeSessionID: "csid-x", State: store.Idle})
	n := notify.FromConfig("desktop", "")
	const p = `{"session_id":"csid-x","hook_event_name":"Notification","notification_type":"idle_prompt"}`
	if err := handleHookN("notify", strings.NewReader(p), st, "", n, []string{"needs_input"}, nil, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := len(srv.Requests()); got != 0 {
		t.Errorf("posts = %d, want 0", got)
	}
}

// CP-7: an unreachable daemon never fails the hook and the state still moves.
func TestHook_desktop_unreachableDaemonDoesNotFailHook(t *testing.T) {
	t.Setenv("PG_OSX_NOTIFIER_SOCKET", filepath.Join(t.TempDir(), "absent.sock"))
	st, _ := openTestStore(t)
	ctx := context.Background()
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "csid-x", State: store.Working})
	n := notify.Desktop{Timeout: 50 * time.Millisecond}
	const p = `{"session_id":"csid-x","cwd":"/x","hook_event_name":"Notification","notification_type":"permission_prompt"}`
	if err := handleHookN("notify", strings.NewReader(p), st, "", n, []string{"needs_input"}, nil, false, io.Discard); err != nil {
		t.Fatalf("hook must not fail on an unreachable daemon: %v", err)
	}
	got, ok, err := st.GetByExternalID(ctx, "a")
	if err != nil || !ok || got.State != store.NeedsInput {
		t.Errorf("state = %v ok=%v err=%v, want needs_input", got.State, ok, err)
	}
}

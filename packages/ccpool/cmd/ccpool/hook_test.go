package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/store"
)

func openTestStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "store.db")
	st, err := store.Open(dbPath, &clock.Fake{T: time.Unix(2000, 0).UTC()})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, dbPath
}

const startPayload = `{"session_id":"csid-x","transcript_path":"/p/csid-x.jsonl","cwd":"/tmp/x","hook_event_name":"SessionStart","source":"startup"}`
const stopPayload = `{"session_id":"csid-x","transcript_path":"/p/csid-x.jsonl","hook_event_name":"Stop","stop_hook_active":false}`
const failPayload = `{"session_id":"csid-x","transcript_path":"/p/csid-x.jsonl","hook_event_name":"Stop"}`
const notifyPayload = `{"session_id":"csid-x","transcript_path":"/p/csid-x.jsonl","hook_event_name":"Notification"}`

// TestHook_start_resolvesByClaudeSessionID_setsReady: payload session_id matches
// an existing row's claude_session_id → row transitions to Ready.
func TestHook_start_resolvesByClaudeSessionID_setsReady(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-alpha", ClaudeSessionID: "csid-x", State: store.Starting}); err != nil {
		t.Fatal(err)
	}
	// No CCPOOL_EXTERNAL_ID — must resolve by claude_session_id.
	if err := handleHook("start", strings.NewReader(startPayload), st, ""); err != nil {
		t.Fatalf("handleHook start: %v", err)
	}
	got, ok, _ := st.GetByExternalID(ctx, "ext-alpha")
	if !ok {
		t.Fatal("row vanished")
	}
	if got.State != store.Ready {
		t.Errorf("state = %q, want ready", got.State)
	}
	if got.TranscriptPath != "/p/csid-x.jsonl" {
		t.Errorf("transcript not reconciled: %+v", got)
	}
}

// TestHook_start_resolvesByEnvExternalID_whenNoRowForSessionID: unknown
// session_id, CCPOOL_EXTERNAL_ID set → Upsert then Ready.
func TestHook_start_resolvesByEnvExternalID_whenNoRowForSessionID(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	// No pre-existing row; only the env external_id (the race case).
	if err := handleHook("start", strings.NewReader(startPayload), st, "ext-alpha"); err != nil {
		t.Fatalf("handleHook start: %v", err)
	}
	got, ok, _ := st.GetByExternalID(ctx, "ext-alpha")
	if !ok {
		t.Fatal("row not upserted")
	}
	if got.State != store.Ready {
		t.Errorf("state = %q, want ready", got.State)
	}
	if got.ClaudeSessionID != "csid-x" || got.TranscriptPath != "/p/csid-x.jsonl" {
		t.Errorf("reconcile failed: %+v", got)
	}
}

func TestHook_stop_setsIdle(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-alpha", ClaudeSessionID: "csid-x", State: store.Working}); err != nil {
		t.Fatal(err)
	}
	if err := handleHook("stop", strings.NewReader(stopPayload), st, ""); err != nil {
		t.Fatalf("handleHook stop: %v", err)
	}
	got, _, _ := st.GetByExternalID(ctx, "ext-alpha")
	if got.State != store.Idle {
		t.Errorf("state = %q, want idle", got.State)
	}
}

func TestHook_fail_setsErrored(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-alpha", ClaudeSessionID: "csid-x", State: store.Working}); err != nil {
		t.Fatal(err)
	}
	if err := handleHook("fail", strings.NewReader(failPayload), st, ""); err != nil {
		t.Fatalf("handleHook fail: %v", err)
	}
	got, _, _ := st.GetByExternalID(ctx, "ext-alpha")
	if got.State != store.Errored {
		t.Errorf("state = %q, want errored", got.State)
	}
}

func TestHook_notify_setsNeedsInput(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-alpha", ClaudeSessionID: "csid-x", State: store.Working}); err != nil {
		t.Fatal(err)
	}
	if err := handleHook("notify", strings.NewReader(notifyPayload), st, ""); err != nil {
		t.Fatalf("handleHook notify: %v", err)
	}
	got, _, _ := st.GetByExternalID(ctx, "ext-alpha")
	if got.State != store.NeedsInput {
		t.Errorf("state = %q, want needs_input", got.State)
	}
}

// TestHook_stop_resolvesPendingTurn proves the fire-and-forget emit→resolve flow
// at the store/cmd layer (pg2-12ko): a pending turn recorded at emit is stamped
// with the transcript anchor when the Stop hook fires, and `result` then resolves
// the reply lazily from that anchor.
func TestHook_stop_resolvesPendingTurn(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-alpha", ClaudeSessionID: "csid-x", State: store.Working}); err != nil {
		t.Fatal(err)
	}
	// EMIT: a fire-and-forget turn recorded pending (what runReply does).
	if err := st.InsertTurn(ctx, store.Turn{TurnID: "t-1", ExternalID: "ext-alpha", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}

	if err := handleHook("stop", strings.NewReader(stopPayload), st, ""); err != nil {
		t.Fatalf("handleHook stop: %v", err)
	}

	got, ok, err := st.GetTurn(ctx, "t-1")
	if err != nil || !ok {
		t.Fatalf("GetTurn: ok=%v err=%v", ok, err)
	}
	if got.Status != store.TurnResolved {
		t.Errorf("Status = %q, want resolved", got.Status)
	}
	if got.TranscriptPath != "/p/csid-x.jsonl" {
		t.Errorf("TranscriptPath = %q, want stamped from stop payload", got.TranscriptPath)
	}

	// RETRIEVE: result resolves the reply lazily from the stamped anchor.
	var out, errBuf bytes.Buffer
	if code := resultForTurn(ctx, st, fakeTranscript{text: "all done"}, "t-1", &out, &errBuf); code != 0 {
		t.Fatalf("resultForTurn code = %d, want 0; stderr=%q", code, errBuf.String())
	}
	if strings.TrimSpace(out.String()) != "all done" {
		t.Errorf("reply = %q, want %q", out.String(), "all done")
	}
}

// askPayload is a PreToolUse/AskUserQuestion payload (claude 2.1.177 shape):
// tool_name + tool_input.questions carry the prompt text the model just invoked.
const askPayload = `{"session_id":"csid-x","transcript_path":"/p/csid-x.jsonl","cwd":"/x","permission_mode":"bypassPermissions","hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_use_id":"tu-1","tool_input":{"questions":[{"question":"Which path? Alpha or Bravo","header":"Path","options":[{"label":"Alpha","description":"a"},{"label":"Bravo","description":"b"}],"multiSelect":false}]}}`

// askPayloadMulti carries two questions; the handler joins their text with "; ".
const askPayloadMulti = `{"session_id":"csid-x","transcript_path":"/p/csid-x.jsonl","cwd":"/x","hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"First?","header":"H1","options":[],"multiSelect":false},{"question":"Second?","header":"H2","options":[],"multiSelect":false}]}}`

func TestHook_ask_transitionsToNeedsInputAndRecordsQuestion(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-alpha", ClaudeSessionID: "csid-x", State: store.Working}); err != nil {
		t.Fatal(err)
	}
	if err := handleHook("ask", strings.NewReader(askPayload), st, ""); err != nil {
		t.Fatalf("handleHook ask: %v", err)
	}
	got, _, _ := st.GetByExternalID(ctx, "ext-alpha")
	if got.State != store.NeedsInput {
		t.Errorf("state = %q, want needs_input", got.State)
	}
	if got.PendingQuestion != "Which path? Alpha or Bravo" {
		t.Errorf("PendingQuestion = %q, want the question text", got.PendingQuestion)
	}
}

func TestHook_ask_joinsMultipleQuestions(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-alpha", ClaudeSessionID: "csid-x", State: store.Working}); err != nil {
		t.Fatal(err)
	}
	if err := handleHook("ask", strings.NewReader(askPayloadMulti), st, ""); err != nil {
		t.Fatalf("handleHook ask: %v", err)
	}
	got, _, _ := st.GetByExternalID(ctx, "ext-alpha")
	if got.PendingQuestion != "First?; Second?" {
		t.Errorf("PendingQuestion = %q, want %q", got.PendingQuestion, "First?; Second?")
	}
}

func TestHook_ask_malformedToolInput_stillNeedsInputEmptyQuestion(t *testing.T) {
	// Never-fail: a malformed tool_input must still record the needs_input edge
	// (the picker-detection signal survives); only the question text is lost.
	st, _ := openTestStore(t)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-alpha", ClaudeSessionID: "csid-x", State: store.Working}); err != nil {
		t.Fatal(err)
	}
	const payload = `{"session_id":"csid-x","hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":"garbage-not-an-object"}`
	if err := handleHook("ask", strings.NewReader(payload), st, ""); err != nil {
		t.Fatalf("handleHook ask (malformed): %v", err)
	}
	got, _, _ := st.GetByExternalID(ctx, "ext-alpha")
	if got.State != store.NeedsInput {
		t.Errorf("state = %q, want needs_input despite malformed tool_input", got.State)
	}
	if got.PendingQuestion != "" {
		t.Errorf("PendingQuestion = %q, want empty on malformed tool_input", got.PendingQuestion)
	}
}

func TestHook_notify_clearsStalePendingQuestion(t *testing.T) {
	// A notify (permission/idle prompt) needs_input edge is NOT an AskUserQuestion,
	// so it must clear any stale question left on the row from a prior ask turn.
	st, _ := openTestStore(t)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-alpha", ClaudeSessionID: "csid-x", State: store.NeedsInput, PendingQuestion: "old question?"}); err != nil {
		t.Fatal(err)
	}
	const payload = `{"session_id":"csid-x","hook_event_name":"Notification"}`
	if err := handleHook("notify", strings.NewReader(payload), st, ""); err != nil {
		t.Fatalf("handleHook notify: %v", err)
	}
	got, _, _ := st.GetByExternalID(ctx, "ext-alpha")
	if got.State != store.NeedsInput {
		t.Errorf("state = %q, want needs_input", got.State)
	}
	if got.PendingQuestion != "" {
		t.Errorf("PendingQuestion = %q, want cleared by notify", got.PendingQuestion)
	}
}

func TestHook_unresolvable_isNoErrorNoRow(t *testing.T) {
	st, _ := openTestStore(t)
	// session_id matches nothing and no CCPOOL_EXTERNAL_ID → log + succeed (exit 0).
	if err := handleHook("stop", strings.NewReader(stopPayload), st, ""); err != nil {
		t.Fatalf("unresolvable hook returned error, want nil: %v", err)
	}
	list, _ := st.List(context.Background())
	if len(list) != 0 {
		t.Errorf("rows = %d, want 0", len(list))
	}
}

func TestLogHook_writesStructuredJSONLAtErrorLevel(t *testing.T) {
	dir := t.TempDir()
	logHook(dir, "hook stop: store open: boom")
	b, err := os.ReadFile(filepath.Join(dir, "diagnostics.jsonl"))
	if err != nil {
		t.Fatalf("read diagnostics.jsonl: %v", err)
	}
	var got struct {
		Time  string `json:"time"`
		Level string `json:"level"`
		Msg   string `json:"msg"`
	}
	if err := json.Unmarshal(b[:len(b)-1], &got); err != nil { // strip trailing \n
		t.Fatalf("logHook wrote non-JSON %q: %v", b, err)
	}
	if got.Level != "error" {
		t.Errorf("level = %q, want error (every old hook.log line was a failure)", got.Level)
	}
	if got.Msg != "hook stop: store open: boom" {
		t.Errorf("msg = %q, want the diagnostic text", got.Msg)
	}
	if got.Time == "" {
		t.Error("time must be set (RFC3339)")
	}
}

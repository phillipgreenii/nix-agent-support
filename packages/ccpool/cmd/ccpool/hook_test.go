package main

import (
	"bytes"
	"context"
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

const startPayload = `{"session_id":"u-x","transcript_path":"/p/u-x.jsonl","cwd":"/tmp/x","hook_event_name":"SessionStart","source":"startup"}`
const stopPayload = `{"session_id":"u-x","transcript_path":"/p/u-x.jsonl","hook_event_name":"Stop","stop_hook_active":false}`

func TestHook_start_upsertsThenReady(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	// No pre-existing row; only CCPOOL_NAME in env (the race case, spec §9/§18).
	if err := handleHook("start", strings.NewReader(startPayload), st, "alpha"); err != nil {
		t.Fatalf("handleHook start: %v", err)
	}
	got, ok, _ := st.GetByName(ctx, "alpha")
	if !ok {
		t.Fatal("row not upserted")
	}
	if got.State != store.Ready {
		t.Errorf("state = %q, want ready", got.State)
	}
	if got.UUID != "u-x" || got.TranscriptPath != "/p/u-x.jsonl" {
		t.Errorf("reconcile failed: %+v", got)
	}
}

func TestHook_stop_resolvesByUUID_setsDone(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{Name: "alpha", UUID: "u-x", State: store.Working}); err != nil {
		t.Fatal(err)
	}
	// No CCPOOL_NAME — must resolve by session_id.
	if err := handleHook("stop", strings.NewReader(stopPayload), st, ""); err != nil {
		t.Fatalf("handleHook stop: %v", err)
	}
	got, _, _ := st.GetByName(ctx, "alpha")
	if got.State != store.Done {
		t.Errorf("state = %q, want done", got.State)
	}
}

// TestHook_stop_resolvesPendingTurn proves the fire-and-forget emit→resolve flow
// at the store/cmd layer (pg2-12ko): a pending turn recorded at emit is stamped
// with the transcript anchor when the Stop hook fires, and `result` then resolves
// the reply lazily from that anchor.
func TestHook_stop_resolvesPendingTurn(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{Name: "alpha", UUID: "u-x", State: store.Working}); err != nil {
		t.Fatal(err)
	}
	// EMIT: a fire-and-forget turn recorded pending (what runReply does).
	if err := st.InsertTurn(ctx, store.Turn{TurnID: "t-1", Name: "alpha", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}

	// Stop hook fires for the session → turn completes; the oldest pending turn
	// gets the transcript anchor stamped onto it.
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
	if got.TranscriptPath != "/p/u-x.jsonl" {
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
const askPayload = `{"session_id":"u-x","transcript_path":"/p/u-x.jsonl","cwd":"/x","permission_mode":"bypassPermissions","hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_use_id":"tu-1","tool_input":{"questions":[{"question":"Which path? Alpha or Bravo","header":"Path","options":[{"label":"Alpha","description":"a"},{"label":"Bravo","description":"b"}],"multiSelect":false}]}}`

// askPayloadMulti carries two questions; the handler joins their text with "; ".
const askPayloadMulti = `{"session_id":"u-x","transcript_path":"/p/u-x.jsonl","cwd":"/x","hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"First?","header":"H1","options":[],"multiSelect":false},{"question":"Second?","header":"H2","options":[],"multiSelect":false}]}}`

func TestHook_ask_transitionsToNeedsInputAndRecordsQuestion(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{Name: "alpha", UUID: "u-x", State: store.Working}); err != nil {
		t.Fatal(err)
	}
	if err := handleHook("ask", strings.NewReader(askPayload), st, ""); err != nil {
		t.Fatalf("handleHook ask: %v", err)
	}
	got, _, _ := st.GetByName(ctx, "alpha")
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
	if err := st.Insert(ctx, store.Session{Name: "alpha", UUID: "u-x", State: store.Working}); err != nil {
		t.Fatal(err)
	}
	if err := handleHook("ask", strings.NewReader(askPayloadMulti), st, ""); err != nil {
		t.Fatalf("handleHook ask: %v", err)
	}
	got, _, _ := st.GetByName(ctx, "alpha")
	if got.PendingQuestion != "First?; Second?" {
		t.Errorf("PendingQuestion = %q, want %q", got.PendingQuestion, "First?; Second?")
	}
}

func TestHook_unresolvable_isNoErrorNoRow(t *testing.T) {
	st, _ := openTestStore(t)
	// session_id matches nothing and no CCPOOL_NAME → log + succeed (exit 0).
	if err := handleHook("stop", strings.NewReader(stopPayload), st, ""); err != nil {
		t.Fatalf("unresolvable hook returned error, want nil: %v", err)
	}
	list, _ := st.List(context.Background())
	if len(list) != 0 {
		t.Errorf("rows = %d, want 0", len(list))
	}
}

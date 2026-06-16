package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// fakeTranscript stubs the transcript adapter so result resolution is hermetic.
type fakeTranscript struct {
	text string
	err  error
}

func (f fakeTranscript) LastAssistantText(string) (string, error) { return f.text, f.err }

func TestResultForTurn_resolved(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	if err := st.InsertTurn(ctx, store.Turn{TurnID: "t-1", Name: "alpha", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.ResolveOldestPendingTurn(ctx, "alpha", "/p/anchor.jsonl"); !ok || err != nil {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}

	var out, errBuf bytes.Buffer
	code := resultForTurn(ctx, st, fakeTranscript{text: "the reply"}, "t-1", &out, &errBuf)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, errBuf.String())
	}
	if got := strings.TrimSpace(out.String()); got != "the reply" {
		t.Errorf("stdout = %q, want %q", got, "the reply")
	}
}

func TestResultForTurn_pending(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	if err := st.InsertTurn(ctx, store.Turn{TurnID: "t-1", Name: "alpha", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}

	var out, errBuf bytes.Buffer
	code := resultForTurn(ctx, st, fakeTranscript{}, "t-1", &out, &errBuf)
	if code != 2 {
		t.Fatalf("code = %d, want 2 (pending)", code)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty on pending", out.String())
	}
	if !strings.Contains(errBuf.String(), "pending") {
		t.Errorf("stderr = %q, want a pending indicator", errBuf.String())
	}
}

func TestResultForTurn_unknown(t *testing.T) {
	st, _ := openTestStore(t)
	var out, errBuf bytes.Buffer
	code := resultForTurn(context.Background(), st, fakeTranscript{}, "nope", &out, &errBuf)
	if code != 1 {
		t.Fatalf("code = %d, want 1 (unknown)", code)
	}
	if errBuf.Len() == 0 {
		t.Error("want an error message on stderr for unknown turn")
	}
}

func TestResultForTurn_resolvedEmptyTranscript(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	// A resolved turn whose transcript_path was never stamped (empty) cannot be
	// read back → clear error, non-zero (not the generic pending code).
	if err := st.InsertTurn(ctx, store.Turn{TurnID: "t-1", Name: "alpha", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.ResolveOldestPendingTurn(ctx, "alpha", ""); !ok || err != nil {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}

	var out, errBuf bytes.Buffer
	code := resultForTurn(ctx, st, fakeTranscript{}, "t-1", &out, &errBuf)
	if code == 0 {
		t.Fatalf("code = %d, want non-zero for unreadable transcript", code)
	}
	if errBuf.Len() == 0 {
		t.Error("want an error message on stderr for empty transcript")
	}
}

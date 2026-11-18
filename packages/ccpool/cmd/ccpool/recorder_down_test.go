package main

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/session"
	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/tmux"
)

// seedRow inserts one row into the armed sandbox's store.
func seedRow(t *testing.T, a armedEnv, row store.Session) {
	t.Helper()
	st, err := store.Open(a.dbPath, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.Insert(context.Background(), row); err != nil {
		t.Fatal(err)
	}
}

// wantFinalRecorderError asserts the retried-then-final error text: the recorder
// named as unavailable, the attempts, and what to do.
func wantFinalRecorderError(t *testing.T, out string) {
	t.Helper()
	for _, want := range []string{"the recorder is not available", "did not answer after 2 attempts", "check that the recorder daemon is running"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q lacks %q", out, want)
		}
	}
}

// TestRunReply_recorderDownReturnsRetriedFinalError: there is no pre-gate. A
// reply to a session that needs a recorded fact (resumability) retries, then
// fails with the final error, and starts no tmux server and changes no row.
func TestRunReply_recorderDownReturnsRetriedFinalError(t *testing.T) {
	a := armedLaunchEnv(t)
	useDownRecorder(t)
	seedRow(t, a, store.Session{ExternalID: "alpha", ClaudeSessionID: "csid-a", CWD: a.cwd, TranscriptPath: "/p/alpha.jsonl", State: store.Idle, TmuxSession: "cc-alpha"})
	// reply reports through slog, which was bound before captureStderr swaps os.Stderr.
	var logs strings.Builder
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	out, code := captureStderr(t, func() int { return runReply([]string{"alpha", "hi", "--no-wait"}) })
	out += logs.String()
	if code == 0 {
		t.Fatalf("runReply with the recorder down = 0, %q; want failure", out)
	}
	wantFinalRecorderError(t, out)
	if socks := tmuxSocketsUnder(a.tmuxDir); len(socks) > 0 {
		t.Errorf("tmux server started despite the failed fact read: %v", socks)
	}
	st, err := store.Open(a.dbPath, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	row, ok, _ := st.GetByExternalID(context.Background(), "alpha")
	if !ok || row.State != store.Idle || row.Generation != 1 {
		t.Errorf("row after the failed reply = %+v, %v; want unchanged idle generation 1", row, ok)
	}
}

// TestRunResult_recorderDownReturnsRetriedFinalError: result reads the reply text
// from the recorder and reports the final error when it cannot.
func TestRunResult_recorderDownReturnsRetriedFinalError(t *testing.T) {
	a := armedLaunchEnv(t)
	useDownRecorder(t)
	st, err := store.Open(a.dbPath, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.InsertTurn(ctx, store.Turn{TurnID: "t-1", ExternalID: "alpha", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.ResolveOldestPendingTurn(ctx, "alpha", "/p/anchor.jsonl"); !ok || err != nil {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	_ = st.Close()
	out, code := captureStderr(t, func() int { return runResult([]string{"t-1"}) })
	if code != 1 {
		t.Fatalf("runResult with the recorder down = %d, %q; want 1", code, out)
	}
	wantFinalRecorderError(t, out)
}

// TestRunState_recorderDown: a session that is not live needs no recorded fact,
// so state answers with the recorder down; a live one reads facts and reports the
// final error instead of guessing.
func TestRunState_recorderDown(t *testing.T) {
	a := armedLaunchEnv(t)
	useDownRecorder(t)
	seedRow(t, a, store.Session{ExternalID: "alpha", ClaudeSessionID: "csid-a", CWD: a.cwd, TranscriptPath: "/p/alpha.jsonl", State: store.Idle, TmuxSession: "cc-alpha"})

	if out, code := captureStderr(t, func() int { return runState([]string{"alpha"}) }); code != 0 {
		t.Fatalf("state of a not-live session with the recorder down = %d, %q; want success", code, out)
	}

	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cl := tmux.NewClient(cfg.Tmux.Socket)
	name := session.TmuxName(cfg.Tmux.Prefix, "alpha")
	if err := cl.NewSession(name, a.cwd, nil, []string{"sleep", "120"}); err != nil {
		t.Skipf("cannot start a tmux session here: %v", err)
	}
	t.Cleanup(func() { _ = cl.KillSession(name) })
	out, code := captureStderr(t, func() int { return runState([]string{"alpha"}) })
	if code != 1 {
		t.Fatalf("state of a live session with the recorder down = %d, %q; want 1", code, out)
	}
	wantFinalRecorderError(t, out)
}

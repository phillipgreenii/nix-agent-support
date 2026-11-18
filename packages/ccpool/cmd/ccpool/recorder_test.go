package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/facts"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
)

// useRecorder starts an in-process recorder (the real daemon over throwaway
// directories, no started process) and points the package's fact reader at it for
// the test. It returns the environment so the test can write transcripts.
func useRecorder(t *testing.T) *clienttest.Env {
	t.Helper()
	e := clienttest.Start(t)
	old := factReader
	factReader = facts.NewWith(client.Options{Socket: e.Socket, Name: "ccpool-test", ConnectTimeout: time.Second})
	t.Cleanup(func() { factReader = old })
	return e
}

// useDownRecorder points the fact reader at a recorder that is stopped.
func useDownRecorder(t *testing.T) {
	t.Helper()
	e := useRecorder(t)
	e.Stop()
	t.Cleanup(facts.SetRetryPolicy(2, time.Millisecond, time.Second))
}

const testSess = "11111111-1111-4111-8111-111111111111"

func tuserLine(ts, text string) string {
	return fmt.Sprintf(`{"type":"user","uuid":"u-%s","sessionId":"%s","timestamp":"%s","message":{"role":"user","content":"%s"}}`+"\n", ts, testSess, ts, text)
}

func tAskLine(ts, toolID string) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":"q-%s","sessionId":"%s","timestamp":"%s","message":{"id":"m-%s","role":"assistant","content":[{"type":"tool_use","id":"%s","name":"AskUserQuestion","input":{}}],"usage":{"output_tokens":3}}}`+"\n", ts, testSess, ts, ts, toolID)
}

func tReplyLine(ts, text string) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":"a-%s","sessionId":"%s","timestamp":"%s","message":{"id":"m-%s","role":"assistant","content":[{"type":"text","text":"%s"}],"usage":{"output_tokens":2}}}`+"\n", ts, testSess, ts, ts, text)
}

// TestRunCapacity_succeedsWhenDaemonOffline: capacity reads only ccpool's own
// store and tmux, so it reports occupancy with the recorder down.
func TestRunCapacity_succeedsWhenDaemonOffline(t *testing.T) {
	armedLaunchEnv(t)
	useDownRecorder(t)
	out, code := captureStderr(t, func() int { return runCapacity([]string{"--json"}) })
	if code != 0 {
		t.Fatalf("runCapacity with the recorder down = %d, %q; want success", code, out)
	}
}

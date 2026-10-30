package facts_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/facts"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
)

const sess = "11111111-1111-4111-8111-111111111111"

func userLine(ts, text string) string {
	return fmt.Sprintf(`{"type":"user","uuid":"u-%s","sessionId":"%s","timestamp":"%s","message":{"role":"user","content":"%s"}}`+"\n", ts, sess, ts, text)
}

func assistantText(ts, msgID, text string, out int) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":"a-%s","sessionId":"%s","timestamp":"%s","message":{"id":"%s","role":"assistant","model":"example-model","content":[{"type":"text","text":"%s"}],"usage":{"input_tokens":1,"output_tokens":%d}}}`+"\n", ts, sess, ts, msgID, text, out)
}

func askLine(ts, msgID, toolID string) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":"q-%s","sessionId":"%s","timestamp":"%s","message":{"id":"%s","role":"assistant","content":[{"type":"tool_use","id":"%s","name":"AskUserQuestion","input":{}}],"usage":{"output_tokens":3}}}`+"\n", ts, sess, ts, msgID, toolID)
}

func resultLine(ts, toolID string) string {
	return fmt.Sprintf(`{"type":"user","uuid":"r-%s","sessionId":"%s","timestamp":"%s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"%s","content":"ok"}]}}`+"\n", ts, sess, ts, toolID)
}

func newReader(e *clienttest.Env) *facts.Reader {
	return facts.NewWith(client.Options{Socket: e.Socket, Name: "ccpool-test", ConnectTimeout: time.Second})
}

func transcriptPath(e *clienttest.Env) string {
	return e.Path("projects", e.Project, sess+".jsonl")
}

func TestFactReadsAgainstRealRecorder(t *testing.T) {
	e := clienttest.Start(t)
	rel := filepath.Join("projects", e.Project, sess+".jsonl")
	e.Write(rel, userLine("2026-01-01T00:00:01Z", "hello")+
		assistantText("2026-01-01T00:00:02Z", "m1", "first", 5)+
		assistantText("2026-01-01T00:00:03Z", "m1", "first", 5)+
		assistantText("2026-01-01T00:00:04Z", "m2", "the reply", 7))
	e.Settle()
	r := newReader(e)
	path := transcriptPath(e)

	if got, err := r.LastAssistantText(path); err != nil || got != "the reply" {
		t.Fatalf("LastAssistantText = %q, %v", got, err)
	}
	if tm, ok, err := r.MessageActivity(path); err != nil || !ok || !tm.Equal(time.Date(2026, 1, 1, 0, 0, 4, 0, time.UTC)) {
		t.Fatalf("MessageActivity = %v %v %v", tm, ok, err)
	}
	if _, ok := r.FirstMessageActivity(path); !ok {
		t.Fatal("FirstMessageActivity: no turn seen")
	}
	// m1 counts once (5), m2 once (7).
	if n, err := r.OutputTokens(path); err != nil || n != 12 {
		t.Fatalf("OutputTokens = %d, %v; want 12", n, err)
	}
	if aw, err := r.IsAwaitingInput(path); err != nil || aw {
		t.Fatalf("IsAwaitingInput = %v, %v; want false", aw, err)
	}
}

func TestAwaitingInputIsAnUnansweredQuestion(t *testing.T) {
	e := clienttest.Start(t)
	rel := filepath.Join("projects", e.Project, sess+".jsonl")
	e.Write(rel, userLine("2026-01-01T00:00:01Z", "go")+askLine("2026-01-01T00:00:02Z", "m1", "toolu_1"))
	e.Settle()
	r := newReader(e)
	if aw, err := r.IsAwaitingInput(transcriptPath(e)); err != nil || !aw {
		t.Fatalf("unanswered question: awaiting = %v, %v", aw, err)
	}
	e.Append(rel, resultLine("2026-01-01T00:00:03Z", "toolu_1"))
	e.Settle()
	if aw, err := r.IsAwaitingInput(transcriptPath(e)); err != nil || aw {
		t.Fatalf("answered question: awaiting = %v, %v", aw, err)
	}
}

// registerProcess makes the recorder know the session as a Conversation held by
// a live Process (the registry file and the process table entry).
func registerProcess(e *clienttest.Env, pid int, start time.Time) string {
	procStart := start.UTC().Format("Mon Jan _2 15:04:05 2006")
	e.Procs.Add(pid, start, nil)
	e.Write(fmt.Sprintf("sessions/%d.json", pid), fmt.Sprintf(
		`{"pid":%d,"sessionId":%q,"cwd":"/example/work","startedAt":%d,"procStart":%q,"kind":"interactive","status":"idle","updatedAt":%d}`,
		pid, sess, start.UnixMilli(), procStart, start.UnixMilli(),
	))
	return procStart
}

func TestOriginalsPresent(t *testing.T) {
	e := clienttest.Start(t)
	registerProcess(e, 4242, time.Now().Add(-time.Hour))
	e.Write(filepath.Join("projects", e.Project, sess+".jsonl"), userLine("2026-01-01T00:00:01Z", "hi"))
	e.Settle()
	r := newReader(e)
	ok, err := r.OriginalsPresent(transcriptPath(e))
	if err != nil || !ok {
		t.Fatalf("known session: originals = %v, %v", ok, err)
	}
	ok, err = r.OriginalsPresent(e.Path("projects", e.Project, "22222222-2222-4222-8222-222222222222.jsonl"))
	if err != nil || ok {
		t.Fatalf("unknown session: originals = %v, %v; want false, nil", ok, err)
	}
}

func TestWaitMessageReturnsOnFeedMatch(t *testing.T) {
	e := clienttest.Start(t)
	rel := filepath.Join("projects", e.Project, sess+".jsonl")
	e.Write(rel, userLine("2026-01-01T00:00:01Z", "old"))
	e.Settle()
	r := newReader(e)
	ctx := context.Background()
	mark, err := r.Mark(ctx)
	if err != nil || mark == "" {
		t.Fatalf("Mark = %q, %v", mark, err)
	}
	// Nothing written since the mark: the wait ends at the window, not at once on
	// the old message.
	if ok, err := r.WaitMessage(ctx, transcriptPath(e), mark, 300*time.Millisecond); err != nil || ok {
		t.Fatalf("no new message: WaitMessage = %v, %v; want false, nil", ok, err)
	}
	e.Append(rel, assistantText("2026-01-01T00:00:05Z", "m1", "started", 1))
	e.Settle()
	start := time.Now()
	if ok, err := r.WaitMessage(ctx, transcriptPath(e), mark, 5*time.Second); err != nil || !ok {
		t.Fatalf("new message: WaitMessage = %v, %v; want true", ok, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("WaitMessage did not return on the match: %v", time.Since(start))
	}
}

func TestProcessAlive(t *testing.T) {
	e := clienttest.Start(t)
	start := time.Now().Add(-time.Hour).Truncate(time.Second)
	ps := registerProcess(e, 4242, start)
	e.Settle()
	r := newReader(e)
	if ok, err := r.ProcessAlive(4242, ps); err != nil || !ok {
		t.Fatalf("live process: alive = %v, %v", ok, err)
	}
	e.Procs.Exit(4242, time.Now())
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.Settle()
		ok, err := r.ProcessAlive(4242, ps)
		if err == nil && !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("exited process: alive = %v, %v; want false", ok, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestProcessAliveUnknownIsFalse(t *testing.T) {
	e := clienttest.Start(t)
	r := newReader(e)
	ok, err := r.ProcessAlive(999999, "Mon Jan  1 00:00:00 2026")
	if err != nil || ok {
		t.Fatalf("unknown process: alive = %v, %v; want false, nil", ok, err)
	}
}

// TestDaemonOffline: with the recorder stopped every fact read fails with
// "daemon offline", and Ping (the dispatch gate) says so.
func TestDaemonOffline(t *testing.T) {
	e := clienttest.Start(t)
	e.Write(filepath.Join("projects", e.Project, sess+".jsonl"), userLine("2026-01-01T00:00:01Z", "hi"))
	e.Settle()
	r := newReader(e)
	if err := r.Ping(context.Background()); err != nil {
		t.Fatalf("Ping while up: %v", err)
	}
	if _, err := r.LastAssistantText(transcriptPath(e)); err != nil {
		t.Fatalf("read while up: %v", err)
	}
	e.Stop()
	path := transcriptPath(e)
	checks := map[string]func() error{
		"Ping":              func() error { return r.Ping(context.Background()) },
		"LastAssistantText": func() error { _, err := r.LastAssistantText(path); return err },
		"IsAwaitingInput":   func() error { _, err := r.IsAwaitingInput(path); return err },
		"MessageActivity":   func() error { _, _, err := r.MessageActivity(path); return err },
		"OutputTokens":      func() error { _, err := r.OutputTokens(path); return err },
		"OriginalsPresent":  func() error { _, err := r.OriginalsPresent(path); return err },
		"ProcessAlive":      func() error { _, err := r.ProcessAlive(1, "x"); return err },
		"Mark":              func() error { _, err := r.Mark(context.Background()); return err },
		"WaitMessage": func() error {
			_, err := r.WaitMessage(context.Background(), path, "x", time.Second)
			return err
		},
	}
	for name, f := range checks {
		err := f()
		if !errors.Is(err, facts.ErrDaemonOffline) {
			t.Errorf("%s with the recorder down: err = %v; want daemon offline", name, err)
		}
	}
	if _, ok := r.FirstMessageActivity(path); ok {
		t.Error("FirstMessageActivity with the recorder down must report no turn")
	}
	if msg := facts.ErrDaemonOffline.Error(); msg != "daemon offline" {
		t.Errorf("message = %q", msg)
	}
}

func TestSessionID(t *testing.T) {
	if got := facts.SessionID("/x/projects/p/" + sess + ".jsonl"); got != sess {
		t.Fatalf("SessionID = %q", got)
	}
}

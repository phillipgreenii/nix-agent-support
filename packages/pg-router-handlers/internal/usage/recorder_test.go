package usage

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
)

const recSess = "11111111-1111-4111-8111-111111111111"

func asstLine(ts, id, model string, in, cc, cr, out int) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":"a-%s","sessionId":"%s","timestamp":"%s","message":{"id":"%s","role":"assistant","model":"%s","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":%d}}}`+"\n",
		ts, recSess, ts, id, model, in, cc, cr, out)
}

func TestRecorderReader_cumulativeOncePerMessage(t *testing.T) {
	e := clienttest.Start(t)
	e.Write("projects/"+e.Project+"/"+recSess+".jsonl",
		`{"type":"user","uuid":"u1","sessionId":"`+recSess+`","timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":"hi","usage":{"input_tokens":99999,"output_tokens":99999}}}`+"\n"+
			asstLine("2026-01-01T00:00:02Z", "m1", "example-model", 100, 10, 1000, 50)+
			asstLine("2026-01-01T00:00:03Z", "m1", "example-model", 100, 10, 1000, 50)+ // same message id: counted once
			asstLine("2026-01-01T00:00:04Z", "m2", "example-model-2", 200, 0, 2000, 80))
	e.Settle()
	r := NewRecorderReader(client.Options{Socket: e.Socket, ConnectTimeout: time.Second})
	got, err := r.Read(context.Background(), e.Path("projects", e.Project, recSess+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	want := Snapshot{Model: "example-model-2", InputTokens: 300, CacheCreationTokens: 10, CacheReadTokens: 3000, OutputTokens: 130}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if !r.FirstTurnStarted(e.Path("projects", e.Project, recSess+".jsonl")) {
		t.Error("FirstTurnStarted = false with messages present")
	}
	if r.FirstTurnStarted(e.Path("projects", e.Project, "22222222-2222-4222-8222-222222222222.jsonl")) {
		t.Error("FirstTurnStarted = true for an unknown transcript")
	}
}

func TestRecorderReader_unknownTranscriptIsZero(t *testing.T) {
	e := clienttest.Start(t)
	r := NewRecorderReader(client.Options{Socket: e.Socket, ConnectTimeout: time.Second})
	got, err := r.Read(context.Background(), e.Path("projects", e.Project, recSess+".jsonl"))
	if err != nil || got != (Snapshot{}) {
		t.Errorf("got %+v, %v; want zero snapshot", got, err)
	}
}

func TestRecorderReader_daemonOffline(t *testing.T) {
	e := clienttest.Start(t)
	r := NewRecorderReader(client.Options{Socket: e.Socket, ConnectTimeout: time.Second})
	e.Stop()
	if _, err := r.Read(context.Background(), e.Path("projects", e.Project, recSess+".jsonl")); !errors.Is(err, ErrDaemonOffline) {
		t.Errorf("err = %v; want daemon offline", err)
	}
	if r.FirstTurnStarted(e.Path("projects", e.Project, recSess+".jsonl")) {
		t.Error("FirstTurnStarted must be false when the recorder is down")
	}
}

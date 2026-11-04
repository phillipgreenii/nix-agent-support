package recorderfacts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
)

const transcriptFixture = `{"type":"user","uuid":"u1","sessionId":"example-s1","timestamp":"2026-10-29T10:00:00.000Z","cwd":"/example/work","gitBranch":"main","isSidechain":false,"message":{"role":"user","content":"please refactor the example parser"}}
{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"example-s1","timestamp":"2026-10-29T10:00:05.000Z","cwd":"/example/work","gitBranch":"main","isSidechain":false,"message":{"id":"msg_1","role":"assistant","model":"example-model-a","content":[{"type":"text","text":"the registry file was deleted when the process exited"}],"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":40}}}
{"type":"assistant","uuid":"a2","parentUuid":"a1","sessionId":"example-s1","timestamp":"2026-10-29T10:00:06.000Z","cwd":"/example/work","gitBranch":"main","isSidechain":false,"message":{"id":"msg_1","role":"assistant","model":"example-model-a","content":[{"type":"text","text":"second block of the same message"}],"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":40}}}
`

func recorderWithTranscript(t *testing.T) (*clienttest.Env, *Facts) {
	t.Helper()
	e := clienttest.Start(t)
	e.Write("projects/"+e.Project+"/example-s1.jsonl", transcriptFixture)
	e.Settle()
	return e, New(client.Options{Socket: e.Socket})
}

func TestSessionsAreReadFromTheRecorder(t *testing.T) {
	_, f := recorderWithTranscript(t)
	got, err := f.Sessions(context.Background(), time.Time{}, time.Time{})
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	s := got[0]
	if s.SessionID != "example-s1" || s.Cwd != "/example/work" || s.Branch != "main" || s.Model != "example-model-a" {
		t.Errorf("session = %+v", s)
	}
	if s.UserTurns != 1 || s.AssistantTurns != 1 {
		t.Errorf("turns user=%d assistant=%d, want 1 and 1 (de-duplicated by message id)", s.UserTurns, s.AssistantTurns)
	}
	if tk := s.ModelTokens["example-model-a"]; tk != (Tokens{Input: 10, Output: 20, CacheRead: 30, CacheWrite: 40}) {
		t.Errorf("tokens = %+v", tk)
	}
	if !strings.Contains(s.FirstPrompt, "refactor the example parser") {
		t.Errorf("first prompt = %q", s.FirstPrompt)
	}
	if want := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC); !s.StartedAt.Equal(want) {
		t.Errorf("StartedAt = %v", s.StartedAt)
	}
	// A bound after the session's start excludes it.
	none, err := f.Sessions(context.Background(), time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC), time.Time{})
	if err != nil || len(none) != 0 {
		t.Errorf("since after start: %v %v", none, err)
	}
}

func TestSearchIsServedByTheRecorder(t *testing.T) {
	_, f := recorderWithTranscript(t)
	hits, err := f.Search(context.Background(), "registry", "", time.Time{}, time.Time{}, false)
	if err != nil || len(hits) == 0 {
		t.Fatalf("%v %v", hits, err)
	}
	if hits[0].SessionID != "example-s1" || hits[0].Role != "assistant" || !strings.Contains(strings.ToLower(hits[0].Snippet), "registry") {
		t.Errorf("hit = %+v", hits[0])
	}
}

// TestSessionsAndSearchReportDaemonOffline: with the recorder stopped neither
// listing falls back to the transcript file that is still on disk.
func TestSessionsAndSearchReportDaemonOffline(t *testing.T) {
	e, f := recorderWithTranscript(t)
	e.Stop()
	if _, err := f.Sessions(context.Background(), time.Time{}, time.Time{}); !errors.Is(err, session.ErrDaemonOffline) {
		t.Errorf("Sessions err = %v", err)
	}
	if _, err := f.Search(context.Background(), "registry", "", time.Time{}, time.Time{}, false); !errors.Is(err, session.ErrDaemonOffline) {
		t.Errorf("Search err = %v", err)
	}
}

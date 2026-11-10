package recorderfacts

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// TestSessionsWindowKeepsTrueStartAndExcludesEarlierSessions: a bounded window
// narrows the scan, but a session that began before the window is still
// dropped (its first record is not in the window) and one that began inside is
// kept with its true start.
func TestSessionsWindowKeepsTrueStartAndExcludesEarlierSessions(t *testing.T) {
	_, f := recorderWithTranscript(t)
	ctx := context.Background()
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }
	in, err := f.Sessions(ctx, day(29), day(30))
	if err != nil || len(in) != 1 || !in[0].StartedAt.Equal(time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("window around start: %v %v", in, err)
	}
	late := time.Date(2026, 10, 29, 10, 0, 5, 0, time.UTC)
	got, err := f.Sessions(ctx, late, day(30))
	if err != nil || len(got) != 0 {
		t.Errorf("session began before window: %v %v", got, err)
	}
	empty, err := f.Sessions(ctx, day(1), day(2))
	if err != nil || len(empty) != 0 {
		t.Errorf("empty window: %v %v", empty, err)
	}
}

// TestSessionsBeforeOnlyWindow: with no lower bound the inventory lists every
// transcript file and the start-time check applies the upper bound.
func TestSessionsBeforeOnlyWindow(t *testing.T) {
	_, f := recorderWithTranscript(t)
	ctx := context.Background()
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }
	got, err := f.Sessions(ctx, time.Time{}, day(30))
	if err != nil || len(got) != 1 {
		t.Fatalf("before-only window: %v %v", got, err)
	}
	none, err := f.Sessions(ctx, time.Time{}, day(29))
	if err != nil || len(none) != 0 {
		t.Errorf("window before the session: %v %v", none, err)
	}
}

// recordingProxy forwards a unix socket to the real recorder socket and keeps
// every byte the client sent, so a test sees the requests on the wire.
func recordingProxy(t *testing.T, target string) (socket string, sent func() string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "pxy")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket = filepath.Join(dir, "p.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var mu sync.Mutex
	var buf bytes.Buffer
	go func() {
		for {
			cl, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = cl.Close() }()
				up, err := net.Dial("unix", target)
				if err != nil {
					return
				}
				defer func() { _ = up.Close() }()
				go func() { _, _ = io.Copy(cl, up) }()
				_, _ = io.Copy(io.MultiWriter(up, writerFunc(func(b []byte) {
					mu.Lock()
					buf.Write(b)
					mu.Unlock()
				})), cl)
			}()
		}
	}()
	return socket, func() string { mu.Lock(); defer mu.Unlock(); return buf.String() }
}

type writerFunc func([]byte)

func (w writerFunc) Write(b []byte) (int, error) { w(b); return len(b), nil }

// TestSessionsListingIssuesNoSessionGroupAggregateOverRecords: against the real
// recorder, the listing (bounded and open window) reads first and last record
// time per session without any query that groups records by session, which
// scanned every record of every listed session and exceeded the query deadline
// on a real tree.
func TestSessionsListingIssuesNoSessionGroupAggregateOverRecords(t *testing.T) {
	e, _ := recorderWithTranscript(t)
	socket, sent := recordingProxy(t, e.Socket)
	f := New(client.Options{Socket: socket})
	ctx := context.Background()
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }
	for name, w := range map[string][2]time.Time{"bounded": {day(29), day(30)}, "since only": {day(29), {}}, "open": {{}, {}}} {
		got, err := f.Sessions(ctx, w[0], w[1])
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: %v %v", name, got, err)
		}
		if want := time.Date(2026, 10, 29, 10, 0, 6, 0, time.UTC); !got[0].EndedAt.Equal(want) {
			t.Errorf("%s: EndedAt = %v, want the last record %v", name, got[0].EndedAt, want)
		}
	}
	wire := sent()
	if !strings.Contains(wire, `"records"`) {
		t.Fatalf("proxy saw no records query, so the check below is vacuous: %q", wire)
	}
	if strings.Contains(wire, `"group":["session"`) {
		t.Errorf("a query groups records by session: %s", wire)
	}
}

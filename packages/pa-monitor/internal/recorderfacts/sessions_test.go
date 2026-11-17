package recorderfacts

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strconv"
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

	// recordingProxyRewriting is recordingProxy that also passes each chunk the
	// recorder sends back through rewrite (nil leaves it as is).
	dir, err := os.MkdirTemp("", "pxy")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket = filepath.Join(dir, "p.sock")
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", socket)
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
				up, err := (&net.Dialer{}).DialContext(context.Background(), "unix", target)
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

// degradedInventoryProxy is an HTTP proxy to the recorder socket that marks
// every files.inventory answer degraded (the recorder catching up) and keeps
// the path and body of every request.
func degradedInventoryProxy(t *testing.T, target string) (socket string, sent func() string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "pxy")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket = filepath.Join(dir, "p.sock")
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var buf bytes.Buffer
	rp := &httputil.ReverseProxy{
		Director: func(r *http.Request) { r.URL.Scheme, r.URL.Host = "http", "recorder" },
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", target)
		}},
		ModifyResponse: func(resp *http.Response) error {
			if !strings.Contains(resp.Request.URL.Path, "inventory") {
				return nil
			}
			data, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			_ = resp.Body.Close()
			data = bytes.Replace(data, []byte(`"files":[`), []byte(`"degraded":true,"files":[`), 1)
			resp.Body = io.NopCloser(bytes.NewReader(data))
			resp.ContentLength = int64(len(data))
			resp.Header.Set("Content-Length", strconv.Itoa(len(data)))
			return nil
		},
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		buf.WriteString(r.URL.Path + " " + string(body) + "\n")
		mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(body))
		rp.ServeHTTP(w, r)
	}), ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return socket, func() string { mu.Lock(); defer mu.Unlock(); return buf.String() }
}

// TestSessionsListingWhileInventoryDegradedReportsBehindWithoutAggregate: when
// the recorder reports its file inventory degraded (it is catching up), the
// listing fails with client.ErrBehind instead of falling back to a count of
// records grouped by session, which scans every record and exceeds the query
// deadline.
func TestSessionsListingWhileInventoryDegradedReportsBehindWithoutAggregate(t *testing.T) {
	e, _ := recorderWithTranscript(t)
	socket, sent := degradedInventoryProxy(t, e.Socket)
	f := New(client.Options{Socket: socket})
	_, err := f.Sessions(context.Background(), time.Time{}, time.Time{})
	if !errors.Is(err, client.ErrBehind) {
		t.Fatalf("err = %v, want client.ErrBehind", err)
	}
	wire := sent()
	if !strings.Contains(wire, "inventory") {
		t.Fatalf("proxy saw no inventory request, so the check below is vacuous: %q", wire)
	}
	if strings.Contains(wire, `"group":["session"`) || strings.Contains(wire, "records") {
		t.Errorf("a records query was issued while the inventory was degraded: %s", wire)
	}
}

// TestSessionsRecordQueriesSeekPastUntimedRecords: every per-session read of
// records carries a time bound, the one condition the recorder's session,
// source and time index can seek past. Without it a read ordered by time
// walks the millions of untimed block records a long session holds before it
// reaches a turn, and the first-time read of such a session ran 29 s on the
// real store, past the recorder's 25 s deadline.
func TestSessionsRecordQueriesSeekPastUntimedRecords(t *testing.T) {
	e, _ := recorderWithTranscript(t)
	socket, sent := recordingProxy(t, e.Socket)
	f := New(client.Options{Socket: socket})
	got, err := f.Sessions(context.Background(), time.Time{}, time.Time{})
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].UserTurns != 1 || got[0].AssistantTurns != 1 || got[0].Cwd != "/example/work" || got[0].FirstPrompt == "" {
		t.Errorf("the bound changed the answer: %+v", got[0])
	}
	wire := sent()
	queries := strings.Count(wire, `"from":"records"`)
	bounded := strings.Count(wire, `"gt":"1970-01-01T00:00:00Z"`)
	if queries == 0 || bounded != queries {
		t.Errorf("%d records queries, %d with the time bound: %s", queries, bounded, wire)
	}
}

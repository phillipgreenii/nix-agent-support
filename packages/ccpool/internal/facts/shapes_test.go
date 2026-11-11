package facts_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/facts"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
)

// queryLog is one recorded POST /v1/query request.
type queryLog struct {
	Limit int             `json:"limit"`
	Where json.RawMessage `json:"where"`
	Raw   string
}

type recordingProxy struct {
	mu      sync.Mutex
	queries []queryLog
}

func (p *recordingProxy) reset() {
	p.mu.Lock()
	p.queries = nil
	p.mu.Unlock()
}

func (p *recordingProxy) snapshot() []queryLog {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]queryLog(nil), p.queries...)
}

// proxyTo starts a recording reverse proxy in front of the recorder socket and
// returns a Reader that talks through it: every request shape the Reader sends
// is captured while the answers are the real recorder's.
func proxyTo(t *testing.T, e *clienttest.Env) (*facts.Reader, *recordingProxy) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingProxy{}
	rp := &httputil.ReverseProxy{
		Director: func(r *http.Request) { r.URL.Scheme = "http"; r.URL.Host = "recorder" },
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", e.Socket)
		}},
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/query") && r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(b)))
			var q queryLog
			_ = json.Unmarshal(b, &q)
			q.Raw = string(b)
			rec.mu.Lock()
			rec.queries = append(rec.queries, q)
			rec.mu.Unlock()
		}
		rp.ServeHTTP(w, r)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return facts.NewWith(client.Options{Socket: sock, Name: "ccpool-test", ConnectTimeout: time.Second}), rec
}

func eqf(field string, v any) client.Filter {
	b, _ := json.Marshal(v)
	return client.Filter{Field: field, Eq: b}
}

// legacyAwaiting is the pre-bounding IsAwaitingInput: every AskUserQuestion
// tool_use (limit 1000) diffed client-side against every tool_result (limit
// 10000). It is the oracle the bounded form must agree with.
func legacyAwaiting(t *testing.T, e *clienttest.Env, id string) bool {
	t.Helper()
	c := e.Dial(context.Background())
	main := func(more ...client.Filter) *client.Filter {
		no := false
		all := append([]client.Filter{eqf("session", id), {Field: "agent_id", Exists: &no}}, more...)
		return &client.Filter{All: all}
	}
	asked, err := c.Query(context.Background(), client.QueryRequest{
		Where: main(eqf("type", "tool_use"), eqf("name", "AskUserQuestion")), Select: []string{"tool_use_id", "time"}, Limit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	answered, err := c.Query(context.Background(), client.QueryRequest{
		Where: main(eqf("type", "tool_result")), Select: []string{"tool_use_id"}, Limit: 10000,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := map[string]bool{}
	for _, row := range answered.Rows {
		if s, ok := row[0].(string); ok {
			done[s] = true
		}
	}
	for _, row := range asked.Rows {
		if s, ok := row[0].(string); ok && s != "" && !done[s] {
			return true
		}
	}
	return false
}

func TestAwaitingInputAgreesWithTheLegacyTwoQueryForm(t *testing.T) {
	ts := func(n int) string { return fmt.Sprintf("2026-01-01T00:%02d:%02dZ", n/60, n%60) }
	cases := []struct {
		name  string
		lines func() string
		want  bool
	}{
		{"none", func() string { return userLine(ts(1), "hi") + assistantText(ts(2), "m1", "a", 1) }, false},
		{"one unanswered", func() string { return userLine(ts(1), "hi") + askLine(ts(2), "m1", "t1") }, true},
		{"one answered", func() string {
			return userLine(ts(1), "hi") + askLine(ts(2), "m1", "t1") + resultLine(ts(3), "t1")
		}, false},
		{"several all answered", func() string {
			return askLine(ts(1), "m1", "t1") + resultLine(ts(2), "t1") + askLine(ts(3), "m2", "t2") + resultLine(ts(4), "t2") +
				askLine(ts(5), "m3", "t3") + resultLine(ts(6), "t3")
		}, false},
		{"several newest unanswered", func() string {
			return askLine(ts(1), "m1", "t1") + resultLine(ts(2), "t1") + askLine(ts(3), "m2", "t2") + resultLine(ts(4), "t2") +
				askLine(ts(5), "m3", "t3")
		}, true},
		{"several middle unanswered", func() string {
			return askLine(ts(1), "m1", "t1") + resultLine(ts(2), "t1") + askLine(ts(3), "m2", "t2") +
				askLine(ts(5), "m3", "t3") + resultLine(ts(6), "t3")
		}, true},
		{"oldest unanswered", func() string {
			return askLine(ts(1), "m1", "t1") + askLine(ts(3), "m2", "t2") + resultLine(ts(4), "t2")
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := clienttest.Start(t)
			e.Write(filepath.Join("projects", e.Project, sess+".jsonl"), tc.lines())
			e.Settle()
			got, err := newReader(e).IsAwaitingInput(transcriptPath(e))
			if err != nil {
				t.Fatal(err)
			}
			byQuery, err := newReader(e).IsAwaitingInputByQuery(transcriptPath(e))
			if err != nil {
				t.Fatal(err)
			}
			if legacy := legacyAwaiting(t, e, sess); got != legacy || got != tc.want || byQuery != got {
				t.Fatalf("IsAwaitingInput = %v, by query = %v, legacy = %v, want %v", got, byQuery, legacy, tc.want)
			}
		})
	}
}

// smallLimit is the largest limit any ccpool state read other than the one
// bounded tool_result page may send; resultsLimit is that page's cap.
const (
	smallLimit   = 50
	resultsLimit = 10000
)

func TestStateReadsOnABigSessionAreBoundedAndNeverLookUpPerId(t *testing.T) {
	e := clienttest.Start(t)
	var b strings.Builder
	b.WriteString(userLine("2026-01-01T00:00:01Z", "go"))
	b.WriteString(askLine("2026-01-01T00:00:02Z", "m0", "q-answered"))
	b.WriteString(resultLine("2026-01-01T00:00:03Z", "q-answered"))
	for i := 0; i < 10000; i++ {
		b.WriteString(resultLine("2026-01-01T00:01:00Z", fmt.Sprintf("bulk-%d", i)))
	}
	b.WriteString(askLine("2026-01-01T00:02:00Z", "m1", "q-open"))
	b.WriteString(assistantText("2026-01-01T00:03:00Z", "m2", "the reply", 4))
	e.Write(filepath.Join("projects", e.Project, sess+".jsonl"), b.String())
	e.Settle()
	r, rec := proxyTo(t, e)
	path := transcriptPath(e)

	if aw, err := r.IsAwaitingInputByQuery(path); err != nil || !aw {
		t.Fatalf("IsAwaitingInput = %v, %v; want true", aw, err)
	}
	if _, ok, err := r.MessageActivity(path); err != nil || !ok {
		t.Fatalf("MessageActivity: %v %v", ok, err)
	}
	if txt, err := r.LastAssistantText(path); err != nil || txt != "the reply" {
		t.Fatalf("LastAssistantText = %q, %v", txt, err)
	}
	results := 0
	for _, q := range rec.snapshot() {
		if strings.Contains(q.Raw, `"tool_result"`) {
			results++
			// ONE page, floored in time, on the indexed source, projecting only the
			// id; never a per-id lookup.
			if q.Limit != resultsLimit || !strings.Contains(q.Raw, `"gte"`) || !strings.Contains(q.Raw, `"claude-transcript"`) {
				t.Fatalf("tool_result query shape: %s", q.Raw)
			}
			if strings.Count(q.Raw, `"field":"tool_use_id"`) != 0 || !strings.Contains(q.Raw, `"select":["tool_use_id"]`) {
				t.Fatalf("tool_result query filters by id or projects more than the id: %s", q.Raw)
			}
			continue
		}
		if q.Limit < 1 || q.Limit > smallLimit {
			t.Fatalf("query with limit %d (want 1..%d): %s", q.Limit, smallLimit, q.Raw)
		}
	}
	if results != 1 {
		t.Fatalf("%d tool_result queries, want exactly 1", results)
	}
}

func TestAwaitingInputIsAtMostTwoQueriesHoweverManyQuestions(t *testing.T) {
	e := clienttest.Start(t)
	var b strings.Builder
	for i := 0; i < 5; i++ {
		ts := fmt.Sprintf("2026-01-01T00:00:%02dZ", i*2)
		b.WriteString(askLine(ts, fmt.Sprintf("m%d", i), fmt.Sprintf("t%d", i)))
		if i != 2 {
			b.WriteString(resultLine(fmt.Sprintf("2026-01-01T00:00:%02dZ", i*2+1), fmt.Sprintf("t%d", i)))
		}
	}
	e.Write(filepath.Join("projects", e.Project, sess+".jsonl"), b.String())
	e.Settle()
	r, rec := proxyTo(t, e)
	if aw, err := r.IsAwaitingInputByQuery(transcriptPath(e)); err != nil || !aw {
		t.Fatalf("awaiting = %v, %v", aw, err)
	}
	if n := len(rec.snapshot()); n != 2 {
		t.Fatalf("%d queries, want 2 (candidates, one tool_result page): %v", n, rec.snapshot())
	}
}

func TestAwaitingInputFloorsTheResultPageAtTheOldestCandidate(t *testing.T) {
	e := clienttest.Start(t)
	// an answer to a question long before the candidates must not be read
	e.Write(filepath.Join("projects", e.Project, sess+".jsonl"),
		resultLine("2026-01-01T00:00:01Z", "ancient")+askLine("2026-01-01T00:00:05Z", "m1", "t1"))
	e.Settle()
	r, rec := proxyTo(t, e)
	if aw, err := r.IsAwaitingInputByQuery(transcriptPath(e)); err != nil || !aw {
		t.Fatalf("awaiting = %v, %v", aw, err)
	}
	qs := rec.snapshot()
	if len(qs) != 2 || !strings.Contains(qs[1].Raw, `"gte":"2026-01-01T00:00:05Z"`) {
		t.Fatalf("result page not floored at the oldest candidate: %+v", qs)
	}
}

func TestAwaitingInputAsksForTheNewestQuestionsFirstAndCapsThem(t *testing.T) {
	e := clienttest.Start(t)
	e.Write(filepath.Join("projects", e.Project, sess+".jsonl"), userLine("2026-01-01T00:00:01Z", "hi"))
	e.Settle()
	r, rec := proxyTo(t, e)
	if aw, err := r.IsAwaitingInputByQuery(transcriptPath(e)); err != nil || aw {
		t.Fatalf("awaiting = %v, %v", aw, err)
	}
	qs := rec.snapshot()
	if len(qs) != 1 || qs[0].Limit != smallLimit || !strings.Contains(qs[0].Raw, `"dir":"desc"`) {
		t.Fatalf("candidate query = %+v; want one query, limit %d, newest first", qs, smallLimit)
	}
}

func TestLastAssistantTextSinceFloorsTheReadAndTheAnswer(t *testing.T) {
	e := clienttest.Start(t)
	e.Write(filepath.Join("projects", e.Project, sess+".jsonl"),
		userLine("2026-01-01T00:00:01Z", "hi")+assistantText("2026-01-01T00:00:02Z", "m1", "old reply", 1))
	e.Settle()
	r, rec := proxyTo(t, e)
	path := transcriptPath(e)

	floor := time.Date(2026, 1, 1, 0, 0, 3, 0, time.UTC)
	if txt, err := r.LastAssistantTextSince(path, floor); err != nil || txt != "" {
		t.Fatalf("a reply older than the floor was returned: %q, %v", txt, err)
	}
	qs := rec.snapshot()
	if len(qs) != 1 || qs[0].Limit != 1 || !strings.Contains(qs[0].Raw, `"gte":"2026-01-01T00:00:03Z"`) {
		t.Fatalf("floored query shape = %+v; want limit 1 with a time gte floor", qs)
	}

	e.Append(filepath.Join("projects", e.Project, sess+".jsonl"), assistantText("2026-01-01T00:00:04Z", "m2", "new reply", 1))
	e.Settle()
	if txt, err := r.LastAssistantTextSince(path, floor); err != nil || txt != "new reply" {
		t.Fatalf("LastAssistantTextSince = %q, %v; want the reply after the floor", txt, err)
	}
	// Zero floor: same answer as LastAssistantText, and no time bound is sent.
	rec.reset()
	if txt, err := r.LastAssistantText(path); err != nil || txt != "new reply" {
		t.Fatalf("LastAssistantText = %q, %v", txt, err)
	}
	if q := rec.snapshot(); len(q) != 1 || strings.Contains(q[0].Raw, `"gte"`) {
		t.Fatalf("unfloored query carried a floor: %+v", q)
	}
}

// IsAwaitingInput reads the recorder's derived ask tables through
// sessions.facts: it sends no query at all, however big the session is.
func TestAwaitingInputSendsNoQueryToTheRecorder(t *testing.T) {
	e := clienttest.Start(t)
	var b strings.Builder
	b.WriteString(askLine("2026-01-01T00:00:02Z", "m0", "q-answered"))
	b.WriteString(resultLine("2026-01-01T00:00:03Z", "q-answered"))
	for i := 0; i < 2000; i++ {
		b.WriteString(resultLine("2026-01-01T00:01:00Z", fmt.Sprintf("bulk-%d", i)))
	}
	b.WriteString(askLine("2026-01-01T00:02:00Z", "m1", "q-open"))
	e.Write(filepath.Join("projects", e.Project, sess+".jsonl"), b.String())
	e.Settle()
	r, rec := proxyTo(t, e)
	if aw, err := r.IsAwaitingInput(transcriptPath(e)); err != nil || !aw {
		t.Fatalf("awaiting = %v, %v; want true", aw, err)
	}
	if qs := rec.snapshot(); len(qs) != 0 {
		t.Fatalf("%d /query requests, want 0: %v", len(qs), qs)
	}
}

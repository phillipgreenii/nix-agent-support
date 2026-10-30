package client_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// The wire-contract suite: a real daemon (store, copier, parser, feed, listener)
// in process, one case per operation and one per error (the conversation and
// process operations are in lifecycle_test.go), over its Unix socket. The cases for operations that need
// no daemon state (hello, errors) go through raw HTTP so the framing is what is
// tested, not this package's reading of it.

const (
	cL1 = `{"type":"user","uuid":"a1","sessionId":"s1","timestamp":"2026-10-29T20:00:00Z","cwd":"/example/work/sub","message":{"role":"user"}}` + "\n"
	cL2 = `{"type":"assistant","uuid":"a2","sessionId":"s1","timestamp":"2026-10-29T20:00:01Z","cwd":"/example/work","message":{"id":"m1","model":"example-model-a","role":"assistant","usage":{"output_tokens":10}}}` + "\n"
	cL3 = `{"type":"assistant","uuid":"a3","sessionId":"s1","timestamp":"2026-10-29T20:00:02Z","message":{"id":"m2","model":"example-model-b","role":"assistant","usage":{"output_tokens":5}}}` + "\n"
	cL4 = `{"type":"summary","uuid":"a4","sessionId":"s1","timestamp":"2026-10-29T20:00:03Z","summary":"registry file deleted early"}` + "\n"
	rel = "projects/example-proj/s1.jsonl"
)

func ctx() context.Context { return context.Background() }

func start(t *testing.T, mutate func(*config.Config)) (*clienttest.Env, *client.Client) {
	t.Helper()
	e := clienttest.Start(t, clienttest.Options{Mutate: mutate})
	e.Write(rel, cL1+cL2+cL3+cL4)
	e.Settle()
	return e, e.Dial(ctx())
}

// httpDo speaks the framing directly.
func httpDo(t *testing.T, sock, method, path, body string) (int, map[string]any) {
	t.Helper()
	hc := &http.Client{Transport: &http.Transport{DialContext: func(c context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(c, "unix", sock)
	}, DisableKeepAlives: true}, Timeout: 15 * time.Second}
	req, _ := http.NewRequest(method, "http://recorder"+path, bytes.NewReader([]byte(body)))
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s %s: not JSON: %q", method, path, b)
	}
	return resp.StatusCode, out
}

func TestHelloAnswersTheFourFactsOverTheFraming(t *testing.T) {
	e, _ := start(t, nil)
	code, h := httpDo(t, e.Socket, "GET", "/v1/hello", "")
	if code != 200 || h["protocol_version"] != float64(wire.ProtocolVersion) || h["schema_version"] == nil || h["epoch"] == nil {
		t.Fatalf("hello %d %v", code, h)
	}
	caps, _ := h["capabilities"].([]any)
	if fmt.Sprint(caps) != "[conversation.list conversation.show coverage feed hello process.list process.show query raw search snapshot status]" {
		t.Fatalf("capabilities %v", caps)
	}
	if _, ok := h["caught_up"]; !ok {
		t.Fatalf("hello lacks caught_up: %v", h)
	}
}

func TestEveryErrorHasItsStatusAndName(t *testing.T) {
	e, _ := start(t, func(c *config.Config) { c.WaitMaxWaiter = 2 })
	post := func(op, body string) (int, string) {
		code, m := httpDo(t, e.Socket, "POST", "/v1/"+op, body)
		name, _ := m["error"].(string)
		return code, name
	}
	for name, tc := range map[string]struct {
		op, body, want string
		code           int
	}{
		"bad_request":    {"query", `{"where":{"field":"nope","exists":true}}`, wire.ErrBadRequest, 400},
		"bad json":       {"query", `{`, wire.ErrBadRequest, 400},
		"wait over cap":  {"feed", `{"wait_ms":60001}`, wire.ErrBadRequest, 400},
		"not_found op":   {"nope", `{}`, wire.ErrNotFound, 404},
		"not_found raw":  {"raw", `{"record":{"file":"f_999","gen":1,"offset":0,"item":-1}}`, wire.ErrNotFound, 404},
		"cursor_expired": {"feed", `{"after":"99:0"}`, wire.ErrCursorExpired, 410},
		"cursor beyond":  {"feed", `{"after":"1:99999999"}`, wire.ErrCursorExpired, 410},
	} {
		if code, got := post(tc.op, tc.body); code != tc.code || got != tc.want {
			t.Errorf("%s: %d %q, want %d %q", name, code, got, tc.code, tc.want)
		}
	}
	// busy: the third waiter of a daemon that admits two.
	c := e.Dial(ctx())
	head := mustFeedHead(t, c)
	var wg sync.WaitGroup
	cctx, cancel := context.WithCancel(context.Background())
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.FeedPage(cctx, nil, head, nil, 0, 60000)
		}()
	}
	waitUntil(t, func() bool { return gauge(e, "feed_waiters") == 2 })
	_, err := c.FeedPage(ctx(), nil, head, nil, 0, 60000)
	if !errorsIs(err, client.ErrBusy) {
		t.Fatalf("third waiter: %v", err)
	}
	code, name := post("feed", fmt.Sprintf(`{"after":%q,"wait_ms":60000}`, head))
	if code != 429 || name != wire.ErrBusy {
		t.Fatalf("busy over HTTP: %d %q", code, name)
	}
	cancel()
	wg.Wait()
}

func gauge(e *clienttest.Env, name string) float64 {
	v, _ := e.Daemon.Obs().Reg.Gauge(name, nil)
	return v
}

func mustFeedHead(t *testing.T, c *client.Client) string {
	t.Helper()
	a, err := c.FeedPage(ctx(), nil, "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return a.NextCursor
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestQueryAndSearchEveryFilterOperator(t *testing.T) {
	_, c := start(t, nil)
	count := func(where string) int {
		var w client.Filter
		if err := json.Unmarshal([]byte(where), &w); err != nil {
			t.Fatal(err)
		}
		a, err := c.Query(ctx(), client.QueryRequest{Where: &w, Aggregate: []client.Agg{{Fn: "count"}}})
		if err != nil {
			t.Fatalf("%s: %v", where, err)
		}
		return int(a.Rows[0][0].(float64))
	}
	for name, tc := range map[string]struct {
		where string
		want  int
	}{
		"eq":       {`{"field":"type","eq":"assistant"}`, 2},
		"in":       {`{"field":"type","in":["user","summary"]}`, 2},
		"range":    {`{"field":"time","range":{"from":"2026-10-29T20:00:01Z","to":"2026-10-29T20:00:03Z"}}`, 2},
		"under":    {`{"field":"cwd","under":"/example/work"}`, 2},
		"exists":   {`{"field":"cwd","exists":true}`, 2},
		"absent":   {`{"field":"cwd","exists":false}`, 2},
		"contains": {`{"field":"cwd","contains":"work/sub"}`, 1},
		"match":    {`{"field":"summary","match":"registry deleted"}`, 1},
		"all":      {`{"all":[{"field":"type","eq":"assistant"},{"field":"cwd","exists":true}]}`, 1},
		"any":      {`{"any":[{"field":"type","eq":"user"},{"field":"type","eq":"summary"}]}`, 2},
		"not":      {`{"not":{"field":"type","eq":"assistant"}}`, 2},
	} {
		if got := count(tc.where); got != tc.want {
			t.Errorf("%s: %d, want %d", name, got, tc.want)
		}
	}
	a, err := c.Query(ctx(), client.QueryRequest{Limit: 3, Select: []string{"type", "session"}})
	if err != nil || len(a.Rows) != 3 || !a.More || a.AsOf == "" {
		t.Fatalf("limit and more: %+v %v", a, err)
	}
	s, err := c.Search(ctx(), client.SearchRequest{Q: "registry deleted", Mode: "word"})
	if err != nil || len(s.Hits) != 1 {
		t.Fatalf("search: %+v %v", s, err)
	}
}

func TestSnapshotThenATailDeliversTheNewRecordOnce(t *testing.T) {
	e, c := start(t, nil)
	snap, err := c.Snapshot(ctx(), client.SnapshotRequest{Project: []string{"type"}})
	if err != nil || len(snap.Entries) != 4 || snap.More || snap.Cursor == "" {
		t.Fatalf("snapshot %+v %v", snap, err)
	}
	e.Append(rel, cL3)
	e.Settle()
	tail, err := c.FeedPage(ctx(), nil, snap.Cursor, []string{"type"}, 0, 0)
	if err != nil || len(tail.Entries) != 1 || tail.Entries[0].Kind != "record" {
		t.Fatalf("tail %+v %v", tail, err)
	}
	if again, _ := c.FeedPage(ctx(), nil, tail.NextCursor, nil, 0, 0); len(again.Entries) != 0 || again.TimedOut {
		t.Fatalf("nothing after the tail: %+v", again)
	}
}

func TestRawStatusAndCoverageAnswer(t *testing.T) {
	e, c := start(t, nil)
	a, err := c.Query(ctx(), client.QueryRequest{Where: &client.Filter{Field: "type", Eq: json.RawMessage(`"summary"`)}})
	if err != nil || len(a.Records) != 1 {
		t.Fatalf("locate %+v %v", a, err)
	}
	raw, err := c.Raw(ctx(), client.RawRequest{Record: &a.Records[0]})
	if err != nil || raw.Data != cL4 {
		t.Fatalf("raw %+v %v", raw, err)
	}
	for _, op := range []string{"status", "coverage"} {
		if code, m := httpDo(t, e.Socket, "POST", "/v1/"+op, `{}`); code != 200 || m["caught_up"] != true {
			t.Errorf("%s: %d %v", op, code, m)
		}
	}
}

func TestADaemonRestartClosesTheWaitAndTheIteratorResumesWithTheSameCursor(t *testing.T) {
	e, _ := start(t, nil)
	var sleeps []time.Duration
	var mu sync.Mutex
	c, err := client.Dial(ctx(), client.Options{Socket: e.Socket, Sleep: func(_ context.Context, d time.Duration) error {
		mu.Lock()
		sleeps = append(sleeps, d)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	head := mustFeedHead(t, c)
	it := c.Feed(ctx(), &client.Filter{Field: "type", Eq: json.RawMessage(`"assistant"`)}, head, client.FeedOptions{WaitMS: 30000})
	got := make(chan client.Entry, 1)
	go func() {
		en, err := it.Next(ctx())
		if err != nil {
			t.Error(err)
		}
		got <- en
	}()
	waitUntil(t, func() bool { return gauge(e, "feed_waiters") == 1 })
	e.Restart() // closes the waiting connection
	e.Append(rel, cL2)
	e.Settle()
	select {
	case en := <-got:
		if en.Kind != "record" {
			t.Fatalf("entry %+v", en)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the iterator did not resume after the restart")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sleeps) == 0 || sleeps[0] != time.Second {
		t.Fatalf("it reconnected without the 1 s back off: %v", sleeps)
	}
}

func TestARebuildBumpsTheEpochSoAnOldCursorExpiresAndASnapshotResyncs(t *testing.T) {
	e, c := start(t, nil)
	old := mustFeedHead(t, c)
	e.Stop()
	for _, n := range []string{"index.db", "index.db-wal", "index.db-shm"} {
		_ = os.Remove(filepath.Join(e.StateDir, n))
	}
	e.Restart()
	if err := e.Daemon.Parser().ParseAll(); err != nil {
		t.Fatal(err)
	}
	c2 := e.Dial(ctx())
	h, _ := c2.Hello(ctx())
	if h.Epoch < 2 {
		t.Fatalf("epoch %d after a rebuild", h.Epoch)
	}
	_, err := c2.FeedPage(ctx(), nil, old, nil, 0, 0)
	var ce *client.Error
	if !errorsIs(err, client.ErrCursorExpired) || !errorsAs(err, &ce) || ce.Cursor == "" {
		t.Fatalf("old epoch: %v", err)
	}
	snap, err := c2.Snapshot(ctx(), client.SnapshotRequest{})
	if err != nil || len(snap.Entries) != 4 {
		t.Fatalf("resync snapshot %+v %v", snap, err)
	}
	if a, err := c2.FeedPage(ctx(), nil, snap.Cursor, nil, 0, 0); err != nil || len(a.Entries) != 0 {
		t.Fatalf("tail after resync %+v %v", a, err)
	}
	if v, _ := e.Daemon.Obs().Reg.Counter("index_rebuilds_total", nil); v != 1 {
		t.Fatalf("rebuild counter %v", v)
	}
}

func TestAnEmptyMatchStillAdvancesNextCursorOverTheSocket(t *testing.T) {
	e, c := start(t, nil)
	f := &client.Filter{Field: "type", Eq: json.RawMessage(`"registry"`)}
	head := mustFeedHead(t, c)
	e.Append(rel, cL1)
	e.Settle()
	a, err := c.FeedPage(ctx(), f, head, nil, 0, 0)
	if err != nil || len(a.Entries) != 0 || a.NextCursor == head || strings.Count(a.NextCursor, ":") != 1 {
		t.Fatalf("%+v %v", a, err)
	}
}

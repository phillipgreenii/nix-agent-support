package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/server"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// cancelBodies maps every operation whose reads run under the request context
// to a valid body. cancelExempt lists the operations that do not, each with the
// reason; the test fails for an operation in neither, so a new operation has to
// be classified.
var cancelBodies = map[string]string{
	"coverage":          `{}`,
	"query":             `{}`,
	"files.inventory":   `{}`,
	"search":            `{"q":"line"}`,
	"feed":              `{}`,
	"snapshot":          `{}`,
	"raw":               `{"file":"f_1","length":1}`,
	"conversation.show": `{"session_id":"example-session"}`,
	"conversation.list": `{}`,
	"process.show":      `{"pid":1}`,
	"process.list":      `{}`,
	"sessions.facts":    `{"session_ids":["example-session"],"fields":["awaiting_input"]}`,
	"held":              `{}`,
	"where":             `{"session":"example-session"}`,
}

var cancelExempt = map[string]string{
	"hello":   "answers from the daemon's own state, no store read",
	"status":  "answers from the snapshot the status loop keeps, issues no query",
	"remove":  "mutates the archive and the index; cancelling half way would leave partial state",
	"extract": "copies archive files and commits stage rows; cancelling half way would leave partial state",
	"sweep":   "starts a background sweep and answers from daemon state, no store read",
	"doctor":  "verifies files on disk and writes its report; a long file walk, not a pool read",
}

func post(d *Daemon, ctx context.Context, op, body string) (int, string) {
	req := httptest.NewRequest(http.MethodPost, "/v1/"+op, strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	d.srv.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// Every registered operation, run with a request context that is already
// cancelled, must not come back as the answer it gives with a live context: the
// context has to reach the store. Real SQLite, no sleeping.
func TestEveryOperationStopsOnACancelledRequestContext(t *testing.T) {
	e := newEnv(t, false)
	e.transcript("a.jsonl", "line\n", time.Now().Add(-time.Hour))
	d := e.open(t)
	defer func() { _ = d.Close() }()
	d.srv = server.New(d.obs, d.cfg.Clients, d.hello)
	d.registerOps()
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}

	for _, op := range d.srv.Operations() {
		_, cancels := cancelBodies[op]
		_, exempt := cancelExempt[op]
		if cancels == exempt {
			t.Errorf("operation %q must be in exactly one of cancelBodies and cancelExempt", op)
		}
	}

	dead, cancel := context.WithCancel(context.Background())
	cancel()
	for op, body := range cancelBodies {
		t.Run(op, func(t *testing.T) {
			liveCode, liveBody := post(d, context.Background(), op, body)
			code, got := post(d, dead, op, body)
			if code == liveCode && got == liveBody {
				t.Fatalf("%s answered the same (%d %s) under a cancelled context: the context does not reach the store", op, code, got)
			}
			if code == http.StatusOK {
				t.Fatalf("%s answered 200 under a cancelled context: %s", op, got)
			}
			// The server-level mapping: a cancelled request is the cancelled class,
			// never query_timeout or internal, and counts as client_gone.
			if code != 499 || !strings.Contains(got, wire.ErrCancelled) {
				t.Fatalf("%s under a cancelled context answered %d %s, want 499 %s", op, code, got, wire.ErrCancelled)
			}
			if v, _ := d.obs.Reg.Counter(obs.MRequestCancelled, map[string]string{"op": op, "reason": "client_gone"}); v < 1 {
				t.Fatalf("%s: request_cancelled_total client_gone %v", op, v)
			}
			if v, _ := d.obs.Reg.Counter(obs.MQueryTimeouts, map[string]string{"op": op}); v != 0 {
				t.Fatalf("%s: query_timeouts_total %v for a cancel", op, v)
			}
		})
	}
}

// spinOp registers an operation that runs a statement which never ends on its
// own (a recursive count with no bound), so only the request context or the
// store's read deadline can stop it: real SQLite, no sleeping in the test body.
func spinOp(d *Daemon) {
	d.srv.Register("test.spin", server.KindQuery, func(ctx context.Context, _ []byte) (any, *wire.Error) {
		err := d.st.ReadTxCtx(ctx, func(r *store.Reader) error {
			var n int64
			return r.Q().QueryRow(`WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c) SELECT count(*) FROM c`).Scan(&n)
		})
		if err != nil {
			return nil, wire.FromErr(err)
		}
		return map[string]bool{"ok": true}, nil
	})
}

func postHeader(d *Daemon, op, deadlineMs string) (int, string) {
	req := httptest.NewRequest(http.MethodPost, "/v1/"+op, strings.NewReader("{}"))
	if deadlineMs != "" {
		req.Header.Set(wire.DeadlineHeader, deadlineMs)
	}
	rec := httptest.NewRecorder()
	d.srv.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// A header at the clamp floor ends a statement that never returns with
// query_timeout, well before query.timeout, and counts as a deadline cancel.
// The header "1" is clamped up to wire.MinDeadline (1 s), a production constant
// and the point of the test, so this test waits 1 s by design (measured 1.04 s
// plain, 1.13 s -race) and cannot be shortened without changing that constant.
func TestDeadlineHeaderAtTheFloorTimesOutARealStatement(t *testing.T) {
	e := newEnv(t, false)
	d := e.open(t)
	defer func() { _ = d.Close() }()
	d.srv = server.New(d.obs, d.cfg.Clients, d.hello)
	d.srv.SetQueryTimeout(d.cfg.QueryTimeout)
	spinOp(d)

	code, body := postHeader(d, "test.spin", "1")
	if code != http.StatusGatewayTimeout || !strings.Contains(body, wire.ErrQueryTimeout) {
		t.Fatalf("answered %d %s, want query_timeout", code, body)
	}
	if v, _ := d.obs.Reg.Counter(obs.MRequestCancelled, map[string]string{"op": "test.spin", "reason": "deadline"}); v != 1 {
		t.Fatalf("deadline cancels %v", v)
	}
	if v, _ := d.obs.Reg.Counter(obs.MQueryTimeouts, map[string]string{"op": "test.spin"}); v != 1 {
		t.Fatalf("query_timeouts_total %v", v)
	}
	// The connection went back to the pool clean: a read after it succeeds.
	if err := d.st.ReadTx(func(r *store.Reader) error {
		var n int
		return r.Q().QueryRow(`SELECT 1`).Scan(&n)
	}); err != nil {
		t.Fatalf("read after the timeout: %v", err)
	}
}

// noHeaderQueryTimeout is the query.timeout of TestWithoutTheHeaderQueryTimeoutApplies.
//
//   - It has to be an ordinary positive query.timeout: the config has no floor on
//     it (only wire.MinDeadline, 1 s, floors a client's header, and that is not
//     in play here because a header not shorter than query.timeout is ignored).
//     So 250 ms is as legitimate a query.timeout as 1 s.
//   - What the assertions need: spin never ends on its own, so a 504 query_timeout
//     can only come from query.timeout, whatever its size, and the header cases
//     ("" and "60000") must both be NOT shorter than it: 60000 ms is 240x 250 ms.
//     A deadline that fires before the statement starts is expected to still
//     yield query_timeout (a context deadline maps to the wire query_timeout
//     error in wire.go), so a slow start under load should not flip the result;
//     the test passed 3 times under -race with this value.
//   - Smaller than this buys little (each of the 2 requests waits one timeout);
//     larger is unnecessary because nothing in the test depends on the size. The
//     floor test (TestDeadlineHeaderAtTheFloor...) keeps the daemon default
//     query.timeout (25 s) and MUST NOT use this value, as it needs
//     query.timeout above wire.MinDeadline.
//   - Measured, niced -p 4: 2.05 s plain / 2.11 s -race at 1 s (2 x 1 s); at
//     250 ms 0.53 s plain / 0.69 s -race (full-module after run).
const noHeaderQueryTimeout = 250 * time.Millisecond

// Without the header the daemon's own query.timeout ends the request.
func TestWithoutTheHeaderQueryTimeoutApplies(t *testing.T) {
	e := newEnv(t, false)
	e.cfg.QueryTimeout = noHeaderQueryTimeout
	d := e.open(t)
	defer func() { _ = d.Close() }()
	d.srv = server.New(d.obs, d.cfg.Clients, d.hello)
	d.srv.SetQueryTimeout(d.cfg.QueryTimeout)
	spinOp(d)

	// A header not shorter than query.timeout is ignored too.
	for _, hdr := range []string{"", "60000"} {
		code, body := postHeader(d, "test.spin", hdr)
		if code != http.StatusGatewayTimeout || !strings.Contains(body, wire.ErrQueryTimeout) {
			t.Fatalf("header %q answered %d %s, want query_timeout", hdr, code, body)
		}
	}
	if v, _ := d.obs.Reg.Counter(obs.MQueryTimeouts, map[string]string{"op": "test.spin"}); v != 2 {
		t.Fatalf("query_timeouts_total %v", v)
	}
}

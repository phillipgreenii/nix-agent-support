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
	"search":            `{"q":"line"}`,
	"feed":              `{}`,
	"snapshot":          `{}`,
	"raw":               `{"file":"f_1","length":1}`,
	"conversation.show": `{"session_id":"example-session"}`,
	"conversation.list": `{}`,
	"process.show":      `{"pid":1}`,
	"process.list":      `{}`,
	"held":              `{}`,
	"where":             `{"session":"example-session"}`,
}

var cancelExempt = map[string]string{
	"hello":   "answers from the daemon's own state, no store read",
	"status":  "answers from the snapshot the status loop keeps, issues no query",
	"remove":  "mutates the archive and the index; cancelling half way would leave partial state",
	"extract": "copies archive files and commits stage rows; cancelling half way would leave partial state",
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

// Without the header the daemon's own query.timeout ends the request.
func TestWithoutTheHeaderQueryTimeoutApplies(t *testing.T) {
	e := newEnv(t, false)
	e.cfg.QueryTimeout = time.Second
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

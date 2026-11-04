package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/server"
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
		})
	}
}

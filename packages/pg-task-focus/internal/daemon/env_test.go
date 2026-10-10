package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/contract"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/daemon"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/notify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/testutil"
)

// The daemon tests run the real daemon on an ephemeral loopback port, over a
// temporary data directory, with a fake clock and a fake player, and talk to
// it over HTTP exactly as a client does. Every response is validated against
// api/openapi.yaml.

var newYork = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return loc
}()

// local is the instant of a wall-clock time in New York on 2026-10-07.
func local(h, m int) time.Time { return time.Date(2026, time.October, 7, h, m, 0, 0, newYork).UTC() }

type env struct {
	t       *testing.T
	d       *daemon.Daemon
	clock   *clock.Fake
	player  *notify.Fake
	dir     string
	cfgDir  string
	cfg     string
	port    int
	log     *lockedBuf
	spec    *contract.Spec
	client  *http.Client
	ids     uint32
	otlp    slog.Handler
	tracing trace.TracerProvider
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// options tweaks a test daemon.
type options struct {
	editConfig func(map[string]any)
	fs         store.FS
	at         time.Time
	dir        string
	otlp       slog.Handler
	tracing    trace.TracerProvider
}

// newEnv starts a daemon and waits for it to be ready.
func newEnv(t *testing.T, o options) *env {
	t.Helper()
	e := &env{t: t, log: &lockedBuf{}, spec: contract.Load(t), client: &http.Client{Timeout: 10 * time.Second}}
	if o.at.IsZero() {
		o.at = local(8, 50)
	}
	e.clock = clock.NewFake(o.at)
	e.player = &notify.Fake{}
	e.dir = o.dir
	if e.dir == "" {
		e.dir = filepath.Join(t.TempDir(), "data")
	}
	e.cfgDir = t.TempDir()
	e.cfg = filepath.Join(e.cfgDir, "config.json")
	e.port = freePort(t)
	e.writeConfig(o.editConfig)
	e.otlp, e.tracing = o.otlp, o.tracing
	e.start(o.fs)
	t.Cleanup(func() { e.d.Stop() })
	return e
}

func (e *env) writeConfig(edit func(map[string]any)) {
	e.t.Helper()
	raw, err := os.ReadFile("../../testdata/config/valid.json")
	if err != nil {
		e.t.Fatal(err)
	}
	var c map[string]any
	if err := json.Unmarshal(raw, &c); err != nil {
		e.t.Fatal(err)
	}
	c["listen_port"] = e.port
	c["public_url"] = "https://focus.example.test"
	if edit != nil {
		edit(c)
	}
	out, err := json.Marshal(c)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(e.cfg, out, 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) start(fs store.FS) {
	e.t.Helper()
	d, err := daemon.Start(context.Background(), daemon.Params{
		ConfigPath: e.cfg, DataDir: e.dir, Version: "test", Log: e.log, Clock: e.clock,
		Player: e.player, Notifier: e.player, FS: fs, OTLPLogs: e.otlp, Tracing: e.tracing, HeartbeatInterval: 50 * time.Millisecond,
		ProbeInterval: time.Hour,
	})
	if err != nil {
		e.t.Fatalf("Start: %v\nlog:\n%s", err, e.log.String())
	}
	e.d = d
}

// restart stops the daemon and starts it again on the same data directory.
func (e *env) restart(fs store.FS) {
	e.t.Helper()
	e.d.Stop()
	e.start(fs)
}

// id is a fresh client id.
func (e *env) id() string {
	e.ids++
	return string(testutil.IDOf(local(0, 0), 'C', e.ids))
}

func (e *env) url(path string) string { return fmt.Sprintf("http://127.0.0.1:%d%s", e.port, path) }

// resp is a response read in full.
type resp struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r resp) json(t testing.TB, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("response body is not the expected JSON: %v\n%s", err, r.Body)
	}
}

func (r resp) Reason() string {
	var p struct{ Reason string }
	_ = json.Unmarshal(r.Body, &p)
	return p.Reason
}

// raw sends a request with the headers given and returns the response, without
// any contract check.
func (e *env) raw(method, path string, body []byte, hdr map[string]string) resp {
	e.t.Helper()
	req, err := http.NewRequest(method, e.url(path), bytes.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	for k, v := range hdr {
		if strings.EqualFold(k, "Host") {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	res, err := e.client.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp{Status: res.StatusCode, Header: res.Header, Body: b}
}

// call sends a request as a client would (JSON in, X-Client cli) and checks
// the request body and the response against the contract.
func (e *env) call(method, path string, body any) resp {
	e.t.Helper()
	var raw []byte
	hdr := map[string]string{"X-Client": "cli"}
	if body != nil {
		raw = marshal(e.t, body)
		hdr["Content-Type"] = "application/json"
	}
	r := e.raw(method, path, raw, hdr)
	tmpl, ok := e.spec.TemplateOf(strings.SplitN(path, "?", 2)[0])
	if !ok {
		e.t.Fatalf("%s is not a path of the OpenAPI document", path)
	}
	if body != nil {
		e.spec.ValidateRequest(e.t, method, tmpl, raw)
	}
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "text/") {
		e.spec.ValidateResponse(e.t, method, tmpl, r.Status, ct, r.Body)
	}
	return r
}

func (e *env) get(path string) resp         { e.t.Helper(); return e.call("GET", path, nil) }
func (e *env) post(path string, b any) resp { e.t.Helper(); return e.call("POST", path, b) }

func marshal(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ok posts and requires a 200, returning the decoded result.
func (e *env) ok(path string, body any) map[string]any {
	e.t.Helper()
	r := e.post(path, body)
	if r.Status != 200 {
		e.t.Fatalf("POST %s: status %d: %s", path, r.Status, r.Body)
	}
	var out map[string]any
	r.json(e.t, &out)
	return out
}

// refused posts and requires a problem with the given status and reason.
func (e *env) refused(path string, body any, status int, reason string) map[string]any {
	e.t.Helper()
	r := e.post(path, body)
	if r.Status != status || r.Reason() != reason {
		e.t.Fatalf("POST %s: status %d reason %q, want %d %q\n%s", path, r.Status, r.Reason(), status, reason, r.Body)
	}
	var out map[string]any
	r.json(e.t, &out)
	return out
}

// state reads GET /api/v1/state.
func (e *env) state() map[string]any {
	e.t.Helper()
	r := e.get("/api/v1/state")
	if r.Status != 200 {
		e.t.Fatalf("GET /state: %d %s", r.Status, r.Body)
	}
	var s map[string]any
	r.json(e.t, &s)
	return s
}

// bootstrap sets the day, week and sprint, as the first change of a log.
func (e *env) bootstrap() map[string]any {
	e.t.Helper()
	return e.ok("/api/v1/periods/change", map[string]any{
		"id": e.id(),
		"changes": []map[string]any{
			{"kind": "day", "start": "2026-10-07", "tz": "America/New_York"},
			{"kind": "week", "start": "2026-10-05", "end": "2026-10-11", "tz": "America/New_York"},
			{"kind": "sprint", "start": "2026-10-05", "end": "2026-10-18", "tz": "America/New_York"},
		},
	})
}

// startCycle starts a cycle of a type now and returns its id.
func (e *env) startCycle(typ string) string {
	e.t.Helper()
	e.ok("/api/v1/cycles/start", map[string]any{"id": e.id(), "type": typ})
	focus, _ := e.state()["focus"].(map[string]any)
	if focus == nil || focus["type"] != typ {
		e.t.Fatalf("after starting %s the focus is %v", typ, focus)
	}
	return focus["id"].(string)
}

// eventually waits for cond, polling.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

var _ = event.ID("")

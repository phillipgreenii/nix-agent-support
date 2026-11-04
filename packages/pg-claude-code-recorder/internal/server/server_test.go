package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

func sockPath(t *testing.T) string {
	// Unix socket paths are short on darwin (104 bytes): keep it under /tmp.
	d, err := os.MkdirTemp("/tmp", "ccrec")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return filepath.Join(d, "r.sock")
}

func newServer(t *testing.T) (*Server, *obs.Obs, string) {
	o := obs.New(nil)
	s := New(o, []string{"swiftbar"}, func() any { return map[string]any{"protocol_version": 1} })
	s.Register("echo", KindQuery, func(_ context.Context, body []byte) (any, *wire.Error) {
		return map[string]string{"body": string(body)}, nil
	})
	s.Register("boom", KindQuery, func(context.Context, []byte) (any, *wire.Error) { panic("kaput") })
	s.Register("nope", KindWait, func(context.Context, []byte) (any, *wire.Error) {
		return nil, wire.Errf(wire.ErrNotFound, "no such thing")
	})
	p := sockPath(t)
	if err := s.Listen(p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, o, p
}

// The HM module puts the socket in the state home, a directory nothing else
// may have created before the daemon starts (pg2-wb689g).
func TestListenCreatesMissingSocketDirectory(t *testing.T) {
	p := filepath.Join(filepath.Dir(sockPath(t)), "state", "pg-claude-code-recorder", "recorder.sock")
	s := New(obs.New(nil), nil, func() any { return nil })
	if err := s.Listen(p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if fi, err := os.Stat(p); err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket not created: %v", err)
	}
}

func TestSocketIs0600AndServesHelloAndOperations(t *testing.T) {
	_, o, p := newServer(t)
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket %v %v", fi, err)
	}
	c := client.New(p, "swiftbar")
	var hello map[string]any
	if err := c.Do(context.Background(), "GET", "/v1/hello", nil, &hello); err != nil || hello["protocol_version"] != float64(1) {
		t.Fatalf("hello %v %v", hello, err)
	}
	var echo map[string]string
	if err := c.Post(context.Background(), "echo", map[string]int{"a": 1}, &echo); err != nil || echo["body"] != `{"a":1}` {
		t.Fatalf("echo %v %v", echo, err)
	}
	if v, _ := o.Reg.Counter(obs.MRequestsTotal, map[string]string{"client": "swiftbar", "op": "echo", "outcome": "ok"}); v != 1 {
		t.Fatalf("request counter %v", v)
	}
	if h := o.Reg.HistL(obs.MQueryDuration, map[string]string{"op": "echo"}); h == nil || h.Samples != 1 {
		t.Fatalf("query histogram %+v", h)
	}
}

func TestErrorsCarryTheirStableNameAndUnknownClientsAreOther(t *testing.T) {
	_, o, p := newServer(t)
	c := client.New(p, "example-unlisted-client")
	err := c.Post(context.Background(), "nope", nil, nil)
	var we *wire.Error
	if !errors.As(err, &we) || we.Name != wire.ErrNotFound {
		t.Fatalf("got %v", err)
	}
	err = c.Post(context.Background(), "missing-op", nil, nil)
	if !errors.As(err, &we) || we.Name != wire.ErrNotFound {
		t.Fatalf("unknown op: %v", err)
	}
	err = c.Post(context.Background(), "boom", nil, nil)
	if !errors.As(err, &we) || we.Name != wire.ErrInternal {
		t.Fatalf("panic: %v", err)
	}
	if v, _ := o.Reg.Counter(obs.MRequestsTotal, map[string]string{"client": "other", "op": "nope", "outcome": "not_found"}); v != 1 {
		t.Fatalf("not_found counter %v", v)
	}
	if v, _ := o.Reg.Counter(obs.MRequestsTotal, map[string]string{"client": "other", "op": "unknown", "outcome": "not_found"}); v != 1 {
		t.Fatalf("unknown-op counter %v", v)
	}
	if h := o.Reg.Hist(obs.MWaitDuration); h == nil || h.Samples != 1 {
		t.Fatalf("wait histogram %+v", h)
	}
}

func TestStaleSocketIsReplacedAndLiveOneIsRefused(t *testing.T) {
	p := sockPath(t)
	ln, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close() // leaves the socket file behind: a stale socket
	s := New(obs.New(nil), nil, func() any { return nil })
	if err := s.Listen(p); err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	defer s.Close()
	s2 := New(obs.New(nil), nil, func() any { return nil })
	if err := s2.Listen(p); err == nil {
		t.Fatal("a live daemon's socket was taken over")
	}
}

func TestRegularFileAtTheSocketPathIsNeverRemoved(t *testing.T) {
	p := sockPath(t)
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := New(obs.New(nil), nil, func() any { return nil }).Listen(p); err == nil {
		t.Fatal("regular file replaced")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}

func TestOfflineClientReportsDaemonOffline(t *testing.T) {
	c := client.New(sockPath(t), "cli")
	err := c.Post(context.Background(), "status", nil, nil)
	var we *wire.Error
	if !errors.As(err, &we) || we.Name != wire.ErrDaemonOffline {
		t.Fatalf("got %v", err)
	}
}

// A timed-out request logs one query_timeout carrying its request id, the kind
// and key arguments, and how its time split between running and waiting. The
// handler advances an injected clock and charges the trace, so nothing sleeps.
func TestQueryTimeoutLogsKindArgsAndWaitVersusExec(t *testing.T) {
	o := obs.New(nil)
	s := New(o, nil, func() any { return nil })
	clk := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clk }
	s.Register("query", KindQuery, func(ctx context.Context, _ []byte) (any, *wire.Error) {
		clk = clk.Add(1500 * time.Millisecond)
		obs.TraceFrom(ctx).AddExec(400 * time.Millisecond)
		return nil, wire.Errf(wire.ErrQueryTimeout, "deadline")
	})
	body := `{"op":"select","from":"records","limit":5,"api_token":"hunter2","where":{"session":"abc"}}`
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("POST", "/v1/query", strings.NewReader(body))
		r.Header.Set(wire.ClientHeader, "swiftbar")
		s.Handler().ServeHTTP(httptest.NewRecorder(), r)
	}
	evs := o.EventsNamed(obs.EvQueryTimeout)
	if len(evs) != 2 {
		t.Fatalf("%d query_timeout events, want one per timeout", len(evs))
	}
	a := evs[0].Attrs
	for k, want := range map[string]string{
		"op": "query", "client": "swiftbar", "ms": "1500", "wait_ms": "1100", "exec_ms": "400",
		"query_kind": "query/select/records",
		"args":       `api_token=` + "", // replaced below
	} {
		if k == "args" {
			continue
		}
		if a[k] != want {
			t.Errorf("%s = %q, want %q", k, a[k], want)
		}
	}
	if want := `from=records limit=5 op=select where={"session":"abc"}`; a["args"] != want {
		t.Errorf("args = %q, want %q", a["args"], want)
	}
	if strings.Contains(a["args"], "hunter2") {
		t.Error("a credential-named argument was logged")
	}
	if a["request_id"] == "" || a["request_id"] == evs[1].Attrs["request_id"] {
		t.Errorf("request ids %q and %q must be set and distinct", a["request_id"], evs[1].Attrs["request_id"])
	}
}

func logServer(t *testing.T) (*Server, *obs.Obs, *bytes.Buffer, *time.Time) {
	var buf bytes.Buffer
	o := obs.New(slog.New(slog.NewTextHandler(&buf, nil)))
	s := New(o, nil, func() any { return nil })
	clock := time.Unix(1_000_000, 0)
	s.now = func() time.Time { return clock }
	return s, o, &buf, &clock
}

func post(t *testing.T, s *Server, op string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/"+op, strings.NewReader("{}"))
	s.Handler().ServeHTTP(httptest.NewRecorder(), req)
}

func TestRequestHistogramsCarryOnlyTheOpLabel(t *testing.T) {
	s, o, _, _ := logServer(t)
	s.Register("alpha", KindQuery, func(context.Context, []byte) (any, *wire.Error) { return 1, nil })
	s.Register("beta", KindQuery, func(context.Context, []byte) (any, *wire.Error) { return 1, nil })
	s.Register("gamma", KindFeed, func(context.Context, []byte) (any, *wire.Error) { return 1, nil })
	post(t, s, "alpha")
	post(t, s, "alpha")
	post(t, s, "beta")
	post(t, s, "gamma")
	for op, want := range map[string]uint64{"alpha": 2, "beta": 1} {
		if h := o.Reg.HistL(obs.MQueryDuration, map[string]string{"op": op}); h == nil || h.Samples != want {
			t.Errorf("query op %s: %+v, want %d samples", op, h, want)
		}
	}
	if h := o.Reg.HistL(obs.MFeedDuration, map[string]string{"op": "gamma"}); h == nil || h.Samples != 1 {
		t.Errorf("feed gamma: %+v", h)
	}
	if h := o.Reg.HistL(obs.MFeedDuration, map[string]string{"op": "alpha"}); h != nil && h.Samples != 0 {
		t.Errorf("alpha leaked into feed: %+v", h)
	}
	for _, n := range []string{obs.MQueryDuration, obs.MFeedDuration} {
		if h := o.Reg.Hist(n); h != nil {
			t.Errorf("%s has an unlabelled series: %+v", n, h)
		}
	}
}

func TestRequestsInFlightIsDecrementedOnEveryExitPath(t *testing.T) {
	s, o, _, _ := logServer(t)
	var seen float64
	l := func(op string) map[string]string { return map[string]string{"op": op} }
	s.Register("ok", KindQuery, func(context.Context, []byte) (any, *wire.Error) {
		seen, _ = o.Reg.Gauge(obs.MRequestsInFlight, l("ok"))
		return 1, nil
	})
	s.Register("panics", KindQuery, func(context.Context, []byte) (any, *wire.Error) { panic("kaput") })
	s.Register("times_out", KindQuery, func(context.Context, []byte) (any, *wire.Error) {
		return nil, wire.Errf(wire.ErrQueryTimeout, "deadline")
	})
	for _, op := range []string{"ok", "panics", "times_out"} {
		post(t, s, op)
		if v, ok := o.Reg.Gauge(obs.MRequestsInFlight, l(op)); !ok || v != 0 {
			t.Errorf("%s: requests_inflight %v (set %v) after the request, want 0", op, v, ok)
		}
	}
	if seen != 1 {
		t.Errorf("requests_inflight during the handler = %v, want 1", seen)
	}
}

func TestSlowRequestLogsOncePerOpPerMinute(t *testing.T) {
	s, _, buf, clock := logServer(t)
	s.Register("slow", KindQuery, func(context.Context, []byte) (any, *wire.Error) {
		*clock = clock.Add(11 * time.Second)
		return 1, nil
	})
	s.Register("quick", KindQuery, func(context.Context, []byte) (any, *wire.Error) { return 1, nil })
	count := func() int { return strings.Count(buf.String(), "slow_request") }
	post(t, s, "quick")
	if count() != 0 {
		t.Fatalf("a quick request logged slow_request:\n%s", buf)
	}
	post(t, s, "slow")
	if count() != 1 {
		t.Fatalf("want one slow_request, got:\n%s", buf)
	}
	for _, want := range []string{"level=WARN", "op=slow", "client=", "ms=11000"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("slow_request line lacks %q:\n%s", want, buf)
		}
	}
	post(t, s, "slow") // 11 s later: inside the minute
	if count() != 1 {
		t.Fatalf("second slow request within a minute logged again:\n%s", buf)
	}
	*clock = clock.Add(time.Minute)
	post(t, s, "slow")
	if count() != 2 {
		t.Fatalf("a slow request after the minute should log again:\n%s", buf)
	}
}

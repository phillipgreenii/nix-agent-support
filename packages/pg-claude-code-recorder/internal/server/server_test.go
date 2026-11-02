package server

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

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
	if h := o.Reg.Hist(obs.MQueryDuration); h == nil || h.Samples != 1 {
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

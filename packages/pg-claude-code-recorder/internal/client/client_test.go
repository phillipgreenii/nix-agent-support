package client

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// headerSeen serves one socket and reports the deadline header of each request.
func headerSeen(t *testing.T) (string, chan string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ccrec")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "r.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan string, 4)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get(wire.DeadlineHeader)
		_, _ = w.Write([]byte("{}"))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock, seen
}

func TestDoSetsTheDeadlineHeaderFromItsOwnTimeoutMinusOneSecond(t *testing.T) {
	sock, seen := headerSeen(t)
	c := New(sock, "test")
	if err := c.Post(context.Background(), "echo", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got != "29000" {
		t.Fatalf("default 30s timeout sent %q, want 29000", got)
	}
	c.HTTP.Timeout = 5 * time.Second
	if err := c.Post(context.Background(), "echo", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got != "4000" {
		t.Fatalf("5s timeout sent %q, want 4000", got)
	}
}

func TestDoSendsNoDeadlineHeaderWithoutAUsableTimeout(t *testing.T) {
	sock, seen := headerSeen(t)
	c := New(sock, "test")
	for _, d := range []time.Duration{0, time.Second} {
		c.HTTP.Timeout = d
		if err := c.Post(context.Background(), "echo", nil, nil); err != nil {
			t.Fatal(err)
		}
		if got := <-seen; got != "" {
			t.Fatalf("timeout %v sent %q, want no header", d, got)
		}
	}
}

// A daemon that answers after the client's own timeout is a query_timeout, not unavailable
// (callers read unavailable as "daemon offline").
func TestDoNamesAClientTimeoutQueryTimeout(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ccrec")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "r.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-done
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { close(done); _ = srv.Close() })

	c := New(sock, "test")
	c.HTTP.Timeout = 100 * time.Millisecond
	err = c.Post(context.Background(), "echo", nil, nil)
	we, ok := err.(*wire.Error)
	if !ok || we.Name != wire.ErrQueryTimeout {
		t.Fatalf("err = %v, want a query_timeout wire error", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	c.HTTP.Timeout = 0
	err = c.Post(ctx, "echo", nil, nil)
	if we, ok := err.(*wire.Error); !ok || we.Name != wire.ErrQueryTimeout {
		t.Fatalf("ctx deadline: err = %v, want a query_timeout wire error", err)
	}
}

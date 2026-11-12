package recorderfacts

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

// lateDaemon serves hello at once and every other request only after delay (or
// when the test ends), like a daemon that is up but slow to answer a query.
func lateDaemon(t *testing.T, delay time.Duration) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pamon")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "r.sock")
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/hello" {
			_, _ = w.Write([]byte(`{"protocol_version":1}`))
			return
		}
		select {
		case <-time.After(delay):
		case <-done:
		}
		_, _ = w.Write([]byte(`{}`))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { close(done); _ = srv.Close() })
	return sock
}

// A daemon that is up but answers after the client's deadline is a timeout, never "daemon offline".
func TestSessionsAndSearchReportATimeoutNotDaemonOffline(t *testing.T) {
	f := New(client.Options{Socket: lateDaemon(t, 5*time.Second)})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	for name, call := range map[string]func() error{
		"sessions": func() error { _, err := f.Sessions(ctx, time.Time{}, time.Time{}); return err },
		"search":   func() error { _, err := f.Search(ctx, "word", "", time.Time{}, time.Time{}, true); return err },
		"registrations": func() error {
			_, err := f.Registrations(ctx)
			return err
		},
	} {
		err := call()
		if err == nil {
			t.Fatalf("%s: want an error", name)
		}
		if errors.Is(err, session.ErrDaemonOffline) || strings.Contains(err.Error(), "the recorder is not available") {
			t.Errorf("%s: a late answer must not read as daemon offline: %v", name, err)
		}
		if !errors.Is(err, client.ErrQueryTimeout) || !strings.Contains(err.Error(), "query_timeout") {
			t.Errorf("%s: want a query_timeout error, got %v", name, err)
		}
	}
}

// A socket nobody listens on is still "daemon offline".
func TestAbsentDaemonIsStillOffline(t *testing.T) {
	f := New(client.Options{Socket: filepath.Join(t.TempDir(), "absent.sock")})
	_, err := f.Sessions(context.Background(), time.Time{}, time.Time{})
	if !errors.Is(err, session.ErrDaemonOffline) {
		t.Fatalf("err = %v, want daemon offline", err)
	}
}

// Unavailable names the action, the recorder and the socket for an offline
// error, and leaves every other error (a timeout, say) untouched.
func TestUnavailableNamesTheRecorderAndSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "absent.sock")
	f := New(client.Options{Socket: sock})
	_, err := f.Sessions(context.Background(), time.Time{}, time.Time{})
	want := "cannot list sessions: the recorder is not available (socket " + sock + ")"
	if got := f.Unavailable("list sessions", err); got == nil || got.Error() != want || !errors.Is(got, session.ErrDaemonOffline) {
		t.Fatalf("Unavailable = %v, want %q wrapping ErrDaemonOffline", got, want)
	}
	other := errors.New("boom")
	if got := f.Unavailable("search", other); got != other {
		t.Errorf("a non-offline error must pass through unchanged, got %v", got)
	}
}

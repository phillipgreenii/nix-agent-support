package pprofserver

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shortDir returns a short temp directory: a unix socket path is limited to
// about 104 bytes on macOS, and t.TempDir() paths there can exceed it.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// unixClient dials sock for every request, whatever host the URL names.
func unixClient(sock string) *http.Client {
	return &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
	}
}

func get(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return resp.StatusCode, string(body)
}

func TestUnixSocketServesProfiles(t *testing.T) {
	sock := filepath.Join(shortDir(t), "pprof.sock")
	srv, err := Start(sock)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Close()

	c := unixClient(sock)
	code, body := get(t, c, "http://pprof/debug/pprof/")
	if code != http.StatusOK || !strings.Contains(body, "goroutine") {
		t.Fatalf("index: code=%d body=%q", code, body)
	}
	code, body = get(t, c, "http://pprof/debug/pprof/goroutine?debug=1")
	if code != http.StatusOK || !strings.Contains(body, "goroutine profile") {
		t.Fatalf("goroutine: code=%d body=%.80q", code, body)
	}
	code, body = get(t, c, "http://pprof/debug/pprof/profile?seconds=1")
	if code != http.StatusOK || len(body) == 0 {
		t.Fatalf("cpu profile: code=%d len=%d", code, len(body))
	}
}

func TestOnlyPprofRoutesAreServed(t *testing.T) {
	sock := filepath.Join(shortDir(t), "pprof.sock")
	srv, err := Start(sock)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Close()

	// net/http/pprof registers on http.DefaultServeMux at import; the server must
	// use its own mux, so nothing else on the default mux (expvar, other
	// handlers) leaks onto this listener.
	for _, p := range []string{"/", "/debug/vars", "/metrics"} {
		if code, _ := get(t, unixClient(sock), "http://pprof"+p); code != http.StatusNotFound {
			t.Errorf("GET %s: code=%d, want 404", p, code)
		}
	}
}

func TestUnixSocketIsOwnerOnly(t *testing.T) {
	sock := filepath.Join(shortDir(t), "pprof.sock")
	srv, err := Start(sock)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Close()

	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("socket mode = %o, want 600", got)
	}
}

func TestTCPLoopback(t *testing.T) {
	srv, err := Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Close()

	host, _, err := net.SplitHostPort(srv.Addr())
	if err != nil || host != "127.0.0.1" {
		t.Fatalf("Addr() = %q, want 127.0.0.1:<port>", srv.Addr())
	}
	code, body := get(t, http.DefaultClient, "http://"+srv.Addr()+"/debug/pprof/")
	if code != http.StatusOK || !strings.Contains(body, "heap") {
		t.Fatalf("index: code=%d body=%.80q", code, body)
	}
}

func TestLocalhostNameBindsLoopbackOnly(t *testing.T) {
	srv, err := Start("localhost:0")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Close()
	if host, _, _ := net.SplitHostPort(srv.Addr()); host != "127.0.0.1" {
		t.Fatalf("localhost bound %q, want 127.0.0.1", srv.Addr())
	}
}

func TestRejectsAddressesThatAreNotLocal(t *testing.T) {
	for _, addr := range []string{
		"",
		":6060",
		"0.0.0.0:6060",
		"[::]:6060",
		"192.168.1.5:6060",
		"example.com:6060",
		"6060",
		"relative/pprof.sock",
	} {
		srv, err := Start(addr)
		if err == nil {
			srv.Close()
			t.Errorf("Start(%q) succeeded, want an error", addr)
		}
	}
}

func TestReplacesStaleSocketFile(t *testing.T) {
	sock := filepath.Join(shortDir(t), "pprof.sock")

	// A crashed daemon leaves its socket file behind: listen, then close without
	// unlinking.
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(sock); err != nil {
		t.Fatalf("stale socket was not left behind: %v", err)
	}

	srv, err := Start(sock)
	if err != nil {
		t.Fatalf("Start over a stale socket: %v", err)
	}
	defer srv.Close()
	if code, _ := get(t, unixClient(sock), "http://pprof/debug/pprof/"); code != http.StatusOK {
		t.Fatalf("code=%d, want 200", code)
	}
}

func TestRefusesToReplaceARegularFile(t *testing.T) {
	path := filepath.Join(shortDir(t), "pprof.sock")
	if err := os.WriteFile(path, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}

	srv, err := Start(path)
	if err == nil {
		srv.Close()
		t.Fatal("Start over a regular file succeeded, want an error")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != "precious" {
		t.Fatalf("regular file was disturbed: %q, %v", got, readErr)
	}
}

func TestCloseRemovesSocketAndIsIdempotent(t *testing.T) {
	sock := filepath.Join(shortDir(t), "pprof.sock")
	srv, err := Start(sock)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	srv.Close()
	srv.Close()
	if _, err := os.Lstat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket still present after Close: %v", err)
	}
	if _, err := unixClient(sock).Get("http://pprof/debug/pprof/"); err == nil {
		t.Fatal("request succeeded after Close")
	}
}

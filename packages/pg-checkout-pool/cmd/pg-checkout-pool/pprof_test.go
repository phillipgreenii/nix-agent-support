package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func pprofEnvOf(v string) func(string) string {
	return func(k string) string {
		if k == pprofEnv {
			return v
		}
		return ""
	}
}

func TestStartPprofFromEnvUnsetIsSilent(t *testing.T) {
	var out bytes.Buffer
	stop := startPprofFromEnv(pprofEnvOf(""), &out)
	stop()
	if out.Len() != 0 {
		t.Fatalf("unset %s wrote %q, want nothing", pprofEnv, out.String())
	}
}

func TestStartPprofFromEnvRejectedAddressDoesNotStopTheDaemon(t *testing.T) {
	var out bytes.Buffer
	stop := startPprofFromEnv(pprofEnvOf("0.0.0.0:6060"), &out)
	stop() // must be callable and harmless
	if !strings.Contains(out.String(), pprofEnv+" ignored") {
		t.Fatalf("rejection was not reported: %q", out.String())
	}
}

func TestStartPprofFromEnvLoopbackServesAndStops(t *testing.T) {
	var out bytes.Buffer
	stop := startPprofFromEnv(pprofEnvOf("127.0.0.1:0"), &out)
	const prefix = "daemon: pprof listening on "
	line := out.String()
	if !strings.HasPrefix(line, prefix) {
		stop()
		t.Fatalf("no listening line: %q", line)
	}
	addr := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	resp, err := httpGet(t, http.DefaultClient, "http://"+addr+"/debug/pprof/")
	if err != nil {
		stop()
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		stop()
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	stop()
	if resp, err := httpGet(t, http.DefaultClient, "http://"+addr+"/debug/pprof/"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("still serving after stop")
	}
}

// shortTempDir returns a directory under /tmp: unix-socket paths are limited
// to about 104 bytes on macOS, which t.TempDir() can exceed.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pcp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestStartPprofFromEnvUnixSocketServesWith0600AndStops(t *testing.T) {
	// The socket's parent does not exist yet: the helper must create it (serve
	// does not create the state dir; launchd only creates the log files' one).
	sock := filepath.Join(shortTempDir(t), "state", "pprof.sock")

	var out bytes.Buffer
	stop := startPprofFromEnv(pprofEnvOf(sock), &out)
	stopped := false
	defer func() {
		if !stopped {
			stop()
		}
	}()
	if !strings.Contains(out.String(), sock) {
		t.Fatalf("listening line does not name the socket: %q", out.String())
	}
	fi, err := os.Lstat(sock)
	if err != nil {
		t.Fatalf("socket not created: %v", err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("socket mode = %v, want 0600", mode)
	}

	client := &http.Client{Transport: &http.Transport{
		DialContext: dialUnix(sock),
	}}
	resp, err := httpGet(t, client, "http://pprof/debug/pprof/")
	if err != nil {
		t.Fatalf("GET over socket: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	stop()
	stopped = true
	if resp, err := httpGet(t, client, "http://pprof/debug/pprof/"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("still serving after stop")
	}
}

func dialUnix(sock string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}
}

// runServeUntilReady runs serve on an ephemeral loopback port, stops it with
// SIGTERM once it is listening, and returns what it wrote to stderr.
func runServeUntilReady(t *testing.T) string {
	t.Helper()
	e := newEnv(t)
	var out, errb bytes.Buffer
	done := make(chan int, 1)
	ready := make(chan struct{})
	go func() {
		done <- e.scrapeApp(&out, &errb).serve([]string{"--addr", "127.0.0.1:0", "--root", e.root}, func(string) { close(ready) })
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("serve never became ready")
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("serve exit = %d, stderr %q", code, errb.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop")
	}
	return errb.String()
}

func TestServeOpensPprofOnlyWhenEnabled(t *testing.T) {
	t.Setenv(pprofEnv, "")
	if got := runServeUntilReady(t); strings.Contains(got, "pprof") {
		t.Errorf("pprof mentioned with %s unset: %q", pprofEnv, got)
	}
	t.Setenv(pprofEnv, "127.0.0.1:0")
	got := runServeUntilReady(t)
	if !strings.Contains(got, "pprof listening on 127.0.0.1:") {
		t.Errorf("pprof not started on loopback when enabled: %q", got)
	}
}

func TestServeRefusesNonLoopbackPprofButKeepsServing(t *testing.T) {
	t.Setenv(pprofEnv, "0.0.0.0:0")
	got := runServeUntilReady(t)
	if !strings.Contains(got, pprofEnv+" ignored") || strings.Contains(got, "pprof listening") {
		t.Errorf("non-loopback pprof address was not refused: %q", got)
	}
}

// httpGet issues a GET bound to the test's context (the non-Context
// http.Get forms are what the noctx linter rejects).
func httpGet(t *testing.T, c *http.Client, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c.Do(req)
}

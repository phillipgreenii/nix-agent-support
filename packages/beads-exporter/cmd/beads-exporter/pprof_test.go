package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	resp, err := http.Get("http://" + addr + "/debug/pprof/")
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
	if resp, err := http.Get("http://" + addr + "/debug/pprof/"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("still serving after stop")
	}
}

// shortTempDir returns a directory under /tmp: unix-socket paths are limited
// to about 104 bytes on macOS, which t.TempDir() can exceed.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "bxp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestStartPprofFromEnvUnixSocketServesWith0600AndStops(t *testing.T) {
	// The module's launchd script creates the state dir before starting the
	// exporter, so the socket's parent exists.
	sock := filepath.Join(shortTempDir(t), "pprof.sock")

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
	resp, err := client.Get("http://pprof/debug/pprof/")
	if err != nil {
		t.Fatalf("GET over socket: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	stop()
	stopped = true
	if resp, err := client.Get("http://pprof/debug/pprof/"); err == nil {
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

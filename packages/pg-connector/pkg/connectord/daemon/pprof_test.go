package daemon

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

// pprofEnv is a stand-in name: the endpoint reads whichever variable the Spec names.
const pprofEnv = "X_PG_CONNECTOR_PPROF_ADDR"

func pprofEnvOf(v string) func(string) string {
	return func(k string) string {
		if k == pprofEnv {
			return v
		}
		return ""
	}
}

func TestStartPprofUnsetIsSilent(t *testing.T) {
	var out bytes.Buffer
	stop := StartPprof(pprofEnv, pprofEnvOf(""), &out)
	stop()
	if out.Len() != 0 {
		t.Fatalf("unset %s wrote %q, want nothing", pprofEnv, out.String())
	}
}

func TestStartPprofRejectedAddressDoesNotStopTheDaemon(t *testing.T) {
	var out bytes.Buffer
	stop := StartPprof(pprofEnv, pprofEnvOf("0.0.0.0:6060"), &out)
	stop() // must be callable and harmless
	if !strings.Contains(out.String(), pprofEnv+" ignored") {
		t.Fatalf("rejection was not reported: %q", out.String())
	}
}

func TestStartPprofLoopbackServesAndStops(t *testing.T) {
	var out bytes.Buffer
	stop := StartPprof(pprofEnv, pprofEnvOf("127.0.0.1:0"), &out)
	const prefix = "daemon: pprof listening on "
	line := out.String()
	if !strings.HasPrefix(line, prefix) {
		stop()
		t.Fatalf("no listening line: %q", line)
	}
	addr := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	resp, err := getURL(http.DefaultClient, "http://"+addr+"/debug/pprof/")
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
	if resp, err := getURL(http.DefaultClient, "http://"+addr+"/debug/pprof/"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("still serving after stop")
	}
}

func TestStartPprofUnixSocketServesWith0600AndStops(t *testing.T) {
	// The helper creates the socket's missing parent directory (the state
	// directory may not exist yet when the daemon starts), so nest one level.
	sock := filepath.Join(shortTempDir(t), "state", "pprof.sock")

	var out bytes.Buffer
	stop := StartPprof(pprofEnv, pprofEnvOf(sock), &out)
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
	resp, err := getURL(client, "http://pprof/debug/pprof/")
	if err != nil {
		t.Fatalf("GET over socket: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	stop()
	stopped = true
	if resp, err := getURL(client, "http://pprof/debug/pprof/"); err == nil {
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

// getURL issues a GET through c with a background context, so the request
// has a context to carry.
func getURL(c *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// shortTempDir returns a fresh directory with a short absolute path: a unix
// socket path is limited to about 100 bytes, which t.TempDir often exceeds.
func shortTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "pcd")
	if err != nil { // a sandbox without a writable /tmp: fall back to TMPDIR
		d, err = os.MkdirTemp("", "pcd")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

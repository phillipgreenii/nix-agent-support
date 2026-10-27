package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type env map[string]string

func (e env) get(k string) string { return e[k] }

func tmpDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "pgc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// pprofServer serves every request with "ok:<path>?<query>" on a real socket
// named pprof.sock and counts them.
func pprofServer(t *testing.T) (socket string, hits *atomic.Int32) {
	t.Helper()
	socket = filepath.Join(tmpDir(t), "pprof.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	hits = new(atomic.Int32)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("ok:" + r.Method + " " + r.URL.RequestURI()))
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return socket, hits
}

func do(t *testing.T, e env, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, er bytes.Buffer
	code = run(context.Background(), args, e.get, &o, &er)
	return code, o.String(), er.String()
}

func TestPprof_FetchesEachAllowedPathByName(t *testing.T) {
	sock, hits := pprofServer(t)
	e := env{"PG_PROBE_PPROF_SOCKET": sock}
	for name, want := range map[string]string{
		"index": "ok:GET /debug/pprof/", "heap": "ok:GET /debug/pprof/heap",
		"/debug/pprof/cmdline": "ok:GET /debug/pprof/cmdline", "goroutine": "ok:GET /debug/pprof/goroutine",
	} {
		code, out, errOut := do(t, e, "pprof", name)
		if code != exitOK || out != want {
			t.Errorf("%s: exit %d out %q err %q, want %q", name, code, out, errOut, want)
		}
	}
	if hits.Load() != 4 {
		t.Errorf("server saw %d requests, want 4", hits.Load())
	}
}

func TestPprof_FlagsMayFollowTheTarget(t *testing.T) {
	sock, _ := pprofServer(t)
	e := env{"PG_PROBE_PPROF_SOCKET": sock}
	code, out, errOut := do(t, e, "pprof", "profile", "--seconds", "3")
	if code != exitOK || out != "ok:GET /debug/pprof/profile?seconds=3" {
		t.Fatalf("exit %d out %q err %q", code, out, errOut)
	}
	code, out, _ = do(t, e, "pprof", "--debug", "2", "goroutine")
	if code != exitOK || out != "ok:GET /debug/pprof/goroutine?debug=2" {
		t.Fatalf("exit %d out %q", code, out)
	}
}

func TestPprof_SocketDefaultsFromXDGStateHome(t *testing.T) {
	state := tmpDir(t)
	if err := os.MkdirAll(filepath.Join(state, "pa-monitor"), 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(state, "pa-monitor", "pprof.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("here")) })}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	if code, out, errOut := do(t, env{"XDG_STATE_HOME": state}, "pprof", "index"); code != exitOK || out != "here" {
		t.Fatalf("XDG_STATE_HOME: exit %d out %q err %q", code, out, errOut)
	}
}

func TestPprof_List(t *testing.T) {
	code, out, _ := do(t, env{}, "pprof", "list")
	if code != exitOK || !strings.Contains(out, "/debug/pprof/heap\n") || strings.Count(out, "\n") != 11 {
		t.Fatalf("exit %d out %q", code, out)
	}
}

func TestPprof_RefusesOtherPathsAndNonGET_WithNegativeControl(t *testing.T) {
	sock, hits := pprofServer(t)
	e := env{"PG_PROBE_PPROF_SOCKET": sock}
	for _, args := range [][]string{
		{"pprof", "vars"},
		{"pprof", "/metrics"},
		{"pprof", "heap/"},
		{"pprof", "../vars"},
		{"pprof", "heap", "-X", "POST"},
		{"pprof", "heap", "-X", "DELETE"},
		{"pprof", "heap", "--seconds", "5"},
		{"pprof", "profile", "--debug", "1"},
	} {
		if code, _, errOut := do(t, e, args...); code != exitRefused {
			t.Errorf("%v: exit %d (stderr %q), want %d", args, code, errOut, exitRefused)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("a refused request reached the server (%d hits)", hits.Load())
	}
	// Negative control: the same server does answer an allowed request.
	if code, _, _ := do(t, e, "pprof", "heap"); code != exitOK || hits.Load() != 1 {
		t.Fatalf("control: exit %d hits %d", code, hits.Load())
	}
	// -X GET is accepted (and lower case is normalised).
	if code, _, _ := do(t, e, "pprof", "heap", "-X", "get"); code != exitOK {
		t.Errorf("-X get: exit %d", code)
	}
}

func TestPprof_ErrorsAndUsage(t *testing.T) {
	if code, _, errOut := do(t, env{"PG_PROBE_PPROF_SOCKET": "/tmp/pgc-absent/pprof.sock"}, "pprof", "heap"); code != exitError || !strings.Contains(errOut, "no pprof socket") {
		t.Errorf("missing socket: exit %d err %q", code, errOut)
	}
	if code, _, _ := do(t, env{}, "pprof"); code != exitUsage {
		t.Errorf("no target: exit %d", code)
	}
	if code, _, _ := do(t, env{}, "pprof", "a", "b"); code != exitUsage {
		t.Errorf("two targets: exit %d", code)
	}
	if code, _, _ := do(t, env{}, "pprof", "heap", "--nope"); code != exitUsage {
		t.Errorf("unknown flag: exit %d", code)
	}
	if code, _, _ := do(t, env{}); code != exitUsage {
		t.Errorf("no command: exit %d", code)
	}
	if code, _, _ := do(t, env{}, "curl"); code != exitUsage {
		t.Errorf("unknown command: exit %d", code)
	}
}

func TestHelpAndVersion(t *testing.T) {
	if code, out, _ := do(t, env{}, "--help"); code != exitOK || !strings.Contains(out, "wait-log") {
		t.Errorf("help: exit %d", code)
	}
	if code, out, _ := do(t, env{}, "--version"); code != exitOK || !strings.HasPrefix(out, "pg-probe ") {
		t.Errorf("version: exit %d out %q", code, out)
	}
}

func TestWaitLog_ExitCodes(t *testing.T) {
	root := tmpDir(t)
	e := env{"PG_PROBE_WAIT_LOG_ROOTS": root}
	p := filepath.Join(root, "x.log")
	if err := os.WriteFile(p, []byte("starting\nserver ready on 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := do(t, e, "wait-log", "--timeout", "2s", p, "ready on")
	if code != exitOK || out != "server ready on 1\n" {
		t.Fatalf("match: exit %d out %q err %q", code, out, errOut)
	}

	start := time.Now()
	code, out, errOut = do(t, e, "wait-log", "--timeout", "100ms", "--poll", "5ms", p, "never appears")
	if code != exitTimeout || out != "" || !strings.Contains(errOut, "timed out") {
		t.Fatalf("timeout: exit %d out %q err %q", code, out, errOut)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("timeout took %v", time.Since(start))
	}
	if exitTimeout == exitError || exitTimeout == exitUsage || exitTimeout == exitRefused || exitTimeout == exitOK {
		t.Error("timeout exit code is not distinct")
	}
}

func TestWaitLog_RefusesOutsideRootsAndUsage(t *testing.T) {
	root, other := tmpDir(t), tmpDir(t)
	e := env{"PG_PROBE_WAIT_LOG_ROOTS": root}
	secret := filepath.Join(other, "secret")
	if err := os.WriteFile(secret, []byte("token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := do(t, e, "wait-log", "--timeout", "1s", secret, "token"); code != exitRefused || out != "" {
		t.Errorf("outside roots: exit %d out %q", code, out)
	}
	// Negative control: the same content under a root is returned.
	in := filepath.Join(root, "ok")
	if err := os.WriteFile(in, []byte("token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := do(t, e, "wait-log", "--timeout", "1s", in, "token"); code != exitOK || out != "token\n" {
		t.Errorf("control: exit %d out %q", code, out)
	}
	if code, _, _ := do(t, e, "wait-log", in); code != exitUsage {
		t.Errorf("missing regex: exit %d", code)
	}
	if code, _, _ := do(t, e, "wait-log", in, "("); code != exitUsage {
		t.Errorf("bad regex: exit %d", code)
	}
	if code, _, _ := do(t, e, "wait-log", "--timeout", "2h", in, "x"); code != exitRefused {
		t.Errorf("timeout over cap: exit %d", code)
	}
	if code, _, _ := do(t, e, "wait-log", filepath.Join(root, "absent"), "x"); code != exitError {
		t.Errorf("missing file: exit %d", code)
	}
}

func TestDefaultWaitLogRoots(t *testing.T) {
	state := tmpDir(t)
	if err := os.Mkdir(filepath.Join(state, "pg-router-x"), 0o700); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(defaultWaitLogRoots(env{"XDG_STATE_HOME": state, "TMPDIR": "/tmp/t"}.get), " ")
	for _, want := range []string{"/tmp", "/var/log", "/tmp/t", filepath.Join(state, "pa-monitor"), filepath.Join(state, "pg-router-x")} {
		if !strings.Contains(got, want) {
			t.Errorf("default roots %q lack %q", got, want)
		}
	}
	for _, bad := range []string{"/etc", ".ssh", ".claude", state + " ", "/Users"} {
		if strings.Contains(got+" ", bad) {
			t.Errorf("default roots %q contain %q", got, bad)
		}
	}
}

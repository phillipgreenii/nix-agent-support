package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// reexecEnv marks a child process that is this test binary standing in for the
// real command: TestMain runs the real main() in it, so a test sees what a
// supervisor or a shell would see (a real exit status, a real process).
const reexecEnv = "PG_CONNECTOR_ISSUE_JIRA_TEST_REEXEC"

func TestMain(m *testing.M) {
	if os.Getenv(reexecEnv) == "1" {
		main() // exits the process
		return
	}
	code := m.Run()
	removeBuilds()
	os.Exit(code)
}

// shortTempDir returns a fresh directory with a short absolute path: a unix
// socket path is capped near 104 bytes, and t.TempDir can exceed that.
func shortTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "jm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// writeServeConfig writes a minimal config and a fake pjira under one short
// directory and returns the config path and the state directory.
func writeServeConfig(t *testing.T) (cfgPath, state string) {
	t.Helper()
	root := shortTempDir(t)
	state = filepath.Join(root, "state")
	pjira := filepath.Join(root, "pjira")
	if err := os.WriteFile(pjira, []byte("#!/bin/sh\necho unreachable >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"state_dir": state, "pjira_path": pjira})
	if err != nil {
		t.Fatal(err)
	}
	cfgPath = filepath.Join(root, "config.json")
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, state
}

// syncBuffer is a goroutine-safe stderr.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// runReal runs the real command in a child process the way a supervisor starts
// it: an otherwise empty environment (plus env) and working directory "/".
func runReal(t *testing.T, stdin string, env []string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = "/"
	cmd.Env = append([]string{reexecEnv + "=1"}, env...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatal(err)
	}
	return code, out.String(), errb.String()
}

func TestSplitConfigFlag(t *testing.T) {
	cases := []struct {
		args  []string
		path  string
		rest  []string
		given bool
		err   bool
	}{
		{args: nil},
		{args: []string{"serve"}, rest: []string{"serve"}},
		{args: []string{"--config", "/p", "serve"}, path: "/p", rest: []string{"serve"}, given: true},
		{args: []string{"serve", "--config=/p"}, path: "/p", rest: []string{"serve"}, given: true},
		{args: []string{"-config", "/p"}, path: "/p", given: true},
		{args: []string{"--config"}, given: true, err: true},
	}
	for _, c := range cases {
		path, rest, given, err := splitConfigFlag(c.args)
		if path != c.path || given != c.given || (err != nil) != c.err || strings.Join(rest, " ") != strings.Join(c.rest, " ") {
			t.Errorf("splitConfigFlag(%v) = %q %v %v %v, want %q %v %v err=%v", c.args, path, rest, given, err, c.path, c.rest, c.given, c.err)
		}
	}
}

// With no --config the binary is the old stateless backend: one request on
// stdin, one response on stdout, whatever the environment.
func TestRealProcessWithoutConfigAnswersCapabilitiesLikeTheOldBackend(t *testing.T) {
	code, out, errs := runReal(t, `{"op":"capabilities"}`, nil)
	if code != 0 {
		t.Fatalf("exit = %d, stdout=%q stderr=%q", code, out, errs)
	}
	var caps scriptout.CapabilitiesResponse
	if err := json.Unmarshal([]byte(out), &caps); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	want := newDispatchTable(newTestBackend())
	for op := range want {
		if op == scriptout.OpCapabilities {
			continue
		}
		found := false
		for _, got := range caps.Ops {
			found = found || got == op
		}
		if !found {
			t.Errorf("capabilities.ops lacks %q: %v", op, caps.Ops)
		}
	}
	if caps.SchemaVersions["issue"] == 0 || caps.Vocabulary["state"] == nil {
		t.Errorf("capabilities = %+v", caps)
	}
}

func TestRealProcessAnyOtherConfigFormIsAUsageErrorAndABadConfigExitsFour(t *testing.T) {
	cfg, _ := writeServeConfig(t)
	for _, args := range [][]string{
		{"--config", cfg},
		{"--config", cfg, "client"},
		{"--config", cfg, "serve", "extra"},
		{"--config"},
	} {
		code, out, errs := runReal(t, "", nil, args...)
		if code != 2 || out != "" || !strings.Contains(errs, "usage:") {
			t.Errorf("%v: exit=%d stdout=%q stderr=%q, want exit 2 and the usage", args, code, out, errs)
		}
	}
	for _, args := range [][]string{
		{"--config", filepath.Join(shortTempDir(t), "missing.json"), "serve"},
	} {
		code, _, errs := runReal(t, "", nil, args...)
		if code != 4 || !strings.Contains(errs, "read config") {
			t.Errorf("%v: exit=%d stderr=%q, want exit 4 naming the config", args, code, errs)
		}
	}
	bad := filepath.Join(shortTempDir(t), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"state_dir":"relative"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := runReal(t, "", nil, "--config", bad, "serve"); code != 4 {
		t.Errorf("an invalid config: exit=%d stderr=%q, want 4", code, errs)
	}
}

// startServe starts the real serve process.
func startServe(t *testing.T, cfgPath string, env ...string) (cmd *exec.Cmd, stderr *syncBuffer, done chan error) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(exe, "--config", cfgPath, "serve")
	cmd.Dir = "/"
	cmd.Env = append([]string{reexecEnv + "=1"}, env...)
	stderr = &syncBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done = make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		select {
		case err := <-done:
			done <- err
		default:
			_ = cmd.Process.Kill()
			<-done
		}
	})
	return cmd, stderr, done
}

// waitListening waits until the socket accepts a connection.
func waitListening(t *testing.T, socket string, done chan error, stderr *syncBuffer) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", socket); err == nil {
			_ = c.Close()
			return
		}
		select {
		case err := <-done:
			done <- err
			t.Fatalf("serve exited before listening: %v; stderr: %s", err, stderr.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatalf("serve never listened on %s; stderr: %s", socket, stderr.String())
}

// terminate sends SIGTERM and returns the exit code.
func terminate(t *testing.T, cmd *exec.Cmd, done chan error, stderr *syncBuffer) int {
	t.Helper()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		done <- err // the cleanup reads it again
		var ee *exec.ExitError
		switch {
		case err == nil:
			return 0
		case errors.As(err, &ee):
			return ee.ExitCode()
		default:
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("serve did not exit on SIGTERM; stderr: %s", stderr.String())
	}
	return -1
}

func TestRealServeRunsTheDaemonAndDrainsOnSIGTERM(t *testing.T) {
	cfg, state := writeServeConfig(t)
	cmd, stderr, done := startServe(t, cfg)
	waitListening(t, filepath.Join(state, "sock"), done, stderr)
	if fi, err := os.Stat(state); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("state dir: %v %v, want 0700", fi, err)
	}
	if code := terminate(t, cmd, done, stderr); code != serve.ExitOK {
		t.Errorf("SIGTERM exit = %d, want %d; stderr: %s", code, serve.ExitOK, stderr.String())
	}
	if strings.Contains(stderr.String(), "pprof") {
		t.Errorf("no profiling listener without %s: %q", pprofEnv, stderr.String())
	}
}

func TestRealServeOpensTheProfilingListenerOnlyWhenTheVariableIsSet(t *testing.T) {
	cfg, state := writeServeConfig(t)
	cmd, stderr, done := startServe(t, cfg, pprofEnv+"=127.0.0.1:0")
	waitListening(t, filepath.Join(state, "sock"), done, stderr)
	if code := terminate(t, cmd, done, stderr); code != serve.ExitOK {
		t.Errorf("exit = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "pprof listening on 127.0.0.1:") {
		t.Errorf("want the pprof listening line on stderr, got %q", stderr.String())
	}
}

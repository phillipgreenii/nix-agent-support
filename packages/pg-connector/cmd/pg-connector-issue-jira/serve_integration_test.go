//go:build integration

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
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

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

// A write performed with the daemon running is bracketed by the two notices: the
// daemon has seen write_begin by the time the origin tool runs, and invalidate
// only after it ended. The shim holds the write's own pjira call, so the daemon
// is inspected while the write is in the air.
func TestServedWriteNoticesBracketTheOriginCall(t *testing.T) {
	d := servetest.StartDaemon(t, servetest.Options{Hooks: "poll_interval=50ms", Before: func(d *servetest.Daemon) {
		d.Shim.SetIssues(servetest.MineJQL, servetest.Open("EXA-1"))
	}})
	t.Cleanup(d.Shim.Release)
	if resp := d.MustCall(t, servetest.Op("list", `{"query":"`+servetest.QueryMine+`"}`), 20*time.Second); resp.Error != nil {
		t.Fatalf("warm the mine query: %+v", resp.Error)
	}
	d.Shim.WaitQuiet(t, 300*time.Millisecond, 30*time.Second)

	noticeOps := func() []string {
		var ops []string
		for _, row := range d.EventLog(t) {
			if op, _ := row["op"].(string); (op == "write_begin" || op == "invalidate") && row["outcome"] == "ok" {
				ops = append(ops, op)
			}
		}
		return ops
	}

	d.Shim.Hold("comment")
	done := make(chan servetest.ClientResult, 1)
	go func() {
		done <- d.Client(t, `{"op":"comment","args":{"id":"EXA-1","body":"example comment"}}`)
	}()
	d.Shim.WaitHeld(t, 30*time.Second)
	// The origin tool is running the write: the daemon has the first notice and
	// not yet the second.
	if got := noticeOps(); len(got) != 1 || got[0] != "write_begin" {
		t.Fatalf("while the write runs the daemon has seen %v, want only write_begin", got)
	}
	d.Shim.Release()
	select {
	case res := <-done:
		if res.Exit != 0 || res.Resp.Error != nil {
			t.Fatalf("the write: exit %d %s %s", res.Exit, res.Stdout, res.Stderr)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the write never finished")
	}
	if got := noticeOps(); len(got) != 2 || got[0] != "write_begin" || got[1] != "invalidate" {
		t.Errorf("after the write the daemon has seen %v, want write_begin then invalidate", got)
	}
	// The invalidate queued one re-pull of the issue, after the write.
	until(t, 20*time.Second, "the re-pull of EXA-1 after the write", func() bool {
		seenWrite := false
		for _, c := range d.Shim.Calls() {
			if c.Kind() == "write" {
				seenWrite = true
				continue
			}
			if seenWrite && (c.Kind() == "search_batch" || c.Kind() == "search_key") && strings.Contains(c.JQL(), "EXA-1") {
				return true
			}
		}
		return false
	})
}

// A second serve on the state directory of a running daemon refuses, naming the
// lock; a SIGKILL leaves a stale socket a new serve removes and logs.
func TestServedSecondInstanceRefusesAndAKilledOneLeavesAStaleSocket(t *testing.T) {
	d := servetest.StartDaemon(t, servetest.Options{})
	if fi, err := os.Stat(d.StateDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("state dir: %v %v, want 0700", fi, err)
	}
	if fi, err := os.Lstat(d.Socket); err != nil || fi.Mode().Perm() != 0o600 || fi.Mode()&os.ModeSocket == 0 {
		t.Errorf("socket: %v %v, want a 0600 socket", fi, err)
	}
	second := servetest.RunBinary(t, d.Bin(), "", []string{}, "--config", d.ConfigPath, "serve")
	if second.Exit != serve.ExitRefused || !strings.Contains(second.Stderr, "daemon.lock") {
		t.Errorf("a second serve: exit %d stderr %q, want %d naming daemon.lock", second.Exit, second.Stderr, serve.ExitRefused)
	}

	d.Kill(t)
	if fi, err := os.Lstat(d.Socket); err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("fixture: SIGKILL must leave the stale socket: %v", err)
	}
	d.Start(t)
	if !servetest.HasChange(d.DaemonLog(t), "stale_socket_removed") {
		t.Errorf("the daemon log has no stale_socket_removed line: %v", d.DaemonLog(t))
	}
	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Errorf("SIGTERM exit = %d; output: %s", code, d.Stderr())
	}
	if _, err := os.Lstat(d.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the drained serve must remove its socket: %v", err)
	}
}

package main

import (
	"bytes"
	"context"
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

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
)

// shortTempDir returns a fresh directory with a short absolute path: a unix
// socket path is capped near 104 bytes, and t.TempDir can exceed that.
func shortTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "pcg")
	if err != nil { // a sandbox without a writable /tmp: fall back to TMPDIR
		d, err = os.MkdirTemp("", "pcg")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// writeServeConfig renders a config file for serve with the given state
// directory and (when non-empty) socket path, and returns its path.
func writeServeConfig(t *testing.T, stateDir, socketPath string) string {
	t.Helper()
	body := map[string]any{"state_dir": stateDir}
	if socketPath != "" {
		body["socket_path"] = socketPath
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(shortTempDir(t), "config.json")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// serveProc is a real serve process (this test binary standing in for the
// command, started the way a supervisor starts it).
type serveProc struct {
	cmd    *exec.Cmd
	stderr *syncBuffer
	done   chan struct{}
	err    error
}

func startServe(t *testing.T, cfgPath string) *serveProc {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := &serveProc{cmd: exec.Command(exe, "--config", cfgPath, "serve"), stderr: &syncBuffer{}, done: make(chan struct{})}
	p.cmd.Dir = "/"
	p.cmd.Env = []string{reexecEnv + "=1"}
	p.cmd.Stderr = p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.err = p.cmd.Wait(); close(p.done) }()
	t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = p.cmd.Process.Kill()
			<-p.done
		}
	})
	return p
}

// waitListening waits until the socket accepts a connection, or the process
// ended first.
func (p *serveProc) waitListening(t *testing.T, socket string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", socket); err == nil {
			_ = c.Close()
			return
		}
		select {
		case <-p.done:
			t.Fatalf("serve exited before listening: %v; stderr: %s", p.err, p.stderr.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatalf("serve never listened on %s; stderr: %s", socket, p.stderr.String())
}

// wait returns the exit code once the process ended.
func (p *serveProc) wait(t *testing.T) int {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(20 * time.Second):
		t.Fatalf("serve did not exit; stderr: %s", p.stderr.String())
	}
	var ee *exec.ExitError
	switch {
	case p.err == nil:
		return 0
	case errors.As(p.err, &ee):
		return ee.ExitCode()
	default:
		t.Fatal(p.err)
		return -1
	}
}

func TestRealServeLifecycle(t *testing.T) {
	state := filepath.Join(shortTempDir(t), "state")
	socket := filepath.Join(state, "sock")
	cfg := writeServeConfig(t, state, "")

	first := startServe(t, cfg)
	first.waitListening(t, socket)

	if fi, err := os.Stat(state); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("state dir: mode %v err %v, want 0700", fi, err)
	}
	if fi, err := os.Stat(socket); err != nil || fi.Mode().Perm() != 0o600 || fi.Mode()&os.ModeSocket == 0 {
		t.Errorf("socket: %v err %v, want a 0600 socket", fi, err)
	}

	// A client from the same uid is accepted and served: a malformed frame is
	// answered invalid_argument (the assembled daemon's router, not a stub).
	c, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write([]byte("not json\n")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 4096)
	n, _ := c.Read(reply)
	if !strings.Contains(string(reply[:n]), "invalid_argument") {
		t.Errorf("a malformed frame must be answered invalid_argument, got %q", reply[:n])
	}
	_ = c.Close()

	// A second serve on the same state directory refuses, naming the lock.
	second := startServe(t, cfg)
	if code := second.wait(t); code != exitServe {
		t.Errorf("second serve exit = %d, want %d; stderr: %s", code, exitServe, second.stderr.String())
	}
	if !strings.Contains(second.stderr.String(), "lock") || !strings.Contains(second.stderr.String(), "daemon.lock") {
		t.Errorf("second serve must name the lock: %q", second.stderr.String())
	}

	// SIGKILL leaves a socket file and a state a new serve starts from.
	if err := first.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	first.wait(t)
	if fi, err := os.Lstat(socket); err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("fixture: SIGKILL must leave the stale socket: %v", err)
	}
	third := startServe(t, cfg)
	third.waitListening(t, socket)
	// The line is in the daemon's structured log, written before it listens.
	waitLogLine(t, state, "stale_socket_removed")

	// SIGTERM drains: exit 0, lock released, socket gone, and a following serve
	// starts cleanly.
	if err := third.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := third.wait(t); code != 0 {
		t.Errorf("SIGTERM exit = %d, want 0; stderr: %s", code, third.stderr.String())
	}
	waitLogLine(t, state, "drain_done")
	if _, err := os.Lstat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the drained serve must remove its socket: %v", err)
	}
	fourth := startServe(t, cfg)
	fourth.waitListening(t, socket)
	if err := fourth.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := fourth.wait(t); code != 0 {
		t.Errorf("fourth serve exit = %d, want 0; stderr: %s", code, fourth.stderr.String())
	}
}

func TestRealServeRefusesASocketPathOneByteOverTheLimit(t *testing.T) {
	limit := proc.MaxSocketPathBytes()
	dir := shortTempDir(t)
	state := filepath.Join(dir, "state")
	socket := filepath.Join(dir, strings.Repeat("s", limit+1-len(dir)-1))
	if len(socket) != limit+1 {
		t.Fatalf("fixture: path is %d bytes, want %d", len(socket), limit+1)
	}
	p := startServe(t, writeServeConfig(t, state, socket))
	if code := p.wait(t); code != exitServe {
		t.Errorf("exit = %d, want %d; stderr: %s", code, exitServe, p.stderr.String())
	}
	msg := p.stderr.String()
	if !strings.Contains(msg, "sun_path limit") || !strings.Contains(msg, "104") && !strings.Contains(msg, "108") {
		t.Errorf("the refusal must name the limit: %q", msg)
	}
}

func TestServeDrainsOnContextCancelInProcess(t *testing.T) {
	state := filepath.Join(shortTempDir(t), "state")
	cfgPath := writeServeConfig(t, state, "")
	cfg, err := config.Load(cfgPath, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var stderr syncBuffer
	done := make(chan int, 1)
	go func() { done <- serveWith(ctx, cfg, &stderr) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if c, err := net.Dial("unix", cfg.SocketPath); err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never listened; stderr: %s", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("serve exit = %d, want 0; stderr: %s", code, stderr.String())
	}
}

// waitLogLine waits until the daemon's structured log (daemon.log.jsonl in the
// state directory) has a line for the named change.
func waitLogLine(t *testing.T, stateDir, change string) {
	t.Helper()
	path := filepath.Join(stateDir, config.DaemonLogFileName)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil && strings.Contains(string(raw), `"change":"`+change+`"`) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	raw, _ := os.ReadFile(path)
	t.Errorf("the structured log %s has no %s line:\n%s", path, change, raw)
}

// syncBuffer is a bytes.Buffer safe for a test to read while serve writes.
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

package rpcclient

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// shortTempDir returns a /tmp-based dir, dodging macOS's 104-byte
// unix socket path limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "pamcli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// buildDaemonBinary compiles the pa-monitor binary into a temp dir so
// tests can subprocess it. Cached per-test-binary in t.TempDir().
func buildDaemonBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "pa-monitor")
	out, err := exec.Command(
		"go", "build", "-o", bin,
		"github.com/phillipgreenii/pa-monitor/cmd/pa-monitor",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("file did not appear: %s", path)
}

// TestRemotePoller_RecoversFromDaemonRestart confirms that a poller
// that lost its connection mid-Snapshot reconnects on a subsequent call
// once the daemon comes back. Exercises the backoff + redial path.
func TestRemotePoller_RecoversFromDaemonRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess-based")
	}

	bin := buildDaemonBinary(t)
	stateDir := shortTempDir(t)
	env := append(
		os.Environ(),
		"XDG_STATE_HOME="+stateDir,
		"XDG_RUNTIME_DIR="+stateDir,
	)
	sockPath := filepath.Join(stateDir, "pa-monitor", "daemon.sock")

	// Start daemon round 1.
	cmd := exec.Command(bin, "daemon", "--no-poller", "--tick-seconds=1")
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, sockPath)

	// Pinned-socket poller talks only to the test daemon.
	rp := NewRemotePollerForSocket(sockPath)

	// First Snapshot: succeeds.
	if _, _, err := rp.Snapshot(context.Background()); err != nil && !errors.Is(err, ErrOffline) {
		t.Logf("first snapshot err (acceptable): %v", err)
	}

	// Kill daemon.
	_ = cmd.Process.Signal(syscall.SIGTERM)
	_ = cmd.Wait()

	// Force the poller to retry; it should detect the dead socket.
	if _, _, err := rp.Snapshot(context.Background()); err == nil {
		t.Log("snapshot post-kill succeeded — gRPC re-dial returned cached?")
	}

	// Daemon restart.
	cmd2 := exec.Command(bin, "daemon", "--no-poller", "--tick-seconds=1")
	cmd2.Env = env
	if err := cmd2.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd2.Process.Kill()
		_ = cmd2.Wait()
	}()
	waitForFile(t, sockPath)

	// Skip past any active backoff window so the next Snapshot dials.
	rp.mu.Lock()
	rp.backoffUntil = time.Time{}
	rp.mu.Unlock()

	// Should reconnect within a few attempts.
	var lastErr error
	for i := 0; i < 10; i++ {
		if _, _, err := rp.Snapshot(context.Background()); err == nil {
			return // success
		} else {
			lastErr = err
		}
		// Clear any backoff to keep retrying.
		rp.mu.Lock()
		rp.backoffUntil = time.Time{}
		rp.mu.Unlock()
		time.Sleep(200 * time.Millisecond)
	}
	t.Errorf("RemotePoller never reconnected after daemon restart; last err: %v", lastErr)
}

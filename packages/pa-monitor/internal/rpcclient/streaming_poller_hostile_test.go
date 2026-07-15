//go:build hostile

// Sandbox-hostile streaming-poller E2E (bead pg2-ymi3l). This test `go build`s
// the pa-monitor daemon binary, spawns it as a real subprocess over a unix
// socket, and SIGKILLs it to exercise the offline/redial recovery path. Building
// + subprocessing the daemon is unavailable/flaky inside the no-network
// `pa-monitor-go-tests` nix build sandbox, so it is split out of
// streaming_poller_test.go behind the `hostile` build tag; the default gate
// (plain `go test ./...`, no tag) runs only the in-memory fake-seam tests. The
// `eventually` helper lives in streaming_poller_test.go (untagged) and the
// build/socket helpers in testsupport_test.go (also `hostile`-tagged); all are
// available when the tag is on. Run locally with `go test -tags hostile ./...`.
package rpcclient

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestStreamingPoller_LiveRecoversFromDaemonRestart drives a real daemon
// subprocess over WatchState: connect+push, detect a kill, and recover on
// restart. Mirrors the deleted RemotePoller restart test for the stream path.
func TestStreamingPoller_LiveRecoversFromDaemonRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess-based")
	}

	bin := buildDaemonBinary(t)
	stateDir := shortTempDir(t)
	env := append(os.Environ(), "XDG_STATE_HOME="+stateDir, "XDG_RUNTIME_DIR="+stateDir)
	sockPath := filepath.Join(stateDir, "pa-monitor", "daemon.sock")

	start := func() *exec.Cmd {
		cmd := exec.Command(bin, "daemon", "--no-poller", "--tick-seconds=1")
		cmd.Env = env
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		waitForFile(t, sockPath)
		return cmd
	}

	cmd := start()
	p := NewStreamingPollerForSocket(sockPath, 200*time.Millisecond)
	defer func() { _ = p.Close() }()

	// Stream connects and pushes -> Snapshot returns a tree, online.
	eventually(t, 5*time.Second, func() bool {
		tree, _, err := p.Snapshot(context.Background())
		return err == nil && tree != nil && !p.IsOffline()
	}, "poller never received a live push")

	// Crash the daemon (SIGKILL, not SIGTERM: a graceful stop would block in
	// GracefulStop waiting for our held WatchState stream to close — the daemon
	// handlers only watch stream.Context(), which GracefulStop doesn't cancel).
	// A crash is also the failure mode this recovery path exists for.
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	eventually(t, 5*time.Second, func() bool { return p.IsOffline() },
		"poller did not go offline after daemon killed")

	// Restart -> poller recovers on redial.
	cmd2 := start()
	defer func() { _ = cmd2.Process.Kill(); _ = cmd2.Wait() }()
	eventually(t, 10*time.Second, func() bool { return !p.IsOffline() },
		"poller did not recover after daemon restart")
}

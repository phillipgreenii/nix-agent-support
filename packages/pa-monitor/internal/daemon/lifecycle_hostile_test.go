//go:build hostile

// Sandbox-hostile lifecycle tests (bead pg2-ymi3l). These build the pa-monitor
// binary and spawn it as a real daemon subprocess, then drive it with real OS
// signals (SIGKILL/SIGTERM) and process-race timing. They are split out of
// lifecycle_test.go behind the `hostile` build tag so the `pa-monitor-go-tests`
// flake check (which runs plain `go test ./...`, no tag) exercises only the
// sandbox-safe suite. Run the full set locally with `go test -tags hostile ./...`.
// The shared helpers shortTempDir/waitForFile live in lifecycle_test.go (untagged)
// and remain available to these tests when the tag is on.
package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRun_SIGKILLRecoveredOnNextStart(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess-based; skipped in -short mode")
	}

	bin := buildTestBinary(t)
	stateDir := shortTempDir(t)

	cmd := exec.Command(bin, "daemon")
	cmd.Env = append(
		os.Environ(),
		"XDG_STATE_HOME="+stateDir,
		"XDG_RUNTIME_DIR="+stateDir,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	sockPath := filepath.Join(stateDir, "pa-monitor", "daemon.sock")
	waitForFile(t, sockPath)

	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	cmd2 := exec.Command(bin, "daemon")
	cmd2.Env = cmd.Env
	if err := cmd2.Start(); err != nil {
		t.Fatalf("second start failed: %v", err)
	}
	waitForFile(t, sockPath)
	_ = cmd2.Process.Signal(syscall.SIGTERM)
	_ = cmd2.Wait()
}

func buildTestBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "pa-monitor")
	out, err := exec.Command(
		"go", "build",
		"-o", bin,
		"github.com/phillipgreenii/pa-monitor/cmd/pa-monitor",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("build test binary: %v\n%s", err, out)
	}
	return bin
}

func TestRun_ConcurrentStartExactlyOneWins(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess-based")
	}

	bin := buildTestBinary(t)
	stateDir := shortTempDir(t)
	env := append(
		os.Environ(),
		"XDG_STATE_HOME="+stateDir,
		"XDG_RUNTIME_DIR="+stateDir,
	)

	const N = 5
	type result struct {
		err error
		out []byte
	}
	results := make(chan result, N)

	for i := 0; i < N; i++ {
		go func() {
			cmd := exec.Command(bin, "daemon")
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			results <- result{err: err, out: out}
		}()
	}

	time.Sleep(200 * time.Millisecond)

	losers := 0
	for i := 0; i < N-1; i++ {
		select {
		case r := <-results:
			if r.err == nil {
				t.Errorf("expected non-zero exit from a loser, got success: %s", r.out)
			}
			losers++
		case <-time.After(2 * time.Second):
			t.Fatal("losers did not exit within 2s")
		}
	}
	if losers != N-1 {
		t.Errorf("losers = %d, want %d", losers, N-1)
	}

	pidData, err := os.ReadFile(filepath.Join(stateDir, "pa-monitor", "daemon.pid"))
	if err != nil {
		t.Fatal(err)
	}
	winnerPID, _ := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if winnerPID > 0 {
		_ = syscall.Kill(winnerPID, syscall.SIGTERM)
	}
	<-results
}

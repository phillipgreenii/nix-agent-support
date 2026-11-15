//go:build integration

package scriptout

import (
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestInvokeTargetCancelKillsBackendProcessGroup is the real-process proof
// that a cancelled umbrella call ends the backend's whole process group within
// the grace, not just the direct child: the backend shell starts a grandchild
// sleeper (the stand-in for the backend's gh/bd/pjira child), the caller's
// context is cancelled, and both must be gone shortly after.
func TestInvokeTargetCancelKillsBackendProcessGroup(t *testing.T) {
	prev := execRunner
	execRunner = nil
	t.Cleanup(func() { execRunner = prev })

	pidFile := t.TempDir() + "/grandchild.pid"
	script := `sleep 300 & echo $! > ` + pidFile + `; cat >/dev/null; wait`
	tgt := Target{Name: "real-sh", Command: []string{"sh", "-c", script}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := InvokeTarget(ctx, tgt, OpAuthStatus, nil, nil)
		done <- err
	}()

	var pid int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				pid = n
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("backend never recorded its grandchild pid")
	}

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "canceled by caller") {
			t.Fatalf("err = %v, want a canceled-by-caller error", err)
		}
	case <-time.After(DefaultTermGrace + DefaultWaitDelay + 5*time.Second):
		t.Fatal("cancelled call did not return within the grace")
	}
	if got := time.Since(start); got > DefaultTermGrace+DefaultWaitDelay+2*time.Second {
		t.Fatalf("cancelled call took %v", got)
	}

	// The grandchild must be gone (signal 0 probes existence).
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("grandchild %d outlived the cancelled call", pid)
}

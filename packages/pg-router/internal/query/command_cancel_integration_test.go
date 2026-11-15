//go:build integration && unix

package query

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Cancelling a query signals its whole process group: a non-exec wrapper
// (/bin/sh waiting on a background sleep) leaves no orphan.
func TestOSCommander_cancelKillsTheWholeProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "sleep.pid")
	argv := []string{"/bin/sh", "-c", "sleep 600 & echo $! > " + pidFile + "; wait"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := OSCommander{}.RunEnv(ctx, argv, nil)
		done <- err
	}()
	var sleepPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				sleepPID = n
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if sleepPID == 0 {
		t.Fatal("background sleep never started")
	}
	// The wrapper shell is the group leader; the sleep joined its group.
	pgid, err := syscall.Getpgid(sleepPID)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled query returned no error")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("cancelled query did not return")
	}
	for i := 0; ; i++ {
		if err := syscall.Kill(-pgid, 0); err == syscall.ESRCH {
			break
		}
		if i > 300 {
			t.Fatalf("process group %d still alive after cancel (orphaned payload)", pgid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

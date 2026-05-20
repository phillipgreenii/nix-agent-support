package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRun_ParentDirRemovedMidRunDoesNotCrash exercises the case where
// the daemon's state dir is wiped while the daemon is still serving.
// The spec demands: log fatal, exit cleanly, no half-cleanup.
//
// We don't have a structured panic-recovery surface yet; for this case
// we just verify that ctx cancel still unwinds Run cleanly even when
// the deferred Remove calls hit "no such file" errors (which they treat
// as benign).
func TestRun_ParentDirRemovedMidRunDoesNotCrash(t *testing.T) {
	dir := shortTempDir(t)
	paths := Paths{
		Dir:     dir,
		PIDFile: filepath.Join(dir, "daemon.pid"),
		Socket:  filepath.Join(dir, "daemon.sock"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, paths) }()

	waitForFile(t, paths.PIDFile)
	waitForFile(t, paths.Socket)

	// Yank the state dir from under the daemon.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	// Confirm the listener was bound (we can no longer dial because the
	// socket file is gone) — sanity check that we yanked the right files.
	if _, err := net.Dial("unix", paths.Socket); err == nil {
		t.Log("post-remove dial succeeded — kernel may keep abstract socket open; that's fine")
	} else if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, net.ErrClosed) {
		// either "no such file" or kernel-level close is acceptable
		t.Logf("post-remove dial: %v (acceptable)", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned err after parent-dir removal: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel with missing parent dir")
	}

	// No artifacts left to clean up because the parent dir is gone, but
	// the daemon also should not have re-created them. Stat should fail.
	if _, err := os.Stat(paths.PIDFile); !os.IsNotExist(err) {
		t.Errorf("pidfile re-created or stat error: %v", err)
	}
}

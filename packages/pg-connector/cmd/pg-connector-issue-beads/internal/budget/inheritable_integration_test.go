//go:build integration

// Real-process test of Slot.Inheritable (a plain exec child holds the flock),
// so it is integration tier (run with -tags integration).

package budget

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

// startPlainChild starts `sleep` with a plain exec.Cmd: no ExtraFiles, so only
// a descriptor whose close-on-exec flag is clear can reach it.
func startPlainChild(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

func probeDenied(t *testing.T, dir string) bool {
	t.Helper()
	b := newBudget(t, Config{MaxConcurrent: 1, StateDir: dir, WaitBound: 100 * time.Millisecond})
	s, err := b.Acquire(context.Background(), false)
	if err == nil {
		s.Release()
		return false
	}
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("probe: %v", err)
	}
	return true
}

// TestInheritableChildKeepsSlotAfterRelease: after Inheritable, a child started
// WITHOUT ExtraFiles holds the flock past the backend's own Release, and the
// slot frees when that child dies. This is the guarantee the bd runner relies
// on now that x/cmdrun.Call has no ExtraFiles.
func TestInheritableChildKeepsSlotAfterRelease(t *testing.T) {
	dir := t.TempDir()
	b := newBudget(t, Config{MaxConcurrent: 1, StateDir: dir})
	s := mustAcquire(t, b, false)
	if err := s.Inheritable(); err != nil {
		t.Fatalf("Inheritable: %v", err)
	}
	child := startPlainChild(t)
	s.Release()
	if !probeDenied(t, dir) {
		t.Fatal("slot was free while the inheriting child still ran")
	}
	_ = child.Process.Kill()
	_ = child.Wait()
	if probeDenied(t, dir) {
		t.Fatal("slot still held after the child exited")
	}
}

// TestWithoutInheritableReleaseFreesSlot is the negative control: the same
// flow minus Inheritable frees the slot at Release, so the previous test would
// fail without the clear.
func TestWithoutInheritableReleaseFreesSlot(t *testing.T) {
	dir := t.TempDir()
	b := newBudget(t, Config{MaxConcurrent: 1, StateDir: dir})
	s := mustAcquire(t, b, false)
	_ = startPlainChild(t)
	s.Release()
	if probeDenied(t, dir) {
		t.Fatal("slot held by a child that should not have inherited it")
	}
}

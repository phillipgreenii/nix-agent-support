package budget

import (
	"context"
	"testing"

	"golang.org/x/sys/unix"
)

func cloexec(t *testing.T, s *Slot) bool {
	t.Helper()
	rc, err := s.File().SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flags int
	var ferr error
	if err := rc.Control(func(fd uintptr) { flags, ferr = unix.FcntlInt(fd, unix.F_GETFD, 0) }); err != nil || ferr != nil {
		t.Fatalf("F_GETFD: %v %v", err, ferr)
	}
	return flags&unix.FD_CLOEXEC != 0
}

// TestInheritableClearsCloseOnExec: a fresh slot is close-on-exec (no child
// sees it); Inheritable clears the flag so a plain exec'd child inherits it.
func TestInheritableClearsCloseOnExec(t *testing.T) {
	b := newBudget(t, Config{MaxConcurrent: 1})
	s := mustAcquire(t, b, false)
	defer s.Release()
	if !cloexec(t, s) {
		t.Fatal("a fresh slot descriptor is not close-on-exec")
	}
	if err := s.Inheritable(); err != nil {
		t.Fatalf("Inheritable: %v", err)
	}
	if cloexec(t, s) {
		t.Fatal("Inheritable left close-on-exec set")
	}
}

func TestInheritableNoOpOnFailOpenAndReleased(t *testing.T) {
	b := newBudget(t, Config{MaxConcurrent: 1, StateDir: "/proc/nonexistent/x\x00bad"})
	s, err := b.Acquire(context.Background(), false)
	if err != nil || !s.FailedOpen() {
		t.Skipf("could not build a fail-open slot: %v", err)
	}
	if err := s.Inheritable(); err != nil {
		t.Errorf("fail-open Inheritable: %v", err)
	}
	b2 := newBudget(t, Config{MaxConcurrent: 1})
	r := mustAcquire(t, b2, false)
	r.Release()
	if err := r.Inheritable(); err != nil {
		t.Errorf("released Inheritable: %v", err)
	}
}

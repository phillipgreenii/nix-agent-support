package lease

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Re-exec helper: a test binary started with LEASE_TEST_HELPER=sleep just
// sleeps, so tests can spawn a child whose executable has a chosen name.
func TestMain(m *testing.M) {
	if os.Getenv("LEASE_TEST_HELPER") == "sleep" {
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func newDir(t *testing.T) *Dir {
	t.Helper()
	d, err := EnsureDir(filepath.Join(t.TempDir(), "state", DirName))
	if err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	return d
}

// signalPID signals only a pid the test spawned and refuses pid 1 or lower
// (a non-positive or tiny pid would signal a process group or init).
func refusePID(pid int) error {
	if pid <= 1 {
		return fmt.Errorf("refusing to signal pid %d", pid)
	}
	return nil
}

func signalPID(t *testing.T, pid int, sig syscall.Signal) {
	t.Helper()
	if err := refusePID(pid); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, sig); err != nil {
		t.Fatalf("kill(%d, %v): %v", pid, sig, err)
	}
}

// fakeProber answers from a table, for portable liveness tests.
type fakeProber map[int]ProcState

func (f fakeProber) Probe(pid int) ProcState { return f[pid] }

func timePtr(t time.Time) *time.Time { return &t }

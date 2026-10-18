package lease

import (
	"os"
	"path/filepath"
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

// fakeProber answers from a table, for portable liveness tests.
type fakeProber map[int]ProcState

func (f fakeProber) Probe(pid int) ProcState { return f[pid] }

func timePtr(t time.Time) *time.Time { return &t }

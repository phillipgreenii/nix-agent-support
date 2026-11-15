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
	// A race-built child sleeps atexit_sleep_ms (default 1000) before it exits;
	// the setting is read at start-up, so this reaches only the children this
	// binary starts. No assertion depends on it.
	if os.Getenv("GORACE") == "" {
		_ = os.Setenv("GORACE", "atexit_sleep_ms=0")
	}
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

package rpcclient

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// shortTempDir returns a /tmp-based dir, dodging macOS's 104-byte unix socket
// path limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "pamcli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// buildDaemonBinary compiles the pa-monitor binary into a temp dir so tests can
// subprocess it.
func buildDaemonBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "pa-monitor")
	out, err := exec.Command(
		"go", "build", "-o", bin,
		"github.com/phillipgreenii/pa-monitor/cmd/pa-monitor",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("file did not appear: %s", path)
}

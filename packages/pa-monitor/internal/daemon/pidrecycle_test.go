package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// TestAcquirePIDFile_RecycledPidIsReclaimed proves that even if the
// pidfile contains a pid that is dead (or was reused by an unrelated
// process), a new daemon can still acquire the lock — the lock itself
// is the source of truth, not the file's contents. The kernel releases
// flocks on process death, so the test is structurally the same as the
// stale-pid case.
func TestAcquirePIDFile_RecycledPidIsReclaimed(t *testing.T) {
	dir := shortTempDir(t)
	paths := Paths{
		Dir:     dir,
		PIDFile: filepath.Join(dir, "daemon.pid"),
		Socket:  filepath.Join(dir, "daemon.sock"),
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Start a real short-lived child whose pid we will write into the
	// pidfile, then kill it before AcquirePIDFile runs.
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := os.WriteFile(paths.PIDFile, []byte(strconv.Itoa(pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	lock, err := AcquirePIDFile(paths)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	lock.Release()
}

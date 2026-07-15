package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquirePIDFile_WritesFileAndLocks(t *testing.T) {
	dir := shortTempDir(t)
	paths := Paths{
		Dir:     dir,
		PIDFile: filepath.Join(dir, "daemon.pid"),
		Socket:  filepath.Join(dir, "daemon.sock"),
	}

	lock, err := AcquirePIDFile(paths)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lock.Release()

	data, err := os.ReadFile(paths.PIDFile)
	if err != nil {
		t.Fatalf("read pidfile: %v", err)
	}
	if len(data) == 0 {
		t.Error("pidfile is empty")
	}
}

func TestAcquirePIDFile_SecondAcquireFails(t *testing.T) {
	dir := shortTempDir(t)
	paths := Paths{
		Dir:     dir,
		PIDFile: filepath.Join(dir, "daemon.pid"),
		Socket:  filepath.Join(dir, "daemon.sock"),
	}

	first, err := AcquirePIDFile(paths)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer first.Release()

	if _, err := AcquirePIDFile(paths); err == nil {
		t.Fatal("expected second acquire to fail with lock contention")
	}
}

func TestAcquirePIDFile_ReleaseRemovesFile(t *testing.T) {
	dir := shortTempDir(t)
	paths := Paths{
		Dir:     dir,
		PIDFile: filepath.Join(dir, "daemon.pid"),
		Socket:  filepath.Join(dir, "daemon.sock"),
	}

	lock, err := AcquirePIDFile(paths)
	if err != nil {
		t.Fatal(err)
	}
	lock.Release()

	if _, err := os.Stat(paths.PIDFile); !os.IsNotExist(err) {
		t.Errorf("pidfile still exists after Release: stat err=%v", err)
	}
}

func TestAcquirePIDFile_StaleFileFromDeadPidIsReclaimed(t *testing.T) {
	dir := shortTempDir(t)
	paths := Paths{
		Dir:     dir,
		PIDFile: filepath.Join(dir, "daemon.pid"),
		Socket:  filepath.Join(dir, "daemon.sock"),
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Stale pidfile with a pid that's guaranteed not alive.
	if err := os.WriteFile(paths.PIDFile, []byte("2147483646"), 0o600); err != nil {
		t.Fatal(err)
	}

	lock, err := AcquirePIDFile(paths)
	if err != nil {
		t.Fatalf("acquire should reclaim stale pidfile, got err: %v", err)
	}
	defer lock.Release()
}

func TestBindSocket_CreatesAndChmods(t *testing.T) {
	dir := shortTempDir(t)
	paths := Paths{
		Dir:     dir,
		PIDFile: filepath.Join(dir, "daemon.pid"),
		Socket:  filepath.Join(dir, "daemon.sock"),
	}

	l, err := BindSocket(paths)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	defer func() { _ = l.Close() }()

	info, err := os.Stat(paths.Socket)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("socket perms = %v, want 0600", info.Mode().Perm())
	}
}

func TestBindSocket_RemovesStaleSocket(t *testing.T) {
	dir := shortTempDir(t)
	paths := Paths{
		Dir:     dir,
		PIDFile: filepath.Join(dir, "daemon.pid"),
		Socket:  filepath.Join(dir, "daemon.sock"),
	}
	if err := os.WriteFile(paths.Socket, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}

	l, err := BindSocket(paths)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	defer func() { _ = l.Close() }()
}

func TestRun_CleanShutdownRemovesArtifacts(t *testing.T) {
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

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned err: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}

	if _, err := os.Stat(paths.PIDFile); !os.IsNotExist(err) {
		t.Errorf("pidfile not cleaned up: stat err=%v", err)
	}
	if _, err := os.Stat(paths.Socket); !os.IsNotExist(err) {
		t.Errorf("socket not cleaned up: stat err=%v", err)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("file did not appear within timeout: %s", path)
}

// shortTempDir returns a temp directory under /tmp instead of the default
// t.TempDir() which lives under /var/folders/... on macOS. Unix domain
// sockets cap path length at ~104 bytes on macOS / 108 on Linux, and the
// long default temp path blows that limit when test names get long.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pam-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

package eventqueue

import (
	"bufio"
	"bytes"
	"errors"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Tests for the write-ahead log's second-opener protection (lock.go, bead
// pg2-maxn1): the exclusive flock on <log>.lock.

func lockTestStore(t *testing.T) (*FileStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	return fs, path
}

func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A second opener of a held log fails fast with *ErrLogLocked and touches
// nothing: the log is byte-identical, the holder's in-flight compaction temp file
// is not removed as "stale", and the holder keeps working (appends, compaction).
func TestFileStoreSecondOpenerRefused(t *testing.T) {
	fs, path := lockTestStore(t)
	for _, r := range randomLog(rand.New(rand.NewSource(1)), 40) {
		if err := fs.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	// A temp file standing in for the holder's compaction in flight.
	if err := os.WriteFile(compactTempPath(path), []byte("in-flight"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := fileBytes(t, path)

	second, err := NewFileStore(path)
	var locked *ErrLogLocked
	if !errors.As(err, &locked) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("second NewFileStore err = %v, want *ErrLogLocked", err)
	}
	if second != nil {
		t.Fatal("a refused open returned a store")
	}
	if locked.Path != path || locked.LockPath != lockPath(path) {
		t.Fatalf("ErrLogLocked = %+v, want path %s", locked, path)
	}
	if !bytes.Equal(fileBytes(t, path), before) {
		t.Fatal("a refused second opener changed the log")
	}
	if got := fileBytes(t, compactTempPath(path)); string(got) != "in-flight" {
		t.Fatalf("a refused second opener removed or rewrote the holder's temp file: %q", got)
	}
	_ = os.Remove(compactTempPath(path))

	// The holder is unharmed: it can still append and compact.
	if err := fs.Append(Record{Op: opEnqueue, EventID: "after", Type: "T"}); err != nil {
		t.Fatalf("holder append after a refused second opener: %v", err)
	}
	if _, err := fs.Compact(); err != nil {
		t.Fatalf("holder compact after a refused second opener: %v", err)
	}
}

// The lock is released by Close, and survives a compaction (which renames a new
// inode over the log — the reason the lock is a separate file).
func TestFileStoreLockLifecycle(t *testing.T) {
	fs, path := lockTestStore(t)
	if err := fs.Append(Record{Op: opEnqueue, EventID: "a", Type: "T"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Compact(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(path); !errors.As(err, new(*ErrLogLocked)) {
		t.Fatalf("after a compaction the lock must still be held; err = %v", err)
	}
	if err := fs.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reopen after Close: %v", err)
	}
	_ = reopened.Close()
}

// A failed open (here: the log path is a directory) must not leave the lock held.
func TestFileStoreFailedOpenReleasesLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.jsonl")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(path); err == nil {
		t.Fatal("opening a directory as the log should fail")
	}
	if held, err := LogLocked(path); err != nil || held {
		t.Fatalf("LogLocked after a failed open = %v, %v; want false, nil", held, err)
	}
}

// LogLocked is a read-only probe: it creates nothing, reports a held lock, and
// never leaves a lock behind.
func TestLogLockedProbe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	held, err := LogLocked(path)
	if err != nil || held {
		t.Fatalf("absent lock file: LogLocked = %v, %v; want false, nil", held, err)
	}
	if _, err := os.Stat(lockPath(path)); !os.IsNotExist(err) {
		t.Fatalf("LogLocked created the lock file (stat err = %v)", err)
	}
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if held, err := LogLocked(path); err != nil || !held {
		t.Fatalf("held lock: LogLocked = %v, %v; want true, nil", held, err)
	}
	_ = fs.Close()
	if held, err := LogLocked(path); err != nil || held {
		t.Fatalf("released lock: LogLocked = %v, %v; want false, nil", held, err)
	}
	// The probe must not have kept the lock: a real opener still gets it.
	again, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("open after a probe: %v", err)
	}
	_ = again.Close()
}

const holdLogEnv = "PG2_MAXN1_HOLD_LOG"

// TestHelperHoldLog is not a test: it is the child process of
// TestFileStoreLockReleasedWhenHolderDies. It takes the log, announces it, and
// blocks until its stdin closes (or it is killed).
func TestHelperHoldLog(t *testing.T) {
	path := os.Getenv(holdLogEnv)
	if path == "" {
		t.Skip("helper process only")
	}
	fs, err := NewFileStore(path)
	if err != nil {
		os.Stdout.WriteString("error: " + err.Error() + "\n")
		os.Exit(3)
	}
	defer func() { _ = fs.Close() }()
	os.Stdout.WriteString("locked\n")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

// The kernel drops the flock when its holder dies, so a crashed daemon never
// leaves a stale lock behind that would wedge the next start.
func TestFileStoreLockReleasedWhenHolderDies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldLog$")
	cmd.Env = append(os.Environ(), holdLogEnv+"="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := false
	reap := func() {
		if !reaped {
			reaped = true
			_ = stdin.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}
	defer reap()

	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(stdout).ReadString('\n')
		line <- s
	}()
	select {
	case s := <-line:
		if s != "locked\n" {
			t.Fatalf("helper said %q, want \"locked\"", s)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("helper process never took the lock")
	}
	if _, err := NewFileStore(path); !errors.As(err, new(*ErrLogLocked)) {
		t.Fatalf("a live holder in another process must refuse us; err = %v", err)
	}
	if held, err := LogLocked(path); err != nil || !held {
		t.Fatalf("LogLocked with a live foreign holder = %v, %v; want true, nil", held, err)
	}
	reap() // SIGKILL: no deferred Close runs in the child
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("the lock outlived its dead holder: %v", err)
	}
	_ = fs.Close()
}

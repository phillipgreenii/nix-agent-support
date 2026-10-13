package sidecar

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/posted"
)

// The tests below take the per-PR lock from a second PROCESS: this test binary
// re-executed as a holder. flock(2) already excludes two open file descriptions
// inside one process, which is all the copied lock tests exercise; the sidecar
// lock is the one the OLD binary takes on the same file, so only another
// process shows that the Locker Open returns excludes across processes.
const (
	holderHomeEnv  = "SIDECAR_TEST_HOLDER_HOME"
	holderReadyEnv = "SIDECAR_TEST_HOLDER_READY"

	holderOwner = "owner"
	holderRepo  = "repo"
	holderPR    = 7
)

// TestHelperHoldsPRLock is the child of the cross-process tests; it does nothing
// in a normal run. As a child it opens the sidecar from the state home it was
// given, takes the lock of one PR, announces that by creating the ready file,
// and holds the lock until the parent closes its stdin.
func TestHelperHoldsPRLock(t *testing.T) {
	home, ready := os.Getenv(holderHomeEnv), os.Getenv(holderReadyEnv)
	if home == "" || ready == "" {
		t.Skip("helper process only")
	}
	_, locker, err := Open(cfgWith(home), noEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "holder: open:", err)
		os.Exit(2)
	}
	lock, err := locker.Acquire(holderOwner, holderRepo, holderPR)
	if err != nil {
		fmt.Fprintln(os.Stderr, "holder: acquire:", err)
		os.Exit(3)
	}
	if err := os.WriteFile(ready, []byte("locked"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "holder: ready file:", err)
		os.Exit(4)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	if err := lock.Release(); err != nil {
		fmt.Fprintln(os.Stderr, "holder: release:", err)
		os.Exit(5)
	}
	os.Exit(0)
}

// holder is a running child process that holds the lock of one PR.
type holder struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stderr  *bytes.Buffer
	done    chan struct{} // closed once the child has exited
	err     error         // the child's Wait result, valid once done is closed
	stopped bool
}

// startHolder starts a child that holds the lock of PR holderPR under home and
// returns once the child says it holds it. It always stops the child at the end
// of the test.
func startHolder(t *testing.T, home string) *holder {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldsPRLock$")
	cmd.Env = append(os.Environ(), holderHomeEnv+"="+home, holderReadyEnv+"="+ready)
	h := &holder{cmd: cmd, stderr: &bytes.Buffer{}, done: make(chan struct{})}
	cmd.Stderr = h.stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	h.stdin = stdin
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the holder process: %v", err)
	}
	go func() {
		h.err = cmd.Wait()
		close(h.done)
	}()
	t.Cleanup(func() { h.stop(t) })

	deadline := time.After(30 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			return h
		}
		select {
		case <-h.done:
			t.Fatalf("the holder process exited before it took the lock: %v\n%s", h.err, h.stderr)
		case <-deadline:
			_ = cmd.Process.Kill()
			<-h.done
			t.Fatalf("the holder process did not take the lock in time\n%s", h.stderr)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// stop closes the holder's stdin, which makes it release the lock and exit, and
// waits for it. A holder that does not exit is killed and reported. It is safe
// to call more than once.
func (h *holder) stop(t *testing.T) {
	t.Helper()
	if h.stopped {
		return
	}
	h.stopped = true
	_ = h.stdin.Close()
	select {
	case <-h.done:
	case <-time.After(30 * time.Second):
		_ = h.cmd.Process.Kill()
		<-h.done
		t.Errorf("the holder process did not exit once its stdin closed\n%s", h.stderr)
		return
	}
	if h.err != nil {
		t.Errorf("the holder process failed: %v\n%s", h.err, h.stderr)
	}
}

func TestAnotherProcessHoldingTheLockExcludesTheDaemon(t *testing.T) {
	home := t.TempDir()
	_, locker, err := Open(cfgWith(home), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	h := startHolder(t, home)

	// The other process holds the old backend's lock file, at its old name.
	lockFile := filepath.Join(home, "locks", fmt.Sprintf("%s__%s__%d.lock", holderOwner, holderRepo, holderPR))
	if _, err := os.Stat(lockFile); err != nil {
		t.Fatalf("the lock file must be <home>/locks/<owner>__<repo>__<n>.lock: %v", err)
	}

	locker.Wait, locker.Poll = 200*time.Millisecond, 10*time.Millisecond
	start := time.Now()
	k, err := locker.Acquire(holderOwner, holderRepo, holderPR)
	if err == nil {
		_ = k.Release()
		t.Fatal("the lock was granted while another process held it")
	}
	if !errors.Is(err, posted.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Errorf("gave up after %v, want at least the 200ms wait", d)
	}

	// Another PR is a different lock: writes to different PRs run in parallel.
	other, err := locker.Acquire(holderOwner, holderRepo, holderPR+1)
	if err != nil {
		t.Fatalf("a different PR must not contend with the other process: %v", err)
	}
	_ = other.Release()

	// A holder that exits frees the lock (the kernel drops it with the process).
	h.stop(t)
	k, err = locker.Acquire(holderOwner, holderRepo, holderPR)
	if err != nil {
		t.Fatalf("the lock must be free once the other process is gone: %v", err)
	}
	_ = k.Release()
}

func TestADaemonWaitingOnAnotherProcessProceedsWhenItReleases(t *testing.T) {
	home := t.TempDir()
	_, locker, err := Open(cfgWith(home), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	h := startHolder(t, home)

	locker.Wait, locker.Poll = 30*time.Second, 5*time.Millisecond
	got := make(chan error, 1)
	go func() {
		k, err := locker.Acquire(holderOwner, holderRepo, holderPR)
		if err == nil {
			err = k.Release()
		}
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("Acquire returned (%v) while another process still held the lock", err)
	case <-time.After(200 * time.Millisecond):
	}
	h.stop(t)
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("the waiting Acquire must succeed once the other process released: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the waiting Acquire did not proceed after the other process released the lock")
	}
}

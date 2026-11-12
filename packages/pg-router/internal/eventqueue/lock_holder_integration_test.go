//go:build integration && unix

package eventqueue

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

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
		_, _ = os.Stdout.WriteString("error: " + err.Error() + "\n")
		os.Exit(3)
	}
	defer func() { _ = fs.Close() }()
	_, _ = os.Stdout.WriteString("locked\n")
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

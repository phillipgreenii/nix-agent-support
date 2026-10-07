package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

// A dispatch outlives the daemon that spawned it across a restart (ADR 0085),
// so by the time it writes its one reply the read end of its stdout pipe is
// gone. These tests re-execute this test binary (TestMain hands control to
// runBrokenPipeHelper when brokenPipeHelperEnv is set) with such a pipe.
const brokenPipeHelperEnv = "PG_ROUTER_CCPOOL_HANDLER_TEST_BROKEN_PIPE_HELPER"

const (
	helperModeDispatch  = "dispatch"   // the real `dispatch` entry point
	helperModeBareWrite = "bare-write" // control: a reply write with no SIGPIPE handling
)

// runBrokenPipeHelper is the child half. It waits for its stdin to close (the
// parent closes the stdout read end FIRST, then stdin), so the reply write
// always happens against a broken pipe.
func runBrokenPipeHelper(mode string) int {
	switch mode {
	case helperModeDispatch:
		// Malformed JSON on stdin is answered with an error reply, the cheapest
		// path through runDispatch that still ends in a reply write.
		return run([]string{"dispatch"})
	case helperModeBareWrite:
		_, _ = io.ReadAll(os.Stdin)
		writeReply(os.Stdout, map[string]any{"ok": true})
		return 0
	}
	return 99
}

func runWithClosedStdoutPipe(t *testing.T, mode string) error {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), brokenPipeHelperEnv+"="+mode)
	cmd.Stdout = pw
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = pw.Close() // the child now holds the only write end
	_ = pr.Close() // the "daemon" is gone: nobody can read the reply
	_, _ = io.WriteString(stdin, "this is not json")
	_ = stdin.Close()
	err = cmd.Wait()
	t.Logf("helper %s: err=%v stderr=%q", mode, err, stderr.String())
	return err
}

// Control: without the handler's SIGPIPE handling, the reply write kills the
// process with SIGPIPE. This proves the closed pipe really is the failure
// mode and keeps the next test from passing vacuously.
func TestBrokenStdoutPipe_bareReplyWriteIsKilledBySIGPIPE(t *testing.T) {
	err := runWithClosedStdoutPipe(t, helperModeBareWrite)
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("err = %v, want an ExitError (the control write should die of SIGPIPE)", err)
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGPIPE {
		t.Fatalf("wait status = %v, want terminated by SIGPIPE", ee.Sys())
	}
}

// `dispatch` survives losing its reply: the write fails with EPIPE, is logged,
// and the process exits with its ordinary exit code instead of a signal.
func TestBrokenStdoutPipe_dispatchExitsNormally(t *testing.T) {
	err := runWithClosedStdoutPipe(t, helperModeDispatch)
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("err = %v, want exit status 1 (malformed request)", err)
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("no wait status: %v", ee.Sys())
	}
	if ws.Signaled() {
		t.Fatalf("dispatch was killed by %v: a lost reply must not be a signalled death", ws.Signal())
	}
	if ws.ExitStatus() != 1 {
		t.Fatalf("exit status = %d, want 1 (ExitError for the malformed request)", ws.ExitStatus())
	}
}

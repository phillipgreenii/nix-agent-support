//go:build integration

package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"github.com/phillipgreenii/pg-router/conformance"
)

// A dispatch outlives the daemon that spawned it across a restart (ADR 0085), so
// by the time it writes its one reply the read end of its stdout pipe is gone.
// These tests re-execute this test binary with such a pipe.
const (
	helperModeDispatch  = "dispatch"   // the real `dispatch` entry point
	helperModeBareWrite = "bare-write" // control: a reply write with no SIGPIPE handling
)

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
	args := []string{}
	env := adapterRunEnv + "=1"
	if mode == helperModeDispatch {
		args = []string{"dispatch"}
	} else {
		env = brokenPipeBareEnv + "=1"
	}
	cmd := exec.CommandContext(t.Context(), exe, args...)
	cmd.Env = append(os.Environ(), env)
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
// process with SIGPIPE. This proves the closed pipe really is the failure mode
// and keeps the next test from passing vacuously.
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
	if ws.ExitStatus() != conformance.ExitError {
		t.Fatalf("exit status = %d, want %d (ExitError for the malformed request)", ws.ExitStatus(), conformance.ExitError)
	}
}

// The adapter as a process, over the real wire: a dispatch declined for an
// unusable pool exits 9 with the decline body, a validate replies on stdout, and
// a request without a role is a usage error.
func TestProcess_DispatchAndValidateOverTheRealWire(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	name, cfg := workerRole("", "")
	code, stdout := runAdapterProcess(t, []string{"dispatch"}, request(defaultEvent, name, cfg), "PATH=/usr/bin")
	if code != conformance.ExitBusy {
		t.Fatalf("dispatch: exit = %d, want %d; stdout %s", code, conformance.ExitBusy, stdout)
	}
	if reason := decodeReply(t, stdout)["reason"]; reason != "pool-unreadable" {
		t.Errorf("dispatch: reason = %v, want pool-unreadable", reason)
	}
	code, stdout = runAdapterProcess(t, []string{"validate", "--skip-pool-dirs"}, request("", name, cfg))
	if code != conformance.ExitOK || len(decodeReply(t, stdout)["problems"].([]any)) != 0 {
		t.Errorf("validate: exit = %d stdout %s, want 0 and no problems", code, stdout)
	}
	code, stdout = runAdapterProcess(t, []string{"dispatch"}, request(defaultEvent, "", nil))
	if code != conformance.ExitUsage {
		t.Errorf("dispatch without a role: exit = %d stdout %s, want %d", code, stdout, conformance.ExitUsage)
	}
}

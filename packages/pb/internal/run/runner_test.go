package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCLIRunner_capturesStdoutAndExit(t *testing.T) {
	r := CLIRunner{}
	res, err := r.Run(context.Background(), "sh", []string{"-c", "printf hi; exit 0"}, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stdout != "hi" {
		t.Errorf("Stdout = %q, want %q", res.Stdout, "hi")
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
}

func TestCLIRunner_nonZeroExitReturnsError(t *testing.T) {
	r := CLIRunner{}
	res, err := r.Run(context.Background(), "sh", []string{"-c", "echo boom 1>&2; exit 3"}, Options{})
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", res.ExitCode)
	}
	if res.Stderr == "" {
		t.Error("expected stderr captured")
	}
}

func TestCLIRunner_stdinPiped(t *testing.T) {
	r := CLIRunner{}
	res, err := r.Run(context.Background(), "cat", nil, Options{Stdin: "piped"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stdout != "piped" {
		t.Errorf("Stdout = %q, want piped", res.Stdout)
	}
}

func TestFakeRunner_scriptedAndRecords(t *testing.T) {
	f := NewFakeRunner()
	f.AddResponse("bd", []string{"gate", "list"}, Result{Stdout: `{"data":[]}`}, nil)
	res, err := f.Run(context.Background(), "bd", []string{"gate", "list"}, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stdout != `{"data":[]}` {
		t.Errorf("Stdout = %q", res.Stdout)
	}
	calls := f.Calls()
	if len(calls) != 1 || calls[0].Name != "bd" {
		t.Errorf("Calls = %+v", calls)
	}
}

func TestFakeRunner_unscriptedErrors(t *testing.T) {
	f := NewFakeRunner()
	if _, err := f.Run(context.Background(), "bd", []string{"nope"}, Options{}); err == nil {
		t.Fatal("expected error for unscripted call")
	}
}

func TestCLIRunner_timeoutKillsWholeProcessGroup(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "child.pid")
	r := CLIRunner{}
	start := time.Now()
	_, err := r.Run(context.Background(), "sh",
		[]string{"-c", "sleep 300 & echo $! >" + pidfile + "; sleep 300"},
		Options{Timeout: time.Second})
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("Run took %s, want ~1s", elapsed)
	}
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	b, rerr := os.ReadFile(pidfile)
	if rerr != nil {
		t.Fatal(rerr)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	// SIGKILL is delivered to the group before Run returns, but a killed
	// process stays visible to kill(pid, 0) as a zombie until its (re)parent
	// reaps it. Under host load that reap can lag, so a single liveness check
	// right after Run is racy. Poll with a bounded deadline: a grandchild that
	// is truly alive (group not killed) never goes away and still fails.
	if !waitProcessGone(pid, 10*time.Second) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("grandchild %d survived the timeout", pid)
	}
}

// waitProcessGone polls until kill(pid, 0) reports the process no longer
// exists, or the deadline passes. It returns true when the process is gone.
func waitProcessGone(pid int, deadline time.Duration) bool {
	end := time.Now().Add(deadline)
	for {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		if time.Now().After(end) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCLIRunner_timeoutNotHitReturnsNormally(t *testing.T) {
	res, err := CLIRunner{}.Run(context.Background(), "sh", []string{"-c", "printf ok"}, Options{Timeout: 30 * time.Second})
	if err != nil || res.Stdout != "ok" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestCLIRunner_parentCancelIsNotATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := CLIRunner{}.Run(ctx, "sh", []string{"-c", "sleep 30"}, Options{Timeout: time.Minute})
	if err == nil || errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want a non-timeout error", err)
	}
}

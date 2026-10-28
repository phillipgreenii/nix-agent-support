//go:build integration

// wall_clock_e2e_test.go: the end-to-end tests that wait on real time (a
// handler or verify timeout, a held-open pipe, a signal racing a child that
// ignores TERM). Each spends 0.7 s to 3 s of wall clock, which together with
// TestDisplayGolden is about three quarters of this package's unit run, so
// they carry the `integration` tag and run under
// checks.<system>.pg-rescue-integration-tests (ADR 0111, ADR 0024). Their
// helpers (newE2E, helperArgv, waitForFile, ...) stay in the untagged files.
package runner_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
)

func TestVerifyTimeoutFailsTheAttempt(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	e := newE2E(t, "", []hd{{name: "fixer", argv: helperArgv("result", "outcome=resolved"), timeout: "3s"}}, chainOf("fixer"))
	start := time.Now()
	code, _, _ := e.wrap("fixer", helperArgv("exit", "code=4"), "--verify", helperShell("sleep", "started="+pidFile, "secs=60"))
	if time.Since(start) > 10*time.Second {
		t.Errorf("verify was not bounded: %v", time.Since(start))
	}
	a := e.onDisk().Attempts[0]
	if code != 4 || a.Outcome != contract.Failed || a.Reason != "resolved → verify timed out after 3s" {
		t.Errorf("exit=%d attempt=%+v", code, a)
	}
	pid, _ := strconv.Atoi(waitForFile(t, pidFile))
	if !eventuallyDead(pid) {
		t.Errorf("the timed-out verify %d is still alive", pid)
	}
}

func TestTheFirstSignalWins(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	e := newE2E(t, "", []hd{result("h", "declined")}, chainOf("h"))
	e.exec.Signals = make(chan os.Signal, 8)
	done := make(chan int, 1)
	go func() {
		c, _, _ := e.wrap("h", helperArgv("ignore-term", "secs=2", "started="+started))
		done <- c
	}()
	waitForFile(t, started)
	e.exec.Signals <- syscall.SIGINT
	e.exec.Signals <- syscall.SIGTERM
	if c := <-done; c != 130 || e.result().Interrupted.Signal != syscall.SIGINT {
		t.Errorf("exit=%d interruption=%+v; the first signal (INT) decides", c, e.result().Interrupted)
	}
}

func TestCommandOutputThatArrivesAfterItExitsIsStillCaptured(t *testing.T) {
	// The command leaves a child holding its stdout; like a shell pipeline the
	// wrapper keeps reading until the pipe closes.
	e := newE2E(t, "", []hd{result("d", "declined")}, chainOf("d"))
	start := time.Now()
	code, stdout, _ := e.wrap("d", helperArgv("grandchild", "hold=1", "child=late", "ms=900", "text=LATE-TEXT\n", "code=1"))
	if code != 1 || stdout != "LATE-TEXT\n" || time.Since(start) < 800*time.Millisecond {
		t.Errorf("exit=%d stdout=%q after %v", code, stdout, time.Since(start))
	}
}

func TestHandlerTimeoutIsTermThenKill(t *testing.T) {
	for _, c := range []struct {
		name  string
		argv  []string
		limit time.Duration
	}{
		{"term is enough", helperArgv("sleep", "secs=60", "started={pid}"), 5 * time.Second},
		{"ignores term: killed after the grace", helperArgv("ignore-term", "secs=60", "started={pid}"), 5 * time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "pid")
			argv := make([]string, len(c.argv))
			for i, a := range c.argv {
				argv[i] = strings.ReplaceAll(a, "{pid}", pidFile)
			}
			e := newE2E(t, "", []hd{{name: "slow", argv: argv, timeout: "300ms"}, result("next", "declined")}, chainOf("slow", "next"))
			start := time.Now()
			code, _, _ := e.wrap("slow,next", helperArgv("exit", "code=1"))
			if time.Since(start) > c.limit {
				t.Errorf("took %v", time.Since(start))
			}
			atts := e.onDisk().Attempts
			if code != 1 || len(atts) != 2 {
				t.Fatalf("exit=%d attempts=%+v", code, atts)
			}
			if a := atts[0]; a.Outcome != contract.Failed || a.Reason != "timed out after 300ms" || a.Exit != -1 {
				t.Errorf("attempt = %+v", a)
			}
			pid, _ := strconv.Atoi(waitForFile(t, pidFile))
			if !eventuallyDead(pid) {
				t.Errorf("the timed-out handler %d is still alive", pid)
			}
		})
	}
}

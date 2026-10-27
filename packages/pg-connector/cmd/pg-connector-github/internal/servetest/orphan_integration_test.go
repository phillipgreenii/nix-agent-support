//go:build integration

package servetest

import (
	"bufio"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// alive reports whether pid is a live (not yet reaped) process.
func alive(pid int) bool {
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

// A daemon the harness starts must not outlive the process that started it,
// whatever way that process ends (pg2-qupkv0). The leak: t.Cleanup(kill) never
// runs when the test binary is killed (the runner's timeout cap, SIGKILL, a
// "test timed out" panic), so the daemon was re-parented to init and ran for
// days. Here a real shell stands in for the test binary: it starts the REAL
// daemon (built the way every harness test builds it), is SIGKILLed, and the
// daemon must drain and exit by itself.
func TestDaemonExitsWhenItsParentIsKilled(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no sh on PATH: %v", err)
	}
	d := NewDaemon(t, Options{})

	parent := exec.Command(sh, "-c", `"$0" --config "$1" serve >/dev/null 2>&1 </dev/null & echo $!; wait`, d.bin, d.ConfigPath)
	parent.Dir = "/"
	parent.Env = []string{"PATH=" + DaemonPath(), "FAKE_GH_DIR=" + d.Shim.Dir}
	out, err := parent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	parentDone := make(chan struct{})
	go func() { _ = parent.Wait(); close(parentDone) }()
	t.Cleanup(func() {
		_ = parent.Process.Kill()
		<-parentDone
	})

	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("read the daemon pid from the parent: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("the parent printed %q, want the daemon pid: %v", line, err)
	}
	// The pid is of a process this test started: it is the only one ever
	// signalled, and only to clean up after a failure.
	t.Cleanup(func() {
		if alive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	d.WaitReady(t) // the daemon is up and serving, so the exit below is not vacuous
	if !alive(pid) {
		t.Fatalf("the daemon (pid %d) is not running before its parent was killed", pid)
	}

	if err := parent.Process.Kill(); err != nil { // the abnormal end: no cleanup runs
		t.Fatal(err)
	}
	<-parentDone

	deadline := time.Now().Add(20 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("the daemon (pid %d) is still running 20s after its parent was killed: an orphaned daemon", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

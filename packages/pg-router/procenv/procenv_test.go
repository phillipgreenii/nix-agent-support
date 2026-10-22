package procenv

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBaseEnv(t *testing.T) {
	src := []string{
		"HOME=/h", "USER=u", "LOGNAME=l", "TMPDIR=/t", "LANG=C", "TERM=xterm", "COLORTERM=truecolor",
		"PATH=/evil", "SECRET=x", "BEADS_ACTOR=a", "BEADS_OTHER=nope", "HOMEX=bad", "EMPTY=",
		"CLAUDECODE=1", "malformed",
	}
	got := BaseEnv(src, []string{"BEADS_ACTOR", "EMPTY", "UNSET", "PATH", "BEADS_ACTOR"})
	want := []string{
		"HOME=/h", "USER=u", "LOGNAME=l", "TMPDIR=/t", "LANG=C", "TERM=xterm", "COLORTERM=truecolor",
		"BEADS_ACTOR=a", "EMPTY=", "PATH=/usr/bin:/bin:/usr/sbin:/sbin",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if g := BaseEnv(nil, nil); !reflect.DeepEqual(g, []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}) {
		t.Fatalf("empty source: %q", g)
	}
	if g := BaseEnv([]string{"HOME=/a", "HOME=/b"}, nil); g[0] != "HOME=/b" {
		t.Fatalf("last wins: %q", g)
	}
}

func waitFile(t *testing.T, p string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(p); err == nil && strings.HasSuffix(string(b), "\n") {
			return strings.TrimSpace(string(b))
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", p)
	return ""
}

func gone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func waitGone(t *testing.T, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if gone(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s (pid/pgid %d) still alive after cancel", what, pid)
}

func cancelTest(t *testing.T, script string, grace time.Duration) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "sleep.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script, "sh", pidFile)
	Group(cmd, grace)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := cmd.Process.Pid
	sleepPid, err := strconv.Atoi(waitFile(t, pidFile))
	if err != nil {
		t.Fatal(err)
	}
	if g, err := syscall.Getpgid(sleepPid); err != nil || g != pgid {
		t.Fatalf("sleep not in the payload group: pgid=%d err=%v want %d", g, err, pgid)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Wait did not return after cancel")
	}
	waitGone(t, sleepPid, "sleep child")
	waitGone(t, -pgid, "process group")
}

func TestGroupCancelLeavesNoOrphan(t *testing.T) {
	cancelTest(t, `sleep 600 & echo $! > "$1.tmp"; mv "$1.tmp" "$1"; wait`, 2*time.Second)
}

func TestGroupCancelEscalatesToSigkill(t *testing.T) {
	// SIGTERM is ignored by the shell and (inherited) by sleep, so only the
	// group SIGKILL after grace can end the chain.
	cancelTest(t, `trap '' TERM; sleep 600 & echo $! > "$1.tmp"; mv "$1.tmp" "$1"; wait`, 200*time.Millisecond)
}

func TestGroupSetsWaitDelayOnlyWhenZero(t *testing.T) {
	c := exec.Command("/bin/true")
	Group(c, 2*time.Second)
	if c.WaitDelay != 3*time.Second {
		t.Fatalf("WaitDelay = %v, want grace+1s", c.WaitDelay)
	}
	c = exec.Command("/bin/true")
	c.WaitDelay = time.Minute
	Group(c, 2*time.Second)
	if c.WaitDelay != time.Minute {
		t.Fatalf("WaitDelay overwritten: %v", c.WaitDelay)
	}
	if !c.SysProcAttr.Setpgid {
		t.Fatal("Setpgid not set")
	}
}

//go:build unix

package bd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/failure"
)

func TestExecRunnerEnvIsExactlyTheGivenSet(t *testing.T) {
	t.Setenv("AMBIENT_LEAK", "ambient")
	res, err := ExecRunner{}.Run(context.Background(), Cmd{
		Path: "env",
		Env:  []string{"ONLY=one", "ALSO=two"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(string(res.Stdout))
	if len(got) != 2 || got[0] != "ONLY=one" || got[1] != "ALSO=two" {
		t.Fatalf("child env = %q, want exactly ONLY=one ALSO=two", got)
	}
}

func TestExecRunnerReportsNonZeroExitAsResult(t *testing.T) {
	res, err := ExecRunner{}.Run(context.Background(), Cmd{
		Path: "sh", Args: []string{"-c", "echo out; echo err >&2; exit 7"},
	})
	if err != nil {
		t.Fatalf("non-zero exit must not be a Run error: %v", err)
	}
	if res.ExitCode != 7 || strings.TrimSpace(string(res.Stdout)) != "out" || strings.TrimSpace(string(res.Stderr)) != "err" {
		t.Fatalf("result = %+v", res)
	}
}

func TestExecRunnerSpawnFailure(t *testing.T) {
	_, err := ExecRunner{}.Run(context.Background(), Cmd{Path: "/nonexistent/bd"})
	if err == nil {
		t.Fatal("expected a spawn error")
	}
}

func TestTimeoutKillsForkedSleepingGrandchild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	script := `sleep 300 & echo $! > "` + pidFile + `"; wait`
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := ExecRunner{}.Run(ctx, Cmd{Path: "sh", Args: []string{"-c", script}, Env: []string{"PATH=" + os.Getenv("PATH")}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("Run blocked for %v after the deadline", elapsed)
	}
	raw, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatalf("grandchild pid file: %v", rerr)
	}
	pid, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if perr != nil || pid <= 1 {
		t.Fatalf("bad pid %q", raw)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if kerr := syscall.Kill(pid, 0); kerr != nil {
			return // gone
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("grandchild %d survived the timeout", pid)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestClientTimeoutWithRealRunnerClassifiesAsTimeout(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(t.TempDir(), "slow-bd")
	mustWrite(t, stub, "#!/bin/sh\nsleep 300\n")
	if err := os.Chmod(stub, 0o700); err != nil {
		t.Fatal(err)
	}
	c := NewClient(ClientConfig{BDPath: stub, BeadsDir: dir, Home: "/tmp", ChildPath: os.Getenv("PATH"), Timeout: 200 * time.Millisecond})
	start := time.Now()
	_, err := c.List(context.Background(), ListOpts{})
	if failure.ReasonOf(err) != failure.Timeout {
		t.Fatalf("err = %v, want timeout", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("timeout took %v", time.Since(start))
	}
}

// The kill primitive is injected rather than provoked for real. A real
// "already gone" group cannot be made deterministic: once the child is reaped
// its pid/pgid is free for the kernel to recycle, and under host load the
// window between reap and Cancel widens enough for a recycled pgid to turn the
// expected ESRCH into success or EPERM (pg2-j4f8z).
func stubKill(t *testing.T, fn func(pid int, sig syscall.Signal) error) {
	t.Helper()
	orig := killProcess
	killProcess = fn
	t.Cleanup(func() { killProcess = orig })
}

func startedCmd(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "true")
	configureProcessGroup(cmd)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func TestCancelOnAnAlreadyGoneProcessGroupIsNotAnError(t *testing.T) {
	cmd := startedCmd(t)
	var gotPid int
	var gotSig syscall.Signal
	stubKill(t, func(pid int, sig syscall.Signal) error {
		gotPid, gotSig = pid, sig
		return syscall.ESRCH
	})
	if err := cmd.Cancel(); err != nil {
		t.Fatalf("Cancel on a gone group = %v, want nil", err)
	}
	if gotPid != -cmd.Process.Pid || gotSig != syscall.SIGKILL {
		t.Fatalf("kill(%d, %v), want kill(%d, SIGKILL): the whole group", gotPid, gotSig, -cmd.Process.Pid)
	}
}

func TestCancelReportsKillErrorsOtherThanAlreadyGone(t *testing.T) {
	cmd := startedCmd(t)
	stubKill(t, func(int, syscall.Signal) error { return syscall.EPERM })
	if err := cmd.Cancel(); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("Cancel = %v, want EPERM propagated", err)
	}
}

func TestCancelBeforeStartSignalsNothing(t *testing.T) {
	stubKill(t, func(pid int, _ syscall.Signal) error {
		t.Errorf("kill(%d) called for a command that never started", pid)
		return nil
	})
	c := exec.CommandContext(context.Background(), "true")
	configureProcessGroup(c)
	if err := c.Cancel(); err != nil {
		t.Fatalf("Cancel before start = %v", err)
	}
}

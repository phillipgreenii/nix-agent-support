//go:build integration

package proc

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
)

// These tests run the real process table against real child processes (V-3,
// V-4: integration tier). On darwin the exit watch is a kqueue NOTE_EXIT; the
// start time and the environment come from sysctl, not from ps.

func child(t *testing.T, env ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "sleep", "60")
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

func TestRealExitWatchSeesASigkilledChildAndTheRegistryFileStillThere(t *testing.T) {
	r := NewReal()
	cmd := child(t)
	reg := filepath.Join(t.TempDir(), strconv.Itoa(cmd.Process.Pid)+".json")
	if err := os.WriteFile(reg, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	type obs struct {
		at      time.Time
		present bool
	}
	got := make(chan obs, 1)
	if _, err := r.Watch(cmd.Process.Pid, func(at time.Time) {
		_, err := os.Lstat(reg) // the presence check: one lstat right after the event
		got <- obs{at, err == nil}
	}); err != nil {
		t.Fatal(err)
	}
	killed := time.Now()
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case o := <-got:
		if !o.present {
			t.Fatal("the registry file was reported absent: a SIGKILLed Process leaves it behind")
		}
		if d := o.at.Sub(killed); d < 0 || d > 3*time.Second {
			t.Fatalf("the exit was observed %v after the kill", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no exit event for a SIGKILLed child")
	}
	_ = cmd.Wait()
}

func TestRealExitWatchSeesACleanExitWithTheRegistryFileDeletedFirst(t *testing.T) {
	r := NewReal()
	cmd := child(t)
	reg := filepath.Join(t.TempDir(), strconv.Itoa(cmd.Process.Pid)+".json")
	if err := os.WriteFile(reg, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	present := make(chan bool, 1)
	if _, err := r.Watch(cmd.Process.Pid, func(time.Time) { _, err := os.Lstat(reg); present <- err == nil }); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(reg)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-present:
		if p {
			t.Fatal("the registry file was present at a clean exit")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no exit event for a SIGTERMed child")
	}
	_ = cmd.Wait()
}

func TestRealWatchOnAnExitedPidFailsWithGone(t *testing.T) {
	cmd := child(t)
	pid := cmd.Process.Pid
	_ = cmd.Process.Kill()
	_ = cmd.Wait() // reaped: the pid no longer exists
	if _, err := NewReal().Watch(pid, func(time.Time) {}); !errors.Is(err, ErrGone) {
		t.Fatalf("watching a reaped pid: %v (want ErrGone, the ESRCH fact)", err)
	}
	if _, err := NewReal().StartTime(pid); !errors.Is(err, ErrGone) {
		t.Fatalf("StartTime of a reaped pid: %v", err)
	}
}

func TestRealStartTimeAgreesWithPsAndTheRegistryFormatAndAReusedPidIsDead(t *testing.T) {
	r := NewReal()
	cmd := child(t)
	pid := cmd.Process.Pid
	st, err := r.StartTime(pid)
	if err != nil {
		t.Fatal(err)
	}
	ps := exec.CommandContext(context.Background(), "ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	ps.Env = append(os.Environ(), "TZ=UTC", "LC_ALL=C")
	out, err := ps.Output()
	if err != nil {
		t.Skipf("ps is not usable here: %v", err)
	}
	want, ok := ParseStart(strings.TrimSpace(string(out)))
	if !ok {
		t.Fatalf("ps lstart %q", out)
	}
	if d := st.Sub(want); d < -2*time.Second || d > 2*time.Second {
		t.Fatalf("sysctl start %v, ps start %v", st, want)
	}
	if !Alive(r, pid, FormatStart(st)) {
		t.Fatal("the child is not alive by its own start time")
	}
	if Alive(r, pid, FormatStart(st.Add(time.Hour))) {
		t.Fatal("a pid whose start time differs from procStart must be dead")
	}
}

func TestRealEnvironReadsTheHostBindingWithoutPs(t *testing.T) {
	r := NewReal()
	cmd := child(t, "CMUX_SURFACE_ID=example-surface", "CMUX_WORKSPACE_ID=example-ws", "TERM_PROGRAM=example-term")
	env, err := r.Environ(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	h := HostOf(env)
	if h.Kind != "cmux" || h.CmuxSurface != "example-surface" || h.CmuxWorkspace != "example-ws" || h.TermProgram != "example-term" {
		t.Fatalf("host %+v", h)
	}
}

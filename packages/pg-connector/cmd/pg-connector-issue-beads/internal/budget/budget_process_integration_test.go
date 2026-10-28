//go:build integration

// Cross-process tests of the budget: the subject is a real backend process that
// is SIGKILLed while its bd child (a real sleep) keeps the slot held. They
// re-execute the test binary and run an external program, so they are
// integration tier (run with -tags integration, see the module README).

package budget

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// helperEnv marks a re-executed test binary that stands in for the backend:
// it takes a slot, starts a bd stand-in child that holds the slot's file, and
// then blocks until it is killed.
const helperEnv = "PG_BUDGET_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		os.Exit(runHelper())
	}
	os.Exit(m.Run())
}

// runHelper reads PG_BUDGET_DIR (state directory), PG_BUDGET_SLEEP (child
// sleep seconds) and PG_BUDGET_MAX; it prints "ready <child pid>" on stdout
// once the child runs, then blocks forever (the parent test SIGKILLs it).
func runHelper() int {
	max, _ := strconv.Atoi(os.Getenv("PG_BUDGET_MAX"))
	b, err := New(Config{MaxConcurrent: max, StateDir: os.Getenv("PG_BUDGET_DIR"), WaitBound: time.Second})
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper:", err)
		return 2
	}
	slot, err := b.Acquire(context.Background(), false)
	if err != nil || slot.File() == nil {
		fmt.Fprintln(os.Stderr, "helper: no slot:", err)
		return 2
	}
	cmd := exec.Command("sleep", os.Getenv("PG_BUDGET_SLEEP"))
	cmd.ExtraFiles = []*os.File{slot.File()}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "helper:", err)
		return 2
	}
	// The backend drops its own descriptor right away, as the real runner may;
	// only the child's copy keeps the slot held.
	slot.Release()
	fmt.Printf("ready %d\n", cmd.Process.Pid)
	time.Sleep(time.Hour)
	return 0
}

// startHelper runs the helper process and returns it once it reports ready.
func startHelper(t *testing.T, dir string, max int, sleepSeconds string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), helperEnv+"=1", "PG_BUDGET_DIR="+dir,
		"PG_BUDGET_MAX="+strconv.Itoa(max), "PG_BUDGET_SLEEP="+sleepSeconds)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("helper did not report ready: %v", err)
	}
	var pid int
	if _, err := fmt.Sscanf(line, "ready %d", &pid); err != nil {
		t.Fatalf("unexpected helper output %q", line)
	}
	return cmd
}

// Case 3: a backend process that took a slot and started a child holding the
// slot's file, then was SIGKILLed, leaves the slot held until the child exits.
func TestSlotHeldByChildAfterBackendKilled(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("no sleep binary")
	}
	dir := t.TempDir()
	helper := startHelper(t, dir, 1, "3")
	started := time.Now()
	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()

	b := newBudget(t, Config{MaxConcurrent: 1, StateDir: dir, WaitBound: 200 * time.Millisecond})
	if _, err := b.Acquire(context.Background(), false); !errors.Is(err, ErrDenied) {
		t.Fatalf("after the backend died, child alive: err = %v, want ErrDenied", err)
	}

	b2 := newBudget(t, Config{MaxConcurrent: 1, StateDir: dir, WaitBound: 10 * time.Second})
	s, err := b2.Acquire(context.Background(), false)
	if err != nil {
		t.Fatalf("after the child exited: %v", err)
	}
	defer s.Release()
	if s.File() == nil {
		t.Fatalf("slot failed open: %v", s.FailOpenErr())
	}
	if time.Since(started) < 2*time.Second {
		t.Errorf("slot freed after %v, before the 3s child could have exited", time.Since(started))
	}
}

//go:build integration && unix

package wireclient

import (
	"context"
	"encoding/json"
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

// Environment names the integration test reads. The command adapter is a binary
// of a sibling module (it cannot be imported from here), so the check that runs
// this test builds it and passes its path.
const (
	envCommandAdapter        = "PG_ROUTER_TEST_COMMAND_ADAPTER"
	envRequireCommandAdapter = "PG_ROUTER_TEST_REQUIRE_COMMAND_ADAPTER"
)

func adapterBinary(t *testing.T) string {
	t.Helper()
	bin := os.Getenv(envCommandAdapter)
	if bin == "" {
		if os.Getenv(envRequireCommandAdapter) != "" {
			t.Fatalf("%s is unset but %s demands it", envCommandAdapter, envRequireCommandAdapter)
		}
		t.Skipf("%s is not set: no command adapter binary to run", envCommandAdapter)
	}
	return bin
}

// TestOSRunner_cancelStopsTheCommandAdaptersPayload runs the REAL command
// adapter under OSRunner with a payload that records its pid and sleeps, cancels
// the dispatch, and asserts the payload process is gone. The payload lives in a
// process group of its own, so only the adapter's SIGTERM handler can stop it:
// before OSRunner sent SIGTERM (it sent SIGKILL) the payload outlived the cancel.
func TestOSRunner_cancelStopsTheCommandAdaptersPayload(t *testing.T) {
	bin := adapterBinary(t)

	dir := t.TempDir()
	pidFile := filepath.Join(dir, "payload.pid")
	req, err := json.Marshal(map[string]any{
		"schemaVersion": "1",
		"id":            "req-1",
		"event":         map[string]any{"id": "evt-1", "type": "scratch.event", "payload": map[string]any{"id": "b1"}},
		"role": map[string]any{
			"name": "cmdrole",
			"config": map[string]any{"command": map[string]any{
				"argv": []string{"/bin/sh", "-c", `echo $$ > "` + pidFile + `"; exec sleep 600`},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := OSRunner{}.Run(ctx, []string{bin, "dispatch"}, req)
		done <- err
	}()

	var pid int
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil && strings.HasSuffix(string(b), "\n") {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid <= 0 {
		t.Fatal("the payload never wrote its pid")
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("payload pid %d is still alive after the dispatch was cancelled", pid)
}

// Environment the re-executed parent process reads (see TestParentDeathHelper).
const (
	envHelperRole     = "PG_ROUTER_TEST_PARENT_HELPER_REQ"
	envHelperSurvives = "PG_ROUTER_TEST_PARENT_HELPER_SURVIVES"
	envHelperAdapter  = "PG_ROUTER_TEST_PARENT_HELPER_ADAPTER"
)

// TestParentDeathHelper is not a test: it is the "parent" process of
// TestOSRunner_parentSIGKILL*, re-executed from the test binary. It stands in for
// the pg-router daemon, running the real adapter under OSRunner (and, for a
// survives_shutdown role, under WithoutParentDeath, as Client does) until the
// test SIGKILLs it.
func TestParentDeathHelper(t *testing.T) {
	req := os.Getenv(envHelperRole)
	if req == "" {
		t.Skip("helper process only")
	}
	ctx := context.Background()
	if os.Getenv(envHelperSurvives) != "" {
		ctx = WithoutParentDeath(ctx)
	}
	_, _, _ = OSRunner{}.Run(ctx, []string{os.Getenv(envHelperAdapter), "dispatch"}, []byte(req))
}

// runKilledParent starts a parent process that runs a payload through the real
// adapter, waits for the payload's pid, SIGKILLs the parent (no graceful cancel)
// and returns the payload pid and the moment of the kill.
func runKilledParent(t *testing.T, survives bool) (pid int, killedAt time.Time) {
	t.Helper()
	bin := adapterBinary(t)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "payload.pid")
	req, err := json.Marshal(map[string]any{
		"schemaVersion": "1",
		"id":            "req-1",
		"event":         map[string]any{"id": "evt-1", "type": "scratch.event", "payload": map[string]any{"id": "b1"}},
		"role": map[string]any{
			"name": "cmdrole",
			"config": map[string]any{"command": map[string]any{
				"argv": []string{"/bin/sh", "-c", `echo $$ > "` + pidFile + `"; exec sleep 600`},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := exec.Command(os.Args[0], "-test.run=^TestParentDeathHelper$")
	parent.Env = append(os.Environ(), envHelperRole+"="+string(req), envHelperAdapter+"="+bin)
	if survives {
		parent.Env = append(parent.Env, envHelperSurvives+"=1")
	}
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Process.Kill(); _ = parent.Wait() })

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil && strings.HasSuffix(string(b), "\n") {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid <= 0 {
		t.Fatal("the payload never wrote its pid")
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	killedAt = time.Now()
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = parent.Wait()
	return pid, killedAt
}

// TestOSRunner_parentSIGKILLStopsThePayload: with the daemon SIGKILLed (no
// graceful cancel, so no SIGTERM reaches the adapter), the adapter reads EOF on
// its parent-death pipe and stops the payload group within its cancel grace (5 s)
// plus 1 s.
func TestOSRunner_parentSIGKILLStopsThePayload(t *testing.T) {
	pid, killedAt := runKilledParent(t, false)
	deadline := killedAt.Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("payload pid %d still alive %s after the parent was SIGKILLed", pid, time.Since(killedAt))
}

// TestOSRunner_survivesShutdownIgnoresParentDeath: a survives_shutdown role's
// call is made WithoutParentDeath, so the adapter gets no pipe and its payload
// keeps running after the parent dies.
func TestOSRunner_survivesShutdownIgnoresParentDeath(t *testing.T) {
	pid, _ := runKilledParent(t, true)
	time.Sleep(8 * time.Second) // longer than cancel grace plus the 1 s margin
	if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		t.Fatalf("payload pid %d died although the role survives shutdown", pid)
	}
}

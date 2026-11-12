//go:build integration

package main

// Integration test for the per-(role, event) dispatch lock with a REAL orphaned
// payload: the adapter is a separate process that is SIGKILLed while its payload
// (its own process group) keeps running. It runs only under -tags integration,
// outside the unit gate.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/executor"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
	"github.com/phillipgreenii/pg-router-handlers/internal/sessionlock"
	"github.com/phillipgreenii/pg-router/conformance"
)

const (
	envAdapterReq  = "PG_TEST_ORPHAN_ADAPTER_REQ"
	envAdapterLock = "PG_TEST_ORPHAN_ADAPTER_LOCKDIR"
)

func lockedDeps(lockDir string) func(config.Config, roles.Role) executor.Deps {
	return func(cfg config.Config, _ roles.Role) executor.Deps {
		return executor.Deps{Cfg: cfg, LockDir: lockDir}
	}
}

// TestHelperAdapterProcess is the child "adapter": it serves one dispatch and
// is expected to be SIGKILLed by the parent test before it returns.
func TestHelperAdapterProcess(t *testing.T) {
	req := os.Getenv(envAdapterReq)
	if req == "" {
		t.Skip("helper process only")
	}
	var out bytes.Buffer
	serveDispatch(context.Background(), strings.NewReader(req), &out, "", lockedDeps(os.Getenv(envAdapterLock)))
}

func TestOrphanedPayloadHoldsTheEventLock(t *testing.T) {
	lockDir := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "payload.pid")
	cfg := cmdConfig(false, "/bin/sh", "-c", `echo $$ > "`+pidFile+`"; exec sleep 600`)
	req := request(t, eventWith(map[string]any{"id": "b1"}), "cmdrole", cfg, true)

	adapter := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperAdapterProcess$")
	adapter.Env = append(os.Environ(), envAdapterReq+"="+req, envAdapterLock+"="+lockDir)
	if err := adapter.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Process.Kill(); _ = adapter.Wait() })
	pid := waitPid(t, pidFile)

	// The adapter dies without any cleanup; the payload group lives on.
	if err := adapter.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = adapter.Wait()
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("payload %d did not survive its adapter: %v", pid, err)
	}

	redeliver := func() (int, string) {
		var out bytes.Buffer
		exit := serveDispatch(context.Background(), strings.NewReader(req), &out, "", lockedDeps(lockDir))
		return exit, out.String()
	}
	exit, reply := redeliver()
	if exit != conformance.ExitBusy {
		t.Fatalf("redelivery with an orphan alive: exit %d, reply %s; want busy", exit, reply)
	}
	if l, err := sessionlock.TryLock(lockDir, "unrelated"); err != nil {
		t.Fatalf("an unrelated key must stay free: %v", err)
	} else {
		l.Unlock()
	}

	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitGone(t, pid, "orphaned payload")
	deadline := time.Now().Add(10 * time.Second)
	for {
		// The next dispatch runs the payload; use a quick one so it ends.
		quick := request(t, eventWith(map[string]any{"id": "b1"}), "cmdrole", cmdConfig(false, "/bin/sh", "-c", "exit 0"), true)
		var out bytes.Buffer
		exit := serveDispatch(context.Background(), strings.NewReader(quick), &out, "", lockedDeps(lockDir))
		if exit == conformance.ExitOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lock still held after the payload was killed: exit %d, reply %s", exit, out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

//go:build integration && unix

package wireclient

import (
	"context"
	"encoding/json"
	"errors"
	"os"
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

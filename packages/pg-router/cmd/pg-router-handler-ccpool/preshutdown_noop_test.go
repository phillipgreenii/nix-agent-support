package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
)

// stubTools puts recording stand-ins for every external tool the old shutdown
// sweep used (ccpool, bd, git, tmux) first on PATH. Each one appends its name to
// the returned log file, so a test can prove none ran.
func stubTools(t *testing.T) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	for _, tool := range []string{"ccpool", "bd", "git", "tmux", "pg-router-handler-ccpool"} {
		body := "#!/bin/sh\necho " + tool + " \"$@\" >> " + logPath + "\n"
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return logPath
}

// preShutdown answers at once and runs no sweep: no ccpool list or close, no bd
// lookup, no git call, no spawned process, whatever the role config holds. It
// reads no launch config either, so a missing one cannot fail it.
func TestServePreShutdown_answersAtOnceWithNoSweepCalls(t *testing.T) {
	for name, body := range map[string]string{
		"role with config": request("", "worker", map[string]any{}),
		"missing config":   request("", "worker", nil),
		"unknown keys":     request("", "worker", map[string]any{"bogus": 1}),
	} {
		t.Run(name, func(t *testing.T) {
			logPath := stubTools(t)
			t.Setenv("PG_ROUTER_CCPOOL_HANDLER_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
			var stdout bytes.Buffer
			start := time.Now()
			code := servePreShutdown(strings.NewReader(body), &stdout)
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Errorf("preShutdown took %v, want well under 1s", elapsed)
			}
			if code != conformance.ExitOK {
				t.Fatalf("exit = %d, want %d", code, conformance.ExitOK)
			}
			reply := decodeReply(t, stdout.String())
			if reply["id"] != "d-1" || reply["outcome"] != "ok" {
				t.Errorf("reply = %+v, want id=d-1 outcome=ok", reply)
			}
			if err := conformance.Check("handler.preShutdown-reply", reply); err != nil {
				t.Errorf("reply does not conform: %v", err)
			}
			if calls, err := os.ReadFile(logPath); err == nil {
				t.Errorf("preShutdown ran external tools:\n%s", calls)
			}
		})
	}
}

// A malformed request is still rejected; the request contract is unchanged.
func TestServePreShutdown_rejectsMalformedRequest(t *testing.T) {
	var stdout bytes.Buffer
	code := servePreShutdown(strings.NewReader(`{"schemaVersion":"1","role":{"name":"worker"}}`), &stdout) // missing id
	if code != conformance.ExitError || !strings.Contains(stdout.String(), "error") {
		t.Fatalf("exit = %d, out = %q; want exit %d with an error reply", code, stdout.String(), conformance.ExitError)
	}
}

// A request without a usable role object is a usage error with an error reply.
func TestServePreShutdown_requestWithoutRoleIsUsageError(t *testing.T) {
	for name, body := range map[string]string{
		"no role":    `{"schemaVersion":"1","id":"hs-1"}`,
		"empty name": `{"schemaVersion":"1","id":"hs-1","role":{"name":""}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var stdout bytes.Buffer
			code := servePreShutdown(strings.NewReader(body), &stdout)
			if code != conformance.ExitUsage || !strings.Contains(stdout.String(), "error") {
				t.Fatalf("exit = %d, out = %q; want exit %d with an error reply", code, stdout.String(), conformance.ExitUsage)
			}
		})
	}
}

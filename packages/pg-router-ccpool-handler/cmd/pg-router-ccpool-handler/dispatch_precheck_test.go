package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/conformance"
)

// precheckEnv builds a hermetic environment for the review precheck through the
// REAL runDispatch wiring: PATH holds only fake `bd` and `pg-connector` scripts
// (no `ccpool`, so a dispatch that gets past the precheck dies at the admission
// gate with reason capacity-unknown, which is how these tests tell "launch path
// reached" from "skipped"). It returns the role-config path and the log of
// every pg-connector call, so a test can prove the checks were read-only.
func precheckEnv(t *testing.T, beadStatus, prJSON, pendingJSON string) (rolePath, connLog string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	connLog = filepath.Join(dir, "connector.log")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("bd", `printf '%s' '{"data":[{"id":"zr-r","status":"`+beadStatus+`","metadata":{"repo":"o/r","pr_number":120058,"head_sha":"abc"}}]}'`+"\n")
	write("pg-connector", `echo "$*" >> '`+connLog+`'
case "$*" in
  "pr show "*) printf '%s' '`+prJSON+`' ;;
  "pr review pending "*) printf '%s' '`+pendingJSON+`' ;;
  *) echo "unexpected pg-connector call: $*" >&2; exit 2 ;;
esac
`)
	// Only the fake bin dir: `sh` is reached through the scripts' shebang, and
	// the scripts use shell builtins only.
	t.Setenv("PATH", bin)
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	rolePath = filepath.Join(dir, "role.json")
	roleJSON := `{"name":"review","type":"ccpool","ccpool":{"actor":"test-actor","completion":"close-or-handback","onFailure":"add-human","onDispatchFail":"unclaim","promptBody":"hello"}}`
	if err := os.WriteFile(rolePath, []byte(roleJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return rolePath, connLog
}

func runPrecheckDispatch(t *testing.T, rolePath string) (code int, reason string) {
	t.Helper()
	restoreIn := redirectStdin(t, `{"schemaVersion":"1","id":"d-1","event":{"id":"e-1","type":"dispatch","payload":{"id":"zr-r","type":"review-pr","metadata":{"repo":"o/r","pr_number":120058,"head_sha":"abc"}}}}`)
	defer restoreIn()
	got := captureStdout(t, func() { code = runDispatch([]string{"--role-config", rolePath}) })
	var reply struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(got), &reply); err != nil {
		t.Fatalf("stdout = %q, not valid JSON: %v", got, err)
	}
	return code, reply.Reason
}

const (
	prOpen      = `{"result":{"state":"open","merged":false}}`
	prMerged    = `{"result":{"state":"closed","merged":true}}`
	noPending   = `{"result":{"pending":false}}`
	freshReview = `{"result":{"pending":true,"review":{"stale":false}}}`
	staleReview = `{"result":{"pending":true,"review":{"stale":true}}}`
)

// TestRunDispatch_reviewPrecheck proves the three precheck skips surface as the
// pre-accept busy decline carrying each skip's own reason (the metric label
// pg-router core counts), and that every other review dispatch reaches the
// unchanged launch path (here: the admission gate, which fails closed because
// the fake PATH has no ccpool).
func TestRunDispatch_reviewPrecheck(t *testing.T) {
	tests := []struct {
		name       string
		bead, pr   string
		pending    string
		wantReason string
		wantCalls  int // pg-connector invocations
	}{
		{"bead closed", "closed", prOpen, noPending, "skipped-bead-closed", 0},
		{"PR merged", "open", prMerged, noPending, "skipped-pr-merged", 1},
		{"pending review covers the head", "open", prOpen, freshReview, "skipped-pending-review", 2},
		{"launch path: nothing to skip", "open", prOpen, noPending, busyReasonCapacityUnknown, 2},
		{"launch path: stale pending review", "open", prOpen, staleReview, busyReasonCapacityUnknown, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rolePath, connLog := precheckEnv(t, tc.bead, tc.pr, tc.pending)
			code, reason := runPrecheckDispatch(t, rolePath)
			if code != conformance.ExitBusy {
				t.Fatalf("exit = %d, want ExitBusy (%d)", code, conformance.ExitBusy)
			}
			if reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tc.wantReason)
			}
			calls := 0
			if b, err := os.ReadFile(connLog); err == nil {
				calls = strings.Count(string(b), "\n")
			}
			if calls != tc.wantCalls {
				t.Fatalf("pg-connector calls = %d, want %d", calls, tc.wantCalls)
			}
		})
	}
}

// TestRunDispatch_reviewPrecheckFailsOpenWithoutConnector: with no pg-connector
// on PATH the precheck cannot read the PR, logs it, and launches as before.
func TestRunDispatch_reviewPrecheckFailsOpenWithoutConnector(t *testing.T) {
	rolePath, _ := precheckEnv(t, "open", prOpen, noPending)
	if err := os.Remove(filepath.Join(os.Getenv("PATH"), "pg-connector")); err != nil {
		t.Fatal(err)
	}
	code, reason := runPrecheckDispatch(t, rolePath)
	if code != conformance.ExitBusy || reason != busyReasonCapacityUnknown {
		t.Fatalf("got exit %d reason %q, want the unchanged launch path (capacity-unknown)", code, reason)
	}
}

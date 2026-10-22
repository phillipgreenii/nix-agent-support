package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/eventlog"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router/conformance"
)

// The dispatch_decline record (INV-CCH-35): one per decline, written just
// before the reply, carrying the role, the item id, the reason, the kind, the
// role's sessionEnv, the redacted evidence tail and the failure signature.

func readDeclineRecords(t *testing.T, stateDir string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stateDir, handlerEventLogName))
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var rec map[string]any
		if line == "" || json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if rec["kind"] == "dispatch_decline" {
			out = append(out, rec)
		}
	}
	return out
}

// runDeclineDispatch drives runDispatch against a stub ccpool (script body) for
// a ccpool role carrying a sessionEnv, with the handler state directory in a
// temp dir; it returns the exit code, the reply and the state dir.
func runDeclineDispatch(t *testing.T, ccpoolScript string) (int, string, string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ccpool"), []byte("#!/bin/sh\n"+ccpoolScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	cfgPath := filepath.Join(dir, "launch.json")
	if err := os.WriteFile(cfgPath, []byte(`{"originProbe":{"stateDir":"`+stateDir+`"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rolePath := filepath.Join(dir, "role.json")
	roleJSON := `{"name":"worker","type":"ccpool","sessionEnv":{"PG_WRAP_SPEC":"/etc/pg-wrap/worker.toml","K15":"v"},"ccpool":{"actor":"test-actor","completion":"close-only","onFailure":"unclaim","onDispatchFail":"unclaim","promptBody":"hello","isolation":{"Type":"none"}}}`
	if err := os.WriteFile(rolePath, []byte(roleJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	restoreIn := redirectStdin(t, `{"schemaVersion":"1","id":"d-1","event":{"id":"e-1","type":"dispatch","payload":{"id":"zr-w"}}}`)
	defer restoreIn()
	var code int
	got := captureStdout(t, func() { code = runDispatch([]string{"--role-config", rolePath, "--config", cfgPath}) })
	return code, got, stateDir
}

// A failing ccpool new (the fake ccpool) writes one dispatch_decline with all
// seven fields.
func TestRunDispatch_launchFailedWritesDispatchDecline(t *testing.T) {
	code, got, stateDir := runDeclineDispatch(t, `case "$1" in
  list) echo '[]'; exit 0;;
  capacity) echo '{"max_sessions":4,"free":4,"counted":0,"live":0,"preserved":0}'; exit 0;;
  new) echo "did not reach ready (state=starting) Bearer abcdefghijklmnopqrstuvwxyz0123456789ABCDEF" >&2; exit 1;;
  close) exit 0;;
esac
exit 1
`)
	if code != conformance.ExitBusy {
		t.Fatalf("exit = %d, want ExitBusy; stdout=%q", code, got)
	}
	recs := readDeclineRecords(t, stateDir)
	if len(recs) != 1 {
		t.Fatalf("want one dispatch_decline, got %d: %v", len(recs), recs)
	}
	rec := recs[0]
	for k, want := range map[string]any{
		"role": "worker", "item_id": "zr-w", "reason": "launch-failed", "decline_kind": "fault",
		"failure_signature": "launch-failed",
	} {
		if rec[k] != want {
			t.Errorf("%s = %v, want %v", k, rec[k], want)
		}
	}
	if !reflect.DeepEqual(rec["session_env"], map[string]any{"PG_WRAP_SPEC": "/etc/pg-wrap/worker.toml", "K15": "v"}) {
		t.Errorf("session_env = %v, want the role's entries, values included", rec["session_env"])
	}
	ev, _ := rec["evidence"].(string)
	if !strings.Contains(ev, "did not reach ready") || strings.Contains(ev, "abcdefghijklmnopqrstuvwxyz") {
		t.Errorf("evidence = %q, want the redacted ccpool new error tail", ev)
	}
	if rec["level"] != "warn" {
		t.Errorf("level = %v, want warn for a fault decline", rec["level"])
	}
	// The record agrees with the wire reply's detail.
	var reply struct{ Detail string }
	_ = json.Unmarshal([]byte(got), &reply)
	if reply.Detail != ev {
		t.Errorf("evidence %q differs from the reply detail %q", ev, reply.Detail)
	}
}

// An at-capacity decline is kind busy with no evidence and no signature.
func TestRunDispatch_atCapacityWritesDispatchDecline(t *testing.T) {
	code, got, stateDir := runDeclineDispatch(t, `case "$1" in
  list) echo '[]'; exit 0;;
  capacity) echo '{"max_sessions":4,"free":0,"counted":4,"live":4,"preserved":0}'; exit 0;;
esac
exit 1
`)
	if code != conformance.ExitBusy {
		t.Fatalf("exit = %d, want ExitBusy; stdout=%q", code, got)
	}
	recs := readDeclineRecords(t, stateDir)
	if len(recs) != 1 {
		t.Fatalf("want one dispatch_decline, got %d: %v", len(recs), recs)
	}
	rec := recs[0]
	for k, want := range map[string]any{
		"role": "worker", "item_id": "zr-w", "reason": "at-capacity", "decline_kind": "busy",
		"failure_signature": "", "evidence": "", "level": "info",
	} {
		if rec[k] != want {
			t.Errorf("%s = %v, want %v", k, rec[k], want)
		}
	}
	for _, k := range []string{"session_env", "evidence", "failure_signature", "decline_kind"} {
		if _, present := rec[k]; !present {
			t.Errorf("field %s must be present even when empty: %v", k, rec)
		}
	}
}

// A command role's decline carries an empty (not absent) sessionEnv, and every
// decline kind the table names flows through the one writer.
func TestEmitDispatchDecline_commandRoleAndNeutral(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := eventlog.New(path)
	if err != nil {
		t.Fatal(err)
	}
	role := roles.Role{Name: "k15-live", Type: "command"}
	emitDispatchDecline(log, role, "b-9", busyDecline{Reason: busyReasonCommandBusy, Kind: declineKindBusy})
	emitDispatchDecline(log, role, "b-9", neutralDecline("skipped-bead-closed", "the bead is closed"))
	emitDispatchDecline(nil, role, "b-9", busyDecline{}) // a nil log is a no-op
	recs := readDeclineRecords(t, filepath.Dir(path))
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %v", recs)
	}
	if recs[0]["reason"] != "command-busy" || recs[0]["decline_kind"] != "busy" || recs[0]["item_id"] != "b-9" {
		t.Errorf("record 0 = %v", recs[0])
	}
	if !reflect.DeepEqual(recs[0]["session_env"], map[string]any{}) {
		t.Errorf("session_env = %v, want an empty object for a command role", recs[0]["session_env"])
	}
	if recs[1]["decline_kind"] != "neutral" || recs[1]["failure_signature"] != "" || recs[1]["evidence"] != "" {
		t.Errorf("a skipped-* decline has no signature or evidence: %v", recs[1])
	}
}

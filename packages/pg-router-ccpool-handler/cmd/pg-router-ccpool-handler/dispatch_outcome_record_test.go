package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/conformance"
)

// The outcome records of INV-CCH-35 through the real dispatch subcommand: a
// command role writes dispatch_start when its payload starts and then exactly
// one outcome record, whatever the payload's exit.

// runCommandRoleDispatch runs runDispatch for a command role whose payload is
// the /bin/sh script, with the handler state directory in a temp dir, and
// returns the exit code and the dispatch_* kinds of the event log in order.
func runCommandRoleDispatch(t *testing.T, script string) (int, []string, []map[string]any) {
	t.Helper()
	t.Setenv("PATH", "/usr/bin:/bin")
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	cfgPath := filepath.Join(dir, "launch.json")
	if err := os.WriteFile(cfgPath, []byte(`{"originProbe":{"stateDir":"`+stateDir+`"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	argv, err := json.Marshal([]string{"/bin/sh", "-c", script})
	if err != nil {
		t.Fatal(err)
	}
	rolePath := filepath.Join(dir, "role.json")
	if err := os.WriteFile(rolePath, []byte(`{"name":"cmd-role","type":"command","command":{"argv":`+string(argv)+`}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	restoreIn := redirectStdin(t, `{"schemaVersion":"1","id":"d-1","event":{"id":"e-1","type":"dispatch","payload":{"id":"zr-w"}}}`)
	defer restoreIn()
	var code int
	_ = captureStdout(t, func() { code = runDispatch([]string{"--role-config", rolePath, "--config", cfgPath}) })
	b, err := os.ReadFile(filepath.Join(stateDir, handlerEventLogName))
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	var kinds []string
	var recs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var rec map[string]any
		if line == "" || json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if k, _ := rec["kind"].(string); strings.HasPrefix(k, "dispatch_") {
			kinds = append(kinds, k)
			recs = append(recs, rec)
		}
	}
	return code, kinds, recs
}

// A command role's busy exit 9 writes dispatch_start and then dispatch_decline,
// and nothing else (ruling 33).
func TestRunDispatch_commandExit9WritesDispatchStartThenDispatchDecline(t *testing.T) {
	code, kinds, recs := runCommandRoleDispatch(t, "exit 9")
	if code != conformance.ExitBusy {
		t.Fatalf("exit = %d, want ExitBusy (%d)", code, conformance.ExitBusy)
	}
	if want := []string{"dispatch_start", "dispatch_decline"}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("dispatch records = %v, want %v", kinds, want)
	}
	if recs[1]["reason"] != "command-busy" || recs[1]["item_id"] != "zr-w" {
		t.Errorf("decline record = %v", recs[1])
	}
}

// A successful command dispatch writes dispatch_start and then one dispatch_end.
func TestRunDispatch_commandSuccessWritesDispatchStartThenDispatchEnd(t *testing.T) {
	code, kinds, recs := runCommandRoleDispatch(t, "exit 0")
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want ExitOK", code)
	}
	if want := []string{"dispatch_start", "dispatch_end"}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("dispatch records = %v, want %v", kinds, want)
	}
	if recs[1]["outcome"] != "completed" {
		t.Errorf("end record = %v", recs[1])
	}
}

// A failing command dispatch writes dispatch_start and then one dispatch_result.
func TestRunDispatch_commandFailureWritesDispatchStartThenDispatchResult(t *testing.T) {
	code, kinds, recs := runCommandRoleDispatch(t, "echo boom >&2; exit 3")
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want ExitError", code)
	}
	if want := []string{"dispatch_start", "dispatch_result"}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("dispatch records = %v, want %v", kinds, want)
	}
	if recs[1]["failure_signature"] == nil || recs[1]["signature_evidence"] == "" {
		t.Errorf("failure record = %v", recs[1])
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// TestRun_InstrumentWritesOneEventPerCallToTheBackendsOwnLog (bead pg2-ltddq):
// the production wiring (instrument over newDispatchTable) leaves exactly one
// JSONL event per call in the log file the backend owns -- resolved from ITS
// OWN environment, not from pg-connector's config -- and a failed call is
// logged as an error, tagged failure_class=auth for a 401, with the wire error
// code unchanged.
func TestRun_InstrumentWritesOneEventPerCallToTheBackendsOwnLog(t *testing.T) {
	stateHome := t.TempDir()
	getenv := func(k string) string {
		if k == "XDG_STATE_HOME" {
			return stateHome
		}
		return ""
	}
	failing := false
	backend := internal.New(&fakeRunner{handle: func(args []string) (string, error) {
		if failing {
			return "", errors.New("exit status 1: pjira: get issue PROJ-1: status 401 Unauthorized")
		}
		return `{"key":"PROJ-1","summary":"hello","status":"To Do","issuetype":"Task"}`, nil
	}})
	table := instrument(newDispatchTable(backend), getenv)

	var out bytes.Buffer
	scriptout.ServeOne(table, strings.NewReader(`{"op":"show","args":{"id":"PROJ-1"}}`), &out)
	failing = true
	out.Reset()
	scriptout.ServeOne(table, strings.NewReader(`{"op":"show","args":{"id":"PROJ-1"}}`), &out)
	var resp scriptout.Response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil || resp.Error == nil || resp.Error.Code != "unauthenticated" {
		t.Fatalf("wire response for the failing call = %s (err %v), want error.code unauthenticated", out.Bytes(), err)
	}

	path := filepath.Join(stateHome, "pg-connector-issue-jira", "events.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("event log not written at %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2:\n%s", len(lines), raw)
	}
	var evs []map[string]any
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad line %q: %v", l, err)
		}
		evs = append(evs, m)
	}
	if evs[0]["op"] != "show" || evs[0]["level"] != "info" || evs[0]["service"] != "pg-connector-issue-jira" || evs[0]["pjira_calls"] != float64(1) {
		t.Errorf("success event = %v", evs[0])
	}
	if _, has := evs[0]["error_code"]; has {
		t.Errorf("success event carries error_code: %v", evs[0])
	}
	if evs[1]["level"] != "error" || evs[1]["error_code"] != "unauthenticated" || evs[1]["failure_class"] != "auth" {
		t.Errorf("failing event = %v", evs[1])
	}
}

// TestInstrument_DisabledLeavesTableAlone: EnvPath=off must not write
// anything and must not alter the table.
func TestInstrument_DisabledLeavesTableAlone(t *testing.T) {
	dir := t.TempDir()
	getenv := func(k string) string {
		switch k {
		case eventlog.EnvPath:
			return "off"
		case "XDG_STATE_HOME":
			return dir
		}
		return ""
	}
	table := instrument(newDispatchTable(newTestBackend()), getenv)
	var out bytes.Buffer
	scriptout.ServeOne(table, strings.NewReader(`{"op":"show","args":{"id":"PROJ-1"}}`), &out)
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("disabled event log wrote files: %v", entries)
	}
}

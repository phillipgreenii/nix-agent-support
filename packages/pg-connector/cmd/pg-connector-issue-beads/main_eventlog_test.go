package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-beads/internal/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// TestRun_InstrumentWritesStartAndFinalRowPerCallToTheBackendsOwnLog (bead
// pg2-5dyz2): the production wiring (instrument over newDispatchTable) leaves
// a start row and a final row per call in the log file the backend owns,
// resolved from ITS OWN environment.
func TestRun_InstrumentWritesStartAndFinalRowPerCallToTheBackendsOwnLog(t *testing.T) {
	stateHome := t.TempDir()
	getenv := func(k string) string {
		if k == "XDG_STATE_HOME" {
			return stateHome
		}
		return ""
	}
	table := instrument(newDispatchTable(newTestBackend()), getenv)
	var out bytes.Buffer
	scriptout.ServeOne(table, strings.NewReader(`{"op":"show","args":{"id":"tp-1"}}`), &out)

	path := filepath.Join(stateHome, "pg-connector-issue-beads", "events.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("event log not written at %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want a start row and a final row:\n%s", len(lines), raw)
	}
	var start, final map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &start); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &final); err != nil {
		t.Fatal(err)
	}
	if start["phase"] != "start" || start["op"] != "show" || start["service"] != "pg-connector-issue-beads" || start["args"] != `{"id":"tp-1"}` {
		t.Errorf("start row = %v", start)
	}
	if final["op"] != "show" || final["level"] != "info" || final["msg"] != "show ok" {
		t.Errorf("final row = %v", final)
	}
}

// TestInstrument_DisabledLeavesTableAlone: EnvPath=off must not write anything.
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
	scriptout.ServeOne(table, strings.NewReader(`{"op":"show","args":{"id":"tp-1"}}`), &out)
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("disabled event log wrote files: %v", entries)
	}
}

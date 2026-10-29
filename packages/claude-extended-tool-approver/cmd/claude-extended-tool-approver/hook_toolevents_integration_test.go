//go:build integration

// Hook-mode tests for the ceta.* tool events: they exec the COMPILED hook binary with
// a real PreToolUse payload (the same fixtures the unattended tests use) and read the
// event file it appends to under a throwaway XDG_STATE_HOME. The file contract is
// claude-tool-tracker's docs/event-kinds.md.

package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readCetaEvents(t *testing.T, state, session string) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join(state, "claude-tool-tracker", "events", "tool-events."+session+".ceta.jsonl"))
	if err != nil {
		t.Fatalf("no ceta event file: %v", err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad line: %v", err)
		}
		out = append(out, m)
	}
	return out
}

func TestToolEvents_UnattendedAskDeliveredAsRejectIsRecorded(t *testing.T) {
	state := t.TempDir()
	p := bashPayload(secretCWD, "dontAsk", envrcRead)
	run := runPreToolUse(t, p, "XDG_STATE_HOME="+state)
	if run.decision() != "deny" {
		t.Fatalf("decision = %q, want deny", run.decision())
	}
	evs := readCetaEvents(t, state, p.SessionID)
	var verdict map[string]any
	for _, e := range evs {
		if e["kind"] == "ceta.verdict" {
			verdict = e
		}
		if e["source"] != "ceta" || e["schema"] != "tool-events/1" || e["tool_use_id"] != p.ToolUseID || e["session_id"] != p.SessionID {
			t.Errorf("bad envelope: %v", e)
		}
	}
	if verdict == nil {
		t.Fatalf("no ceta.verdict in %v", evs)
	}
	d := verdict["data"].(map[string]any)
	if d["verdict"] != "ask" || d["delivered"] != "reject" {
		t.Errorf("data = %v, want verdict ask delivered reject", d)
	}
	if _, ok := d["sandbox_enabled"].(bool); !ok {
		t.Errorf("sandbox_enabled missing: %v", d)
	}
}

func TestToolEvents_UnwritableEventsDirChangesNothing(t *testing.T) {
	good := t.TempDir()
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "claude-tool-tracker"), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"git ls-files -- .env.yarn | head", envrcRead} {
		p := bashPayload(secretCWD, "default", cmd)
		withDir := runPreToolUse(t, p, "XDG_STATE_HOME="+good)
		withoutDir := runPreToolUse(t, p, "XDG_STATE_HOME="+bad)
		if withDir.stdout != withoutDir.stdout {
			t.Errorf("stdout differs for %q:\nwritable:   %s\nunwritable: %s", cmd, withDir.stdout, withoutDir.stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(good, "claude-tool-tracker", "events")); err != nil {
		t.Errorf("writable run wrote no events dir: %v", err)
	}
}

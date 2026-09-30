package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// TestBuildDeps_setsEventLog is the pg2-ui2gk regression: buildDeps left
// Deps.Log nil, so every watchdog/executor event (hard_stop, needs_input)
// was a silent no-op in production. It also proves the wired sink really
// reaches <stateDir>/events.jsonl.
func TestBuildDeps_setsEventLog(t *testing.T) {
	cfg := config.Default()
	cfg.OriginProbe.StateDir = t.TempDir()
	role := roles.Role{Name: "worker", CCPool: &roles.CCPoolConfig{}}

	deps := buildDeps(cfg, role)

	if deps.Log == nil {
		t.Fatal("buildDeps left Deps.Log nil: watchdog hard_stop/needs_input events would be dropped")
	}
	if err := deps.Log.Emit("error", "hard_stop", "budget hard stop reached", map[string]any{"bead": "x-1"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(cfg.OriginProbe.StateDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"msg":"budget hard stop reached"`) || !strings.Contains(string(b), `"kind":"hard_stop"`) {
		t.Errorf("events.jsonl missing hard_stop line: %q", b)
	}
}

// TestNewEventLog_neverNilWhenStateDirUnwritable proves an unopenable state
// dir degrades to a dropping sink, not nil and not a panic.
func TestNewEventLog_neverNilWhenStateDirUnwritable(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.OriginProbe.StateDir = filepath.Join(f, "sub") // parent is a file: MkdirAll fails

	w := newEventLog(cfg)
	if w == nil {
		t.Fatal("newEventLog returned nil")
	}
	if err := w.Emit("info", "k", "m", nil); err != nil {
		t.Errorf("fallback sink Emit: %v", err)
	}
}

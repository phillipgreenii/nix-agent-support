package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/lanestate"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const prLaneLineGolden = "testdata/pr_lane_line.golden.txt"

// laneLineNow is the fixed clock of the golden: every age in it is relative to
// this instant, so the file does not depend on the wall clock.
var laneLineNow = time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)

// laneLineStore builds a real schema-2 store (the cut-over seeded store holds
// one never-hydrated PR, o/r#1: migrated rows are at version 0) and applies
// record to it. The lane-state write path is the real one (lanestate.Record),
// not a stub.
func laneLineStore(t *testing.T, kind string, record func(t *testing.T, st *store.Store)) *store.Store {
	t.Helper()
	st, err := store.Open(storeAtVersion(t, kind))
	if err != nil {
		t.Fatalf("open %s store: %v", kind, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if record != nil {
		record(t, st)
	}
	return st
}

func recordRun(t *testing.T, st *store.Store, configured, mode string, residual *int, at time.Time) {
	t.Helper()
	in := lanestate.RunInput{PR: "o/r#1", Mode: mode, Residual: residual, LastOutcome: "ok"}
	if err := lanestate.Record(st, configured, at, in); err != nil {
		t.Fatalf("record %s run: %v", mode, err)
	}
}

func intPtr(n int) *int { return &n }

func laneLineCfg(syncMode, laneMode string) *config.Config {
	c := openTestConfig("o/r")
	c.Sync.Mode = syncMode
	c.PRLane.Mode = laneMode
	return c
}

// prLaneLineCases is the matrix the golden file pins: no configuration, no
// store, an old-schema store, the lane off, plan and apply modes with and
// without recorded runs, a configured mode that differs from the recorded one,
// a by-hand plan run after the flip, an unset and an invalid mode.
func prLaneLineCases(t *testing.T) []struct {
	name string
	cfg  *config.Config
	st   *store.Store
} {
	t.Helper()
	at := func(h int) time.Time { return time.Date(2026, 10, 2, h, 0, 0, 0, time.UTC) }
	return []struct {
		name string
		cfg  *config.Config
		st   *store.Store
	}{
		{"no configuration", nil, laneLineStore(t, "new", nil)},
		{"no store", laneLineCfg("off", "off"), nil},
		{"old-schema store", laneLineCfg("off", "off"), laneLineStore(t, "old", nil)},
		{"lane off, nothing recorded", laneLineCfg("off", "off"), laneLineStore(t, "new", nil)},
		{"nothing set", laneLineCfg("", ""), laneLineStore(t, "new", nil)},
		{"invalid lane mode", laneLineCfg("off", "aplly"), laneLineStore(t, "new", nil)},
		{"legacy sync plan, lane off", laneLineCfg("plan", "off"), laneLineStore(t, "new", nil)},
		{"lane plan, one plan run", laneLineCfg("off", "plan"), laneLineStore(t, "new", func(t *testing.T, st *store.Store) {
			recordRun(t, st, "plan", "plan", intPtr(3), at(8))
		})},
		{"lane apply, not flipped yet", laneLineCfg("off", "apply"), laneLineStore(t, "new", func(t *testing.T, st *store.Store) {
			recordRun(t, st, "plan", "plan", intPtr(3), at(8))
		})},
		{"lane apply, flipped, residual left", laneLineCfg("off", "apply"), laneLineStore(t, "new", func(t *testing.T, st *store.Store) {
			recordRun(t, st, "apply", "apply", intPtr(2), at(9))
		})},
		{"lane apply, flipped, residual cleared", laneLineCfg("off", "apply"), laneLineStore(t, "new", func(t *testing.T, st *store.Store) {
			recordRun(t, st, "apply", "apply", intPtr(2), at(9))
			recordRun(t, st, "apply", "apply", intPtr(0), at(9))
		})},
		{"lane apply, by-hand plan run after the flip", laneLineCfg("off", "apply"), laneLineStore(t, "new", func(t *testing.T, st *store.Store) {
			recordRun(t, st, "apply", "apply", intPtr(2), at(9))
			recordRun(t, st, "apply", "plan", intPtr(7), time.Date(2026, 10, 2, 9, 30, 0, 0, time.UTC))
		})},
		{"lane plan after a rollback from apply", laneLineCfg("off", "plan"), laneLineStore(t, "new", func(t *testing.T, st *store.Store) {
			recordRun(t, st, "apply", "apply", intPtr(2), at(9))
			recordRun(t, st, "plan", "plan", intPtr(1), at(9))
		})},
	}
}

// TestPRLaneLineGolden pins the exact text of the live pr lane line.
// UPDATE_GOLDEN=1 rewrites the golden file.
func TestPRLaneLineGolden(t *testing.T) {
	var b strings.Builder
	for _, tc := range prLaneLineCases(t) {
		b.WriteString(tc.name + " => " + prLaneLine(tc.cfg, tc.st, laneLineNow) + "\n")
	}
	got := b.String()
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(prLaneLineGolden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(prLaneLineGolden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(prLaneLineGolden)
	if err != nil {
		t.Fatalf("read golden: %v (UPDATE_GOLDEN=1 writes it)", err)
	}
	if string(want) != got {
		t.Errorf("pr lane line drifted from %s:\n got:\n%s\nwant:\n%s", prLaneLineGolden, got, want)
	}
}

// TestPRLaneLineNeverSaysTimeSinceTheLastApply guards the design's ban: the
// line has no "since the last decider apply" figure, which is legitimately
// large in a quiet window and has no source in pg-desk.
func TestPRLaneLineNeverSaysTimeSinceTheLastApply(t *testing.T) {
	for _, tc := range prLaneLineCases(t) {
		line := prLaneLine(tc.cfg, tc.st, laneLineNow)
		for _, bad := range []string{"since the last", "last apply", "last decider"} {
			if strings.Contains(line, bad) {
				t.Errorf("%s: line %q contains %q", tc.name, line, bad)
			}
		}
	}
}

// TestStatusAndDoctorPrintTheLiveLaneLine runs the real status and doctor
// verbs against a real schema-2 store holding recorded lane state: each prints
// the one line, with the flip time, counts and last run read from the store,
// and the configured lane mode read from the one configuration key.
func TestStatusAndDoctorPrintTheLiveLaneLine(t *testing.T) {
	origNow := changesNow
	t.Cleanup(func() { changesNow = origNow })
	changesNow = func() time.Time { return laneLineNow }

	cfg := laneLineCfg("off", "apply")
	path := storeAtVersion(t, "new")
	seed, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	recordRun(t, seed, "apply", "apply", intPtr(2), time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC))
	_ = seed.Close()
	open := func() (*store.Store, error) { return store.Open(path) }
	withOpenSeams(t, cfg, open)
	stubDoctorSeams(t, nil, nil, nil)

	const want = "pr lane: legacy sync off, lane apply, flipped 2026-10-02T09:00:00Z, never-hydrated 1 (oldest 394h0m0s), residual 1 (oldest 1h0m0s), last run 2026-10-02T09:00:00Z"
	statusOut, err := runStatusCmd(t)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !hasExactLine(statusOut, want) {
		t.Errorf("status output lacks the line %q:\n%s", want, statusOut)
	}
	doctorOut, _ := runDoctorCmd(t) // its other checks need a real pg-connector; the line is printed regardless
	if !hasExactLine(doctorOut, want) {
		t.Errorf("doctor output lacks the line %q:\n%s", want, doctorOut)
	}
	// The line is information: it never adds a doctor failure of its own.
	if strings.Contains(doctorOut, "pr lane: FAIL") {
		t.Errorf("doctor treats the lane line as a check:\n%s", doctorOut)
	}
}

func hasExactLine(out, line string) bool {
	for _, l := range strings.Split(out, "\n") {
		if l == line {
			return true
		}
	}
	return false
}

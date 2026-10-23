package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

const prLaneLineGolden = "testdata/pr_lane_line.golden.txt"

// prLaneLineCases is the matrix the golden file pins: every legacy sync mode
// against every lane mode, a missing mode (reads as off), an invalid lane mode
// (reads as off) and an unavailable configuration.
func prLaneLineCases() []struct {
	name string
	cfg  *config.Config
} {
	mk := func(syncMode, laneMode string) *config.Config {
		c := openTestConfig("o/r")
		c.Sync.Mode = syncMode
		c.PRLane.Mode = laneMode
		return c
	}
	return []struct {
		name string
		cfg  *config.Config
	}{
		{"sync off, lane off", mk("off", "off")},
		{"sync plan, lane off", mk("plan", "off")},
		{"sync apply, lane off", mk("apply", "off")},
		{"sync off, lane plan", mk("off", "plan")},
		{"sync off, lane apply", mk("off", "apply")},
		{"nothing set", mk("", "")},
		{"invalid lane mode", mk("off", "aplly")},
		{"no configuration", nil},
	}
}

// TestPRLaneLineGolden pins the exact text of the static pr lane line.
// UPDATE_GOLDEN=1 rewrites the golden file.
func TestPRLaneLineGolden(t *testing.T) {
	var b strings.Builder
	for _, tc := range prLaneLineCases() {
		b.WriteString(tc.name + " => " + prLaneLine(tc.cfg) + "\n")
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

// TestStatusAndDoctorPrintThePRLaneLine runs the real status and doctor verbs
// against a store with the configuration that says legacy sync off and lane off
// (the interim state), and with a configuration that sets the lane to plan: each
// prints the line the golden pins, read from the configuration key.
func TestStatusAndDoctorPrintThePRLaneLine(t *testing.T) {
	for _, tc := range []struct{ syncMode, laneMode, want string }{
		{"off", "off", "pr lane: legacy sync off, lane off"},
		{"off", "plan", "pr lane: legacy sync off, lane plan"},
	} {
		t.Run(tc.laneMode, func(t *testing.T) {
			cfg := openTestConfig("o/r")
			cfg.Sync.Mode = tc.syncMode
			cfg.PRLane.Mode = tc.laneMode
			if got := prLaneLine(cfg); got != tc.want {
				t.Fatalf("prLaneLine = %q, want %q", got, tc.want)
			}
			golden, err := os.ReadFile(prLaneLineGolden)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(golden), "=> "+tc.want+"\n") {
				t.Fatalf("the golden does not pin %q", tc.want)
			}

			_, openFresh := openTestStore(t)
			withOpenSeams(t, cfg, openFresh)
			stubDoctorSeams(t, nil, nil, nil)
			origLedger := statusRunPgConnectorLedgerShow
			t.Cleanup(func() { statusRunPgConnectorLedgerShow = origLedger })
			statusRunPgConnectorLedgerShow = func(context.Context) (string, error) { return "(empty)\n", nil }

			statusOut, err := runStatusCmd(t)
			if err != nil {
				t.Fatalf("status: %v", err)
			}
			if !hasExactLine(statusOut, tc.want) {
				t.Errorf("status output lacks the line %q:\n%s", tc.want, statusOut)
			}
			doctorOut, _ := runDoctorCmd(t) // its other checks need a real pg-connector; the line is printed regardless
			if !hasExactLine(doctorOut, tc.want) {
				t.Errorf("doctor output lacks the line %q:\n%s", tc.want, doctorOut)
			}
		})
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

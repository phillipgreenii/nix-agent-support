package main

import (
	"strings"
	"testing"
)

func TestCheckNeedsInputEmptyRows(t *testing.T) {
	if got := checkNeedsInput(poolRef{Label: ambientPoolLabel}, nil); len(got) != 0 {
		t.Fatalf("expected no findings for empty input, got %v", got)
	}
}

func TestCheckNeedsInputOneRow(t *testing.T) {
	rows := []ccpoolSessionRow{{ExternalID: "sess-1", Name: "worker", State: "needs_input", Live: true, CWD: "/tmp/w1"}}
	got := checkNeedsInput(poolRef{Label: ambientPoolLabel}, rows)
	if len(got) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(got))
	}
	f := got[0]
	if f.Kind != kindNeedsInput {
		t.Errorf("kind = %v, want %v", f.Kind, kindNeedsInput)
	}
	if f.Fingerprint != "needs-input:sess-1" {
		t.Errorf("fingerprint = %q", f.Fingerprint)
	}
	if f.State != "needs_input" {
		t.Errorf("state = %q, want needs_input", f.State)
	}
}

func TestCheckNeedsInputMultipleRowsIndependentFindings(t *testing.T) {
	rows := []ccpoolSessionRow{
		{ExternalID: "sess-1", State: "needs_input", Live: true},
		{ExternalID: "sess-2", State: "needs_input", Live: true},
	}
	got := checkNeedsInput(poolRef{Label: ambientPoolLabel}, rows)
	if len(got) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(got))
	}
	if got[0].Fingerprint == got[1].Fingerprint {
		t.Fatalf("expected distinct fingerprints per session, got %q twice", got[0].Fingerprint)
	}
}

func TestClassifyZombieBandNoGrowthIsBaseline(t *testing.T) {
	band, streak := classifyZombieBand(10, 10, 2)
	if band != bandBaseline {
		t.Errorf("band = %v, want bandBaseline", band)
	}
	if streak != 0 {
		t.Errorf("streak = %d, want 0 (reset on no-growth)", streak)
	}
	// Shrinking is treated the same as no growth.
	band, streak = classifyZombieBand(10, 5, 2)
	if band != bandBaseline || streak != 0 {
		t.Errorf("shrink: band=%v streak=%d, want bandBaseline/0", band, streak)
	}
}

func TestClassifyZombieBandZeroBaselineAnyGrowthIsFullJump(t *testing.T) {
	band, streak := classifyZombieBand(0, 1, 0)
	if band != band100Percent {
		t.Errorf("band = %v, want band100Percent", band)
	}
	if streak != 1 {
		t.Errorf("streak = %d, want 1", streak)
	}
}

func TestClassifyZombieBandRatioThresholds(t *testing.T) {
	// previous=10: +5 is 50% growth -> band50Percent.
	if band, _ := classifyZombieBand(10, 15, 0); band != band50Percent {
		t.Errorf("50%% growth: band = %v, want band50Percent", band)
	}
	// previous=10: +10 is 100% growth -> band100Percent.
	if band, _ := classifyZombieBand(10, 20, 0); band != band100Percent {
		t.Errorf("100%% growth: band = %v, want band100Percent", band)
	}
	// previous=10: +1 is 10% growth, below the 50% floor -> bandBaseline
	// (no alert yet), but the streak still increments.
	band, streak := classifyZombieBand(10, 11, 0)
	if band != bandBaseline {
		t.Errorf("10%% growth: band = %v, want bandBaseline", band)
	}
	if streak != 1 {
		t.Errorf("10%% growth: streak = %d, want 1 (still counts toward sustained-growth)", streak)
	}
}

func TestClassifyZombieBandSustainedGrowthOverridesRatio(t *testing.T) {
	// Three consecutive small (sub-50%) growth runs in a row -> sustained,
	// even though no single run crossed the 50%/100% ratio thresholds.
	band, streak := classifyZombieBand(10, 11, 2) // this would be the 3rd consecutive growth run
	if band != bandSustained {
		t.Errorf("band = %v, want bandSustained", band)
	}
	if streak != 3 {
		t.Errorf("streak = %d, want 3", streak)
	}
}

func TestCheckZombieDriftNoPriorBaselineIsQuiet(t *testing.T) {
	f, streak := checkZombieDrift(false, 0, 5, 0)
	if f != nil {
		t.Fatalf("expected no finding with no prior baseline, got %+v", f)
	}
	if streak != 0 {
		t.Fatalf("streak = %d, want 0", streak)
	}
}

func TestCheckZombieDriftNoGrowthProducesNoFinding(t *testing.T) {
	f, streak := checkZombieDrift(true, 10, 10, 1)
	if f != nil {
		t.Fatalf("expected no finding for unchanged count, got %+v", f)
	}
	if streak != 0 {
		t.Fatalf("streak = %d, want 0 (reset)", streak)
	}
}

func TestCheckZombieDriftGrowthProducesFinding(t *testing.T) {
	f, streak := checkZombieDrift(true, 10, 20, 0)
	if f == nil {
		t.Fatalf("expected a finding for 100%% growth")
	}
	if f.Kind != kindZombieDrift {
		t.Errorf("kind = %v, want kindZombieDrift", f.Kind)
	}
	if f.Fingerprint != "zombie-count:+100%" {
		t.Errorf("fingerprint = %q", f.Fingerprint)
	}
	if f.State != string(band100Percent) {
		t.Errorf("state = %q", f.State)
	}
	if streak != 1 {
		t.Errorf("streak = %d, want 1", streak)
	}
}

// TestCountZombieSessions pins the zombie definition (pg2-d845f): ONLY
// state == working && !live counts. errored history grows without bound
// and a dead ready/starting row never reached a turn, so neither counts;
// no live row counts.
func TestCountZombieSessions(t *testing.T) {
	cases := []struct {
		state string
		live  bool
		want  int
	}{
		{"working", false, 1},
		{"working", true, 0},
		{"errored", false, 0},
		{"errored", true, 0},
		{"idle", false, 0},
		{"idle", true, 0},
		{"ready", false, 0},
		{"ready", true, 0},
		{"starting", false, 0},
		{"starting", true, 0},
		{"needs_input", false, 0},
		{"needs_input", true, 0},
		{"done", false, 0},
	}
	for _, c := range cases {
		name := c.state + "/dead"
		if c.live {
			name = c.state + "/live"
		}
		t.Run(name, func(t *testing.T) {
			if got := countZombieSessions([]ccpoolSessionRow{{State: c.state, Live: c.live}}); got != c.want {
				t.Fatalf("countZombieSessions(%s live=%v) = %d, want %d", c.state, c.live, got, c.want)
			}
		})
	}
}

func TestCountZombieSessionsSumsOnlyWorkingAndDead(t *testing.T) {
	rows := []ccpoolSessionRow{
		{State: "working", Live: false},
		{State: "working", Live: false},
		{State: "working", Live: true},
		{State: "errored", Live: false},
		{State: "ready", Live: false},
	}
	if got := countZombieSessions(rows); got != 2 {
		t.Fatalf("got %d, want 2", got)
	}
}

// TestCheckNeedsInputIgnoresDeadRows: a needs_input row whose process is
// gone is dead history, not a stuck session (pg2-d845f).
func TestCheckNeedsInputIgnoresDeadRows(t *testing.T) {
	rows := []ccpoolSessionRow{
		{ExternalID: "dead-1", State: "needs_input", Live: false},
		{ExternalID: "live-1", State: "needs_input", Live: true},
	}
	got := checkNeedsInput(poolRef{Label: ambientPoolLabel}, rows)
	if len(got) != 1 || got[0].Fingerprint != "needs-input:live-1" {
		t.Fatalf("expected only the live row to be reported, got %+v", got)
	}
}

func TestCheckNeedsInputNamesNamedPool(t *testing.T) {
	pool := poolRef{Label: "pg-router-ccpool-review", Dir: "/pools/pg-router-ccpool-review"}
	got := checkNeedsInput(pool, []ccpoolSessionRow{{ExternalID: "sess-1", State: "needs_input", Live: true}})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(got))
	}
	if got[0].Fingerprint != "needs-input:pg-router-ccpool-review:sess-1" {
		t.Errorf("fingerprint = %q", got[0].Fingerprint)
	}
	if !strings.Contains(got[0].Summary, "pg-router-ccpool-review") || !strings.Contains(got[0].Evidence, "pool=pg-router-ccpool-review") {
		t.Errorf("finding does not name its pool: %+v", got[0])
	}
}

func TestCheckNeverPrompted(t *testing.T) {
	pool := poolRef{Label: "pg-router-ccpool-worker", Dir: "/pools/pg-router-ccpool-worker"}
	rows := []ccpoolSessionRow{
		{ExternalID: "old", State: "ready", Live: true},      // seen last run -> finding
		{ExternalID: "new", State: "ready", Live: true},      // first sighting -> no finding
		{ExternalID: "dead", State: "ready", Live: false},    // not live -> ignored
		{ExternalID: "busy", State: "working", Live: true},   // prompted -> ignored
		{ExternalID: "gone-idle", State: "idle", Live: true}, // ran a turn -> ignored
	}
	prev := map[string]bool{
		readyKey(pool, "old"):  true,
		readyKey(pool, "dead"): true,
		readyKey(pool, "busy"): true,
	}
	findings, current := checkNeverPrompted(pool, rows, prev)
	if len(findings) != 1 || findings[0].Kind != kindNeverPrompted {
		t.Fatalf("expected exactly the one repeat-ready live session, got %+v", findings)
	}
	if findings[0].Fingerprint != "never-prompted:pg-router-ccpool-worker:old" {
		t.Errorf("fingerprint = %q", findings[0].Fingerprint)
	}
	if len(current) != 2 {
		t.Errorf("current ready set must hold both live ready sessions, got %v", current)
	}
}

func TestCheckNeverPromptedNoBaselineIsQuiet(t *testing.T) {
	pool := poolRef{Label: ambientPoolLabel}
	findings, current := checkNeverPrompted(pool, []ccpoolSessionRow{{ExternalID: "a", State: "ready", Live: true}}, nil)
	if len(findings) != 0 || len(current) != 1 {
		t.Fatalf("findings=%v current=%v", findings, current)
	}
}

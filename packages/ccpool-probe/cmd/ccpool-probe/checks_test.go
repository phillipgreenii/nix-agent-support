package main

import "testing"

func TestCheckNeedsInputEmptyRows(t *testing.T) {
	if got := checkNeedsInput(nil); len(got) != 0 {
		t.Fatalf("expected no findings for empty input, got %v", got)
	}
}

func TestCheckNeedsInputOneRow(t *testing.T) {
	rows := []ccpoolSessionRow{{ExternalID: "sess-1", Name: "worker", State: "needs_input", CWD: "/tmp/w1"}}
	got := checkNeedsInput(rows)
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
		{ExternalID: "sess-1", State: "needs_input"},
		{ExternalID: "sess-2", State: "needs_input"},
	}
	got := checkNeedsInput(rows)
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

func TestCountZombieSessions(t *testing.T) {
	rows := []ccpoolSessionRow{
		{State: "working"},
		{State: "errored"},
		{State: "idle"},
		{State: "needs_input"},
	}
	if got := countZombieSessions(rows); got != 2 {
		t.Fatalf("got %d, want 2", got)
	}
}

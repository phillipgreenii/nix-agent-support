package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckGrafanaAlertsEmpty(t *testing.T) {
	findings := checkGrafanaAlerts(nil)
	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %d", len(findings))
	}
}

func TestCheckGrafanaAlertsOnePerAlert(t *testing.T) {
	alerts := []grafanaAlert{
		{RuleUID: "pg-router-liveness-down", Labels: map[string]string{"instance": "a"}, State: "active", EpisodeCount: 3},
		{RuleUID: "pg-router-failure-rate", Labels: map[string]string{"instance": "b"}, State: "active"},
	}
	findings := checkGrafanaAlerts(alerts)
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findings))
	}
	if findings[0].Kind != kindGrafanaAlert {
		t.Fatalf("got kind %q", findings[0].Kind)
	}
	if findings[0].Fingerprint != "pg-router-liveness-down|instance=a" {
		t.Fatalf("got fingerprint %q", findings[0].Fingerprint)
	}
	if findings[0].EpisodeCount != 3 {
		t.Fatalf("got episode count %d", findings[0].EpisodeCount)
	}
	if len(findings[0].Aliases) != 1 || findings[0].Aliases[0] != findings[0].Fingerprint {
		t.Fatalf("expected aliases to include own fingerprint, got %v", findings[0].Aliases)
	}
}

func TestClassifyBand(t *testing.T) {
	cases := []struct {
		value int
		want  severityBand
	}{
		{0, bandNone},
		{9, bandNone},
		{10, bandLow},
		{49, bandLow},
		{50, bandMedium},
		{199, bandMedium},
		{200, bandHigh},
		{10000, bandHigh},
	}
	for _, c := range cases {
		if got := classifyBand(c.value); got != c.want {
			t.Errorf("classifyBand(%d) = %q, want %q", c.value, got, c.want)
		}
	}
}

func TestCheckQueueGrowthNoPriorBaseline(t *testing.T) {
	if f := checkQueueGrowth("queue-depth", false, 0, 500); f != nil {
		t.Fatalf("expected nil finding with no prior baseline, got %+v", f)
	}
}

func TestCheckQueueGrowthNotGrowing(t *testing.T) {
	if f := checkQueueGrowth("queue-depth", true, 500, 500); f != nil {
		t.Fatalf("expected nil finding when value did not grow, got %+v", f)
	}
	if f := checkQueueGrowth("queue-depth", true, 500, 100); f != nil {
		t.Fatalf("expected nil finding when value shrank, got %+v", f)
	}
}

func TestCheckQueueGrowthStillNoneBand(t *testing.T) {
	if f := checkQueueGrowth("queue-depth", true, 1, 5); f != nil {
		t.Fatalf("expected nil finding while still in the none band, got %+v", f)
	}
}

func TestCheckQueueGrowthRealFinding(t *testing.T) {
	f := checkQueueGrowth("backlog", true, 5, 60)
	if f == nil {
		t.Fatalf("expected a finding")
	}
	if f.Kind != kindQueueGrowth {
		t.Fatalf("got kind %q", f.Kind)
	}
	if f.Fingerprint != "queue-growth:backlog" {
		t.Fatalf("got fingerprint %q", f.Fingerprint)
	}
	if f.State != string(bandMedium) {
		t.Fatalf("got state %q, want %q", f.State, bandMedium)
	}
}

func TestCheckBinaryHashNoPriorBaseline(t *testing.T) {
	if f := checkBinaryHash(false, "", "deadbeef", false); f != nil {
		t.Fatalf("expected nil finding with no prior baseline, got %+v", f)
	}
}

func TestCheckBinaryHashUnchanged(t *testing.T) {
	if f := checkBinaryHash(true, "abc", "abc", false); f != nil {
		t.Fatalf("expected nil finding when hash unchanged, got %+v", f)
	}
}

func TestCheckBinaryHashChangedButDeployExpected(t *testing.T) {
	if f := checkBinaryHash(true, "abc", "def", true); f != nil {
		t.Fatalf("expected nil finding when the deploy record covers the new hash, got %+v", f)
	}
}

func TestCheckBinaryHashChangedUnexpectedly(t *testing.T) {
	f := checkBinaryHash(true, "abc", "def", false)
	if f == nil {
		t.Fatalf("expected a finding")
	}
	if f.Kind != kindBinaryHashMismatch {
		t.Fatalf("got kind %q", f.Kind)
	}
	if f.Fingerprint != binaryHashMismatchFingerprint {
		t.Fatalf("got fingerprint %q", f.Fingerprint)
	}
	if f.State != "def" {
		t.Fatalf("got state %q", f.State)
	}
}

func TestHashFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(path, []byte("hello world"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := hashFile(path)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	// sha256("hello world") -- verified via `printf 'hello world' | shasum -a 256`
	want := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHashFileMissing(t *testing.T) {
	_, err := hashFile(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatalf("expected an error for a missing file")
	}
}

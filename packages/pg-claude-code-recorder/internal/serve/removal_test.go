package serve

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// A restart reads the last result file back, so the timestamp is not "never".
func TestDoctorGaugesSurviveARestart(t *testing.T) {
	e := newEnv(t, false)
	e.cfg.DoctorInterval = time.Hour
	d := e.open(t)
	if _, err := d.doc.Doctor(); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d2 := e.open(t)
	defer func() { _ = d2.Close() }()
	if v, _ := d2.Obs().Reg.Gauge(obs.MDoctorLastRun, nil); v <= 0 {
		t.Fatalf("doctor_last_run_timestamp after a restart = %v", v)
	}
}

func TestStandardSeriesAreRegisteredAtZero(t *testing.T) {
	o := obs.New(nil)
	for _, g := range []string{obs.MQuarantineFiles, obs.MQuarantineBytes, obs.MQuarantineOldestAge, obs.MRemovalBacklogConvs, obs.MRemovalBacklogBytes, obs.MDoctorLastRun, obs.MDoctorMismatchFiles} {
		if v, ok := o.Reg.Gauge(g, nil); !ok || v != 0 {
			t.Errorf("gauge %s: %v %v", g, v, ok)
		}
	}
	if v, ok := o.Reg.Counter(obs.MQuarantineMismatch, nil); !ok || v != 0 {
		t.Errorf("removal_quarantine_mismatch_total %v %v", v, ok)
	}
	for _, r := range obs.SkipReasons {
		if v, ok := o.Reg.Counter(obs.MSkippedFiles, map[string]string{"reason": r}); !ok || v != 0 {
			t.Errorf("skipped_files_total{reason=%s} %v %v", r, v, ok)
		}
	}
	for _, r := range obs.AllReasons {
		if _, ok := o.Reg.Counter(obs.MDroppedBytes, map[string]string{"reason": r}); !ok {
			t.Errorf("dropped_bytes_total{reason=%s} not registered", r)
		}
	}
}

// The projection check runs at start, once the file set is known: a cycle the
// budget and slice interval project past doctor.interval fails config
// validation unless doctor.allow_slow_cycle is set.
func TestStartFailsWhenTheProjectedDoctorCycleExceedsTheInterval(t *testing.T) {
	e := newEnv(t, false)
	e.transcript("a.jsonl", "{\"n\":1}\n{\"n\":2}\n", time.Now().Add(-time.Hour))
	d := e.open(t)
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	// 16 copy bytes at 1 byte per slice: 16 slices of an hour against a 2 h interval
	e.cfg.DoctorBytesPerSlice = 1
	e.cfg.DoctorSliceEvery = time.Hour
	e.cfg.DoctorInterval = 2 * time.Hour
	_, err := Open(Options{Config: e.cfg, Logger: NewLogger(e.log), NoEvents: true, Version: "test", Procs: e.procs})
	if err == nil || !strings.Contains(err.Error(), "doctor.allow_slow_cycle") {
		t.Fatalf("Open = %v, want the projection failure", err)
	}
	e.cfg.DoctorAllowSlowCycle = true
	d2 := e.open(t)
	defer func() { _ = d2.Close() }()
	if v, _ := d2.Obs().Reg.Gauge(obs.MDoctorProjectedCycle, nil); v != 16*3600 {
		t.Fatalf("doctor_projected_cycle_seconds %v, want %d", v, 16*3600)
	}
	// bytes_per_slice = 0 is the old path: no projection, no failure
	e.cfg.DoctorAllowSlowCycle = false
	e.cfg.DoctorBytesPerSlice = 0
	_ = d2.Close()
	d3 := e.open(t)
	_ = d3.Close()
}

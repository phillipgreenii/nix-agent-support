package serve

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// A restart reads the last result file back, so the timestamp is not "never".
func TestDoctorGaugesSurviveARestart(t *testing.T) {
	e := newEnv(t, false)
	e.cfg.DoctorInterval = time.Hour
	d := e.open(t)
	if _, err := d.Settler().Doctor(); err != nil {
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

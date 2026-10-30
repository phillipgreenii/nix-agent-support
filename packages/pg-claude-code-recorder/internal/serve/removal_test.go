package serve

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/settle"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// doctor runs from a timer inside the daemon (doctor.interval), writes its result
// file and exports it, with no external scheduler.
func TestDoctorRunsFromATimerInsideTheDaemon(t *testing.T) {
	e := newEnv(t, false)
	e.transcript("a.jsonl", "{\"n\":1}\n", time.Now())
	e.cfg.DoctorInterval = 50 * time.Millisecond
	d := e.open(t)
	if v, ok := d.Obs().Reg.Gauge(obs.MDoctorLastRun, nil); !ok || v != 0 {
		t.Fatalf("doctor_last_run_timestamp not registered at zero: %v %v", v, ok)
	}
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	waitFor(t, "a doctor run", func() bool {
		_, ok := settle.LoadDoctor(e.cfg.StateDir)
		return ok
	})
	waitFor(t, "the doctor gauges", func() bool {
		v, _ := d.Obs().Reg.Gauge(obs.MDoctorLastRun, nil)
		return v > 0
	})
	if v, ok := d.Obs().Reg.Gauge(obs.MDoctorMismatchFiles, nil); !ok || v != 0 {
		t.Fatalf("doctor_mismatch_files %v %v", v, ok)
	}
	if _, err := os.Stat(filepath.Join(e.cfg.StateDir, settle.DoctorFile)); err != nil {
		t.Fatal(err)
	}
}

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

func TestRemovalOperationsOverTheSocket(t *testing.T) {
	e := newEnv(t, false)
	e.transcript("a.jsonl", "{\"n\":1}\n", time.Now())
	d := e.open(t)
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	c := client.New(e.cfg.Socket, "example-check")
	var rep settle.RemoveReport
	if err := c.Post(context.Background(), "remove", RemoveRequest{DryRun: true}, &rep); err != nil || rep.Enabled || !rep.DryRun {
		t.Fatalf("dry run while disabled: %v %+v", err, rep)
	}
	err := c.Post(context.Background(), "remove", RemoveRequest{Sessions: []string{"0a1b2c3d-0000-4000-8000-000000000001"}}, &rep)
	if we, ok := err.(*wire.Error); !ok || we.Name != wire.ErrRemovalDisabled {
		t.Fatalf("remove while disabled: %v", err)
	}
	var held HeldAnswer
	if err := c.Post(context.Background(), "held", HeldRequest{}, &held); err != nil || len(held.Held) != 0 {
		t.Fatalf("held: %v %+v", err, held)
	}
	err = c.Post(context.Background(), "where", WhereRequest{Session: "0a1b2c3d-0000-4000-8000-000000000001"}, &settle.WhereInfo{})
	if we, ok := err.(*wire.Error); !ok || we.Name != wire.ErrNotFound {
		t.Fatalf("where unknown: %v", err)
	}
	err = c.Post(context.Background(), "extract", ExtractRequest{Session: "0a1b2c3d-0000-4000-8000-000000000001"}, &settle.ExtractResult{})
	if we, ok := err.(*wire.Error); !ok || we.Name != wire.ErrNotFound {
		t.Fatalf("extract unknown: %v", err)
	}
	var doc settle.DoctorResult
	if err := c.Post(context.Background(), "doctor", struct{}{}, &doc); err != nil || len(doc.Mismatches) != 0 {
		t.Fatalf("doctor: %v %+v", err, doc)
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

package serve

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/settle"
)

// A phase logs its end with its duration; a long phase also logs its start, so
// a hang shows while it happens; the total follows. The fake clock is advanced
// inside the phase, so nothing sleeps.
func TestStartupTimerLogsPhasesAndTotal(t *testing.T) {
	d, clk, buf := tickEnv()
	su := newStartupTimer(d.obs, clk)
	done := su.begin("quick", false)
	clk.Advance(20 * time.Millisecond)
	done()
	done = su.begin("store_replay", true)
	if got := logLines(t, buf, obs.EvStartupPhaseBegin); len(got) != 1 || got[0]["phase"] != "store_replay" {
		t.Fatalf("no begin line while the long phase runs: %v", got)
	}
	clk.Advance(4 * time.Minute)
	done()
	su.complete()

	phases := logLines(t, buf, obs.EvStartupPhase)
	if len(phases) != 2 || phases[0]["phase"] != "quick" || phases[0]["duration_ms"] != "20" ||
		phases[1]["phase"] != "store_replay" || phases[1]["duration_ms"] != "240000" {
		t.Fatalf("startup_phase lines %v", phases)
	}
	if got := logLines(t, buf, obs.EvStartupPhaseBegin); len(got) != 1 {
		t.Fatalf("a quick phase must not log a begin line: %v", got)
	}
	c := logLines(t, buf, obs.EvStartupComplete)
	if len(c) != 1 || c[0]["duration_ms"] != "240020" || c[0]["phases"] != "2" {
		t.Fatalf("startup_complete %v", c)
	}
}

// Opening a real daemon logs one startup_phase per open phase, in order, and
// no startup_complete yet (that comes when Start is serving).
func TestDaemonOpenLogsEveryOpenPhase(t *testing.T) {
	e := newEnv(t, false)
	var buf bytes.Buffer
	lg := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	d, err := Open(Options{Config: e.cfg, Logger: lg, NoEvents: true, Version: "test", Procs: e.procs})
	if err != nil {
		t.Fatal(err)
	}
	e.d = d
	t.Cleanup(func() { _ = d.Close() })

	var got []string
	for _, l := range logLines(t, &buf, obs.EvStartupPhase) {
		got = append(got, l["phase"].(string))
		if _, ok := l["duration_ms"]; !ok {
			t.Errorf("phase %v has no duration_ms", l["phase"])
		}
	}
	want := []string{
		"state_dir", "store_journal_read", "store_open_db", "store_replay", "copier_recover", "doctor_gauges",
		"settle_recover", "watcher_new",
	}
	if len(got) != len(want) {
		t.Fatalf("phases %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("phases %v, want %v", got, want)
		}
	}
	if c := logLines(t, &buf, obs.EvStartupComplete); len(c) != 0 {
		t.Fatalf("startup_complete before Start: %v", c)
	}
}

// A tick over the slow threshold logs its phase breakdown beside the warning.
func TestRunTickSlowLogsPhaseBreakdown(t *testing.T) {
	d, clk, buf := tickEnv()
	d.tickPhases = func() settle.TickPhases {
		return settle.TickPhases{Reads: 2 * time.Second, Prefilter: 3 * time.Second, Evaluate: 80 * time.Second, Removal: time.Second, Refresh: 4 * time.Second}
	}
	d.runTick(tickKindWake, func() (settle.TickStats, error) {
		clk.Advance(90 * time.Second)
		return settle.TickStats{Examined: 8031}, nil
	})
	l := logLines(t, buf, obs.EvSettleTickSlow)
	if len(l) != 1 {
		t.Fatalf("%d settle_tick_slow lines", len(l))
	}
	for k, want := range map[string]float64{"duration_ms": 90000, "examined": 8031, "reads_ms": 2000, "prefilter_ms": 3000, "evaluate_ms": 80000, "removal_ms": 1000, "refresh_ms": 4000} {
		if l[0][k] != want {
			t.Errorf("%s = %v, want %v", k, l[0][k], want)
		}
	}
}

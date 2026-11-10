package serve

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/settle"
)

// tickEnv is a daemon holding only what runTick uses, with a fake clock and a
// JSON log captured in a buffer.
func tickEnv() (*Daemon, *clock.Fake, *bytes.Buffer) {
	var buf bytes.Buffer
	o := obs.New(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	clk := clock.NewFake(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))
	return &Daemon{obs: o, clock: clk}, clk, &buf
}

func logLines(t *testing.T, buf *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(err)
		}
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

func runs(d *Daemon, outcome string) float64 {
	v, _ := d.obs.Reg.Counter(obs.MLoopRuns, map[string]string{"loop": obs.LoopSettleTick, "outcome": outcome})
	return v
}

func TestRunTickSuccessRecordsMetricsAndEvent(t *testing.T) {
	d, clk, buf := tickEnv()
	stats := settle.TickStats{Examined: 7, Unchanged: 4, SettledNow: 2, Transitions: 3}
	d.runTick(tickKindWake, func() (settle.TickStats, error) { clk.Advance(1500 * time.Millisecond); return stats, nil })

	if runs(d, "ok") != 1 || runs(d, "error") != 0 {
		t.Fatalf("ok=%v error=%v", runs(d, "ok"), runs(d, "error"))
	}
	if h := d.obs.Reg.Hist(obs.MSettleTickDuration); h == nil || h.Samples != 1 {
		t.Fatalf("histogram %+v", h)
	}
	if v, ok := d.obs.Reg.Gauge(obs.MSettleTickLastDuration, nil); !ok || v != 1.5 {
		t.Fatalf("last duration %v %v", v, ok)
	}
	if v, ok := d.obs.Reg.Gauge(obs.MSettleTickLastTimestamp, nil); !ok || v <= 0 {
		t.Fatalf("last timestamp %v %v", v, ok)
	}
	if v, _ := d.obs.Reg.Counter(obs.MSettleTickExamined, nil); v != 7 {
		t.Fatalf("sessions processed per wake %v, want the 7 the pass reported", v)
	}
	if v, _ := d.obs.Reg.Counter(obs.MSettleTickTransitions, nil); v != 3 {
		t.Fatalf("transitions %v", v)
	}
	evs := logLines(t, buf, obs.EvSettleTick)
	if len(evs) != 1 {
		t.Fatalf("%d settle_tick events", len(evs))
	}
	e := evs[0]
	for k, want := range map[string]string{"outcome": "ok", "duration_ms": "1500", "examined": "7", "unchanged": "4", "settled_now": "2", "transitions": "3"} {
		if e[k] != want {
			t.Errorf("%s = %v, want %s", k, e[k], want)
		}
	}
	if len(logLines(t, buf, "settle_pass_failed")) != 0 || len(logLines(t, buf, "settle_tick_slow")) != 0 {
		t.Error("unexpected warning on a fast successful tick")
	}
}

func TestRunTickErrorRecordsErrorOutcome(t *testing.T) {
	d, _, buf := tickEnv()
	d.runTick(tickKindWake, func() (settle.TickStats, error) { return settle.TickStats{}, errors.New("boom") })
	if runs(d, "error") != 1 || runs(d, "ok") != 0 {
		t.Fatalf("ok=%v error=%v", runs(d, "ok"), runs(d, "error"))
	}
	if len(logLines(t, buf, "settle_pass_failed")) != 1 {
		t.Fatal("settle_pass_failed not logged")
	}
	evs := logLines(t, buf, obs.EvSettleTick)
	if len(evs) != 1 || evs[0]["outcome"] != "error" {
		t.Fatalf("events %v", evs)
	}
}

func TestRunTickOverSixtySecondsLogsSlow(t *testing.T) {
	d, clk, buf := tickEnv()
	d.runTick(tickKindWake, func() (settle.TickStats, error) { clk.Advance(61 * time.Second); return settle.TickStats{}, nil })
	if len(logLines(t, buf, "settle_tick_slow")) != 1 {
		t.Fatal("settle_tick_slow not logged")
	}
}

func TestRefreshGaugesBodyIsTimed(t *testing.T) {
	e := newEnv(t, false)
	d := e.open(t)
	d.refreshGauges()
	if h := d.obs.Reg.Hist(obs.MRefreshGaugesDuration); h == nil || h.Samples != 1 {
		t.Fatalf("histogram %+v", h)
	}
	if v, _ := d.obs.Reg.Counter(obs.MLoopRuns, map[string]string{"loop": obs.LoopRefreshGauges, "outcome": obs.OutcomeOK}); v != 1 {
		t.Fatalf("refresh_gauges ok runs %v", v)
	}
}

// While a tick runs, the in-flight gauge is its age; when it ends the gauge is 0.
func TestRunTickInFlightGaugeAndStuckLog(t *testing.T) {
	d, clk, buf := tickEnv()
	d.runTick(tickKindWake, func() (settle.TickStats, error) {
		clk.Advance(90 * time.Second)
		d.setTickInFlight()
		if v, _ := d.obs.Reg.Gauge(obs.MSettleTickInFlight, nil); v != 90 {
			t.Errorf("in flight %v, want 90", v)
		}
		d.logStuckTick() // what the watchdog timer runs after a minute
		return settle.TickStats{}, nil
	})
	if v, _ := d.obs.Reg.Gauge(obs.MSettleTickInFlight, nil); v != 0 {
		t.Fatalf("in flight after the tick %v, want 0", v)
	}
	ev := logLines(t, buf, obs.EvSettleTickStuck)
	if len(ev) != 1 || ev[0]["in_flight_ms"] != float64(90000) {
		t.Fatalf("stuck events %v", ev)
	}
	d.logStuckTick() // no tick running: silent
	if got := len(logLines(t, buf, obs.EvSettleTickStuck)); got != 1 {
		t.Fatalf("%d stuck events after the tick ended", got)
	}
}

// The by-kind histogram and the slow warning carry the pass kind, while the
// unlabelled histogram keeps counting every pass.
func TestRunTickRecordsDurationByKind(t *testing.T) {
	d, clk, buf := tickEnv()
	d.runTick(tickKindWake, func() (settle.TickStats, error) { clk.Advance(time.Second); return settle.TickStats{}, nil })
	d.runTick(tickKindReconcile, func() (settle.TickStats, error) { clk.Advance(90 * time.Second); return settle.TickStats{}, nil })
	reg := d.obs.Reg
	if h := reg.Hist(obs.MSettleTickDuration); h == nil || h.Samples != 2 {
		t.Fatalf("unlabelled histogram %+v, want both passes", h)
	}
	for kind, want := range map[string]uint64{"wake": 1, "reconcile": 1, "scan": 0} {
		h := reg.HistL(obs.MSettleTickKindDuration, map[string]string{"kind": kind})
		got := uint64(0)
		if h != nil {
			got = h.Samples
		}
		if got != want {
			t.Errorf("kind %s samples %d, want %d", kind, got, want)
		}
	}
	l := logLines(t, buf, obs.EvSettleTickSlow)
	if len(l) != 1 || l[0]["kind"] != "reconcile" {
		t.Fatalf("slow lines %v, want one of kind reconcile", l)
	}
}

// A slow wake logs how long it queued for the run lock and in TakeDirty, apart
// from its work phases.
func TestRunTickSlowLogsLockWaitAndDirtyPhases(t *testing.T) {
	d, clk, buf := tickEnv()
	d.tickPhases = func() settle.TickPhases {
		return settle.TickPhases{LockWait: 70 * time.Second, Dirty: 72 * time.Second, Evaluate: time.Second}
	}
	d.runTick(tickKindWake, func() (settle.TickStats, error) { clk.Advance(75 * time.Second); return settle.TickStats{}, nil })
	l := logLines(t, buf, obs.EvSettleTickSlow)
	if len(l) != 1 {
		t.Fatalf("%d settle_tick_slow lines", len(l))
	}
	for k, want := range map[string]float64{"lock_wait_ms": 70000, "dirty_ms": 72000, "evaluate_ms": 1000} {
		if l[0][k] != want {
			t.Errorf("%s = %v, want %v", k, l[0][k], want)
		}
	}
	if l[0]["kind"] != "wake" {
		t.Errorf("kind = %v", l[0]["kind"])
	}
}

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
	d.runTick(func() (settle.TickStats, error) { clk.Advance(1500 * time.Millisecond); return stats, nil })

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
		t.Fatalf("examined %v", v)
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
	d.runTick(func() (settle.TickStats, error) { return settle.TickStats{}, errors.New("boom") })
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
	d.runTick(func() (settle.TickStats, error) { clk.Advance(61 * time.Second); return settle.TickStats{}, nil })
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

package daemon

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/caffeinate"
	"github.com/phillipgreenii/pa-monitor/internal/core/power"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/otel"
)

// workingTree is a tree with one working session: Working is the zero Status,
// so the tick's keepAwake is true for it.
func workingTree() *aggregate.Tree {
	return &aggregate.Tree{Dirs: []*aggregate.Directory{{
		Path:     "/p",
		Sessions: []*aggregate.SessionView{{Session: &session.Session{SessionID: "s1", PID: 100}}},
	}}}
}

// metricInt64 returns the summed int64 value of the named counter or gauge and
// whether it was found, optionally restricted to data points whose `attr`
// attribute equals `val` (empty attr = no restriction).
func metricInt64(rm metricdata.ResourceMetrics, name, attr, val string) (int64, bool) {
	var total int64
	found := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range d.DataPoints {
					if v, ok := dp.Attributes.Value(attribute.Key(attr)); attr == "" || (ok && v.AsString() == val) {
						total += dp.Value
						found = true
					}
				}
			case metricdata.Gauge[int64]:
				for _, dp := range d.DataPoints {
					total += dp.Value
					found = true
				}
			}
		}
	}
	return total, found
}

// TestRunWith_LowBatteryReleasesKeepAwakeAndCountsIt drives the real tick loop
// with a fake battery (bead pg2-s86z9): a working session holds the assertion on
// AC; once the battery drops to 15% the subprocess is stopped, the release
// counter ticks with its reason, the active gauge reads 0 although the toggle is
// on, and the status carries the gate, the power source and the charge. When AC
// returns the hold resumes.
func TestRunWith_LowBatteryReleasesKeepAwakeAndCountsIt(t *testing.T) {
	bat := power.NewFakeReader(power.Reading{OnBattery: false, Percent: 80})
	var spawned, killed atomic.Int32
	var alive atomic.Bool
	mgr := &caffeinate.Manager{
		Grace:         time.Minute,
		Spawn:         func(int) error { spawned.Add(1); alive.Store(true); return nil },
		Kill:          func() error { killed.Add(1); alive.Store(false); return nil },
		IsAlive:       alive.Load,
		Now:           time.Now,
		Battery:       bat,
		BatteryPolicy: caffeinate.BatteryPolicy{HoldPct: 30, ReleasePct: 20, HysteresisPct: 5},
	}
	e, reader := newReaderEmitter(t)
	opts := tickOptions(t, e, &stubPoller{snapshot: func(context.Context) (*aggregate.Tree, bool, error) {
		return workingTree(), false, nil
	}})
	opts.Caffeinate = mgr
	opts.InitialCaffeinateOn = true
	opts.PlanTier = "max_5x"
	_, stop := startDaemon(t, opts)
	defer stop()

	// On AC: held.
	collectUntil(t, reader, 3*time.Second, func(rm metricdata.ResourceMetrics) bool {
		v, ok := metricInt64(rm, "pa_monitor.caffeinate.active", "", "")
		return ok && v == 1 && spawned.Load() == 1
	})

	// The charger goes away and the charge falls to 15%.
	bat.Set(power.Reading{OnBattery: true, Percent: 15})
	rm := collectUntil(t, reader, 3*time.Second, func(rm metricdata.ResourceMetrics) bool {
		v, ok := metricInt64(rm, "pa_monitor.caffeinate.battery_releases_total", "reason", caffeinate.ReasonReleaseThreshold)
		a, aok := metricInt64(rm, "pa_monitor.caffeinate.active", "", "")
		return ok && v == 1 && aok && a == 0
	})
	if killed.Load() != 1 {
		t.Errorf("Kill called %d times, want 1", killed.Load())
	}
	if got, _ := metricInt64(rm, "pa_monitor.caffeinate.battery_releases_total", "reason", caffeinate.ReasonSpawnBlocked); got < 1 {
		t.Errorf("spawn_blocked count = %d, want >= 1 (a working session was wanted)", got)
	}
	// Every later tick must not re-count the release or respawn.
	time.Sleep(50 * time.Millisecond)
	rm = collectUntil(t, reader, time.Second, func(metricdata.ResourceMetrics) bool { return true })
	if got, _ := metricInt64(rm, "pa_monitor.caffeinate.battery_releases_total", "reason", caffeinate.ReasonReleaseThreshold); got != 1 {
		t.Errorf("release_threshold count = %d after many ticks, want exactly 1", got)
	}
	if spawned.Load() != 1 {
		t.Errorf("respawned on a low battery: spawned=%d", spawned.Load())
	}

	// AC returns: the hold resumes.
	bat.Set(power.Reading{OnBattery: false, Percent: 15})
	collectUntil(t, reader, 3*time.Second, func(rm metricdata.ResourceMetrics) bool {
		a, ok := metricInt64(rm, "pa_monitor.caffeinate.active", "", "")
		return ok && a == 1 && spawned.Load() == 2
	})
}

// TestBuildStateReportsBatteryGate: the status the daemon serves carries the
// rule's verdict once a tick has recorded it, and nothing before that.
func TestBuildStateReportsBatteryGate(t *testing.T) {
	st := newSharedState()
	srv := newServer(st)

	ds := srv.buildState()
	if ds.GetCaffeinateBatteryGate() != "" || ds.CaffeinateBatteryPercent != nil {
		t.Errorf("before any tick: gate=%q percent=%v, want unreported", ds.GetCaffeinateBatteryGate(), ds.CaffeinateBatteryPercent)
	}

	st.setCaffeinateBattery(caffeinate.BatteryStatus{Gate: caffeinate.GateReleased, OnBattery: true, Percent: 15})
	ds = srv.buildState()
	if ds.GetCaffeinateBatteryGate() != "released" || !ds.GetCaffeinateOnBattery() || ds.GetCaffeinateBatteryPercent() != 15 || ds.CaffeinateBatteryPercent == nil {
		t.Errorf("gate=%q on_battery=%v percent=%v, want released/true/15", ds.GetCaffeinateBatteryGate(), ds.GetCaffeinateOnBattery(), ds.CaffeinateBatteryPercent)
	}

	st.setCaffeinateBattery(caffeinate.BatteryStatus{Gate: caffeinate.GateUnknown, Percent: power.NoPercent})
	ds = srv.buildState()
	if ds.GetCaffeinateBatteryGate() != "unknown" || ds.CaffeinateBatteryPercent != nil {
		t.Errorf("gate=%q percent=%v, want unknown with no percent", ds.GetCaffeinateBatteryGate(), ds.CaffeinateBatteryPercent)
	}
}

// TestBatteryReleaseReasonsMatchEmitter pins the emitter's list of reason label
// values to the reasons the state machine emits, so the counter's zero series
// cannot drift from the real ones.
func TestBatteryReleaseReasonsMatchEmitter(t *testing.T) {
	want := map[string]bool{caffeinate.ReasonReleaseThreshold: true, caffeinate.ReasonSpawnBlocked: true}
	if len(otel.CaffeinateBatteryReleaseReasons) != len(want) {
		t.Fatalf("emitter reasons = %v, want %v", otel.CaffeinateBatteryReleaseReasons, want)
	}
	for _, r := range otel.CaffeinateBatteryReleaseReasons {
		if !want[r] {
			t.Errorf("emitter reason %q is not a caffeinate reason", r)
		}
	}
}

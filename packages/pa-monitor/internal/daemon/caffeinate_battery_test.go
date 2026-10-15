package daemon

import (
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/caffeinate"
	"github.com/phillipgreenii/pa-monitor/internal/core/power"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
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

// TestBuildStateReportsBatteryGate: the status the daemon serves carries the
// keeper's battery verdict once a tick has recorded it, and nothing before that.
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

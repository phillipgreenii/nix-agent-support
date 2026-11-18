package metrics

import (
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func gaugeFor(t *testing.T, h *harness, role, session string) int64 {
	t.Helper()
	rm := h.collect(t)
	if !findMetricOK(rm, MetricSupervisorsPerSession) {
		return -1
	}
	g, ok := findMetric(t, rm, MetricSupervisorsPerSession).Data.(metricdata.Gauge[int64])
	if !ok {
		t.Fatalf("%s is not an int64 gauge", MetricSupervisorsPerSession)
	}
	for _, dp := range g.DataPoints {
		r, _ := dp.Attributes.Value(attribute.Key("role"))
		s, _ := dp.Attributes.Value(attribute.Key("session"))
		if r.AsString() == role && s.AsString() == session {
			return dp.Value
		}
	}
	return -1
}

// The names follow the catalog's convention (underscores, the counter exposed with
// _total by the Prometheus bridge).
func TestSupervisionMetricNames(t *testing.T) {
	if MetricReofferedAfterShutdown != "pg_router_events_reoffered_after_shutdown" {
		t.Errorf("MetricReofferedAfterShutdown = %q", MetricReofferedAfterShutdown)
	}
	if MetricSupervisorsPerSession != "pg_router_supervisors_per_session" {
		t.Errorf("MetricSupervisorsPerSession = %q", MetricSupervisorsPerSession)
	}
}

func TestOnReofferedAfterShutdown_countsPerRole(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnReofferedAfterShutdown("desk-pr")
	h.emitter.OnReofferedAfterShutdown("desk-pr")
	h.emitter.OnReofferedAfterShutdown("review")
	m := findMetric(t, h.collect(t), MetricReofferedAfterShutdown)
	if got := sumFor(m, "role", "desk-pr"); got != 2 {
		t.Errorf("desk-pr = %d, want 2", got)
	}
	if got := sumFor(m, "role", "review"); got != 1 {
		t.Errorf("review = %d, want 1", got)
	}
}

func TestOnSupervision_lastWriteWinsPerRoleAndSession(t *testing.T) {
	h := newHarness(t)
	if got := gaugeFor(t, h, "worker", "s-1"); got != -1 {
		t.Fatalf("gauge before any dispatch = %d, want unset", got)
	}
	h.emitter.OnSupervision("worker", "s-1", 2)
	h.emitter.OnSupervision("worker", "s-2", 3)
	if got := gaugeFor(t, h, "worker", "s-1"); got != 2 {
		t.Errorf("s-1 = %d, want 2", got)
	}
	h.emitter.OnSupervision("worker", "s-1", 1)
	if got := gaugeFor(t, h, "worker", "s-1"); got != 1 {
		t.Errorf("s-1 after the later dispatch = %d, want 1 (last write wins)", got)
	}
	if got := gaugeFor(t, h, "worker", "s-2"); got != 3 {
		t.Errorf("s-2 = %d, want 3 (never removed)", got)
	}
}

func TestOnSupervision_ignoresAnInvalidReport(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnSupervision("worker", "", 2)
	h.emitter.OnSupervision("worker", "s-1", 0)
	if got := gaugeFor(t, h, "worker", ""); got != -1 {
		t.Errorf("empty session recorded %d", got)
	}
	if got := gaugeFor(t, h, "worker", "s-1"); got != -1 {
		t.Errorf("supervisors 0 recorded %d", got)
	}
}

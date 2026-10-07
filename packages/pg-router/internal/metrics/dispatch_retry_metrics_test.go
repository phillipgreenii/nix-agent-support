package metrics

import (
	"testing"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// OnDispatchRetry counts per role and transient class (bead pg2-yu5y2) as the
// pg_router_dispatch_retries counter (exported with a _total suffix).
func TestOnDispatchRetryCountsPerRoleAndClass(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnDispatchRetry("desk-pr", "killed")
	h.emitter.OnDispatchRetry("desk-pr", "killed")
	h.emitter.OnDispatchRetry("desk-pr", "deadline")
	h.emitter.OnDispatchRetry("desk-issue", "killed")

	m := findMetric(t, h.collect(t), MetricDispatchRetries)
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok || !sum.IsMonotonic {
		t.Fatalf("dispatch_retries is not a monotonic counter: %#v", m.Data)
	}
	got := map[string]int64{}
	for _, dp := range sum.DataPoints {
		role, _ := dp.Attributes.Value("role")
		class, _ := dp.Attributes.Value("class")
		got[role.AsString()+"/"+class.AsString()] = dp.Value
	}
	if got["desk-pr/killed"] != 2 || got["desk-pr/deadline"] != 1 || got["desk-issue/killed"] != 1 || len(got) != 3 {
		t.Fatalf("counts = %v", got)
	}
}

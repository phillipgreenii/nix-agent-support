package metrics

import (
	"context"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
)

// hasMetric reports whether rm carries a metric named name. The SDK leaves out an
// instrument whose callback observed nothing, so presence means at least one data point.
func hasMetric(rm metricdata.ResourceMetrics, name string) bool {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return true
			}
		}
	}
	return false
}

// WithQueueReady (bead pg2-nzkjl) keeps every gauge that reads the queue silent until its
// readiness function says the queue exists. While it says no, a collect neither calls any
// function that reads the queue nor reports a series for those gauges: a scrape before
// the queue is built is not a reading, and not a zero. Once it says yes they all report.
// Instruments that do not read the queue are unaffected either way.
func TestQueueReadyGatesEveryQueueBoundGauge(t *testing.T) {
	queueBound := []string{
		MetricQueueDepth, MetricBacklog, MetricQueueOldestAge, MetricListenerInFlight,
		MetricQueueLogBytes, MetricQueueLogLimitBytes, MetricQueueLogPercent,
		MetricEmittersHalted, MetricLogRejecting, MetricActiveGates,
	}
	ready, reads := false, 0
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if _, err := New(
		mp,
		func() map[string]int { reads++; return map[string]int{"t1": 2} },
		WithQueueReady(func() bool { return ready }),
		WithQueueAges(func() map[string]time.Duration { reads++; return map[string]time.Duration{"t1": time.Second} }),
		WithListenerInFlight(func() map[string]int { reads++; return map[string]int{"r1": 1} }),
		WithQueueLogSize(func() int64 { reads++; return 10 }),
		WithLogLimitStatus(func() eventqueue.LimitStatus {
			reads++
			return eventqueue.LimitStatus{Bytes: 5, HardBytes: 10, State: eventqueue.StateOK}
		}),
		WithActiveGates(func() []eventqueue.Gate { reads++; return []eventqueue.Gate{{Type: "SYSTEM_PAUSE"}} }),
		// Not queue-bound: reports whatever the queue's state.
		WithLiveness(func() bool { return true }),
	); err != nil {
		t.Fatal(err)
	}
	collect := func() metricdata.ResourceMetrics {
		t.Helper()
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatalf("collect: %v", err)
		}
		return rm
	}

	rm := collect()
	if reads != 0 {
		t.Errorf("the queue was read %d time(s) while it was not ready, want 0", reads)
	}
	for _, name := range queueBound {
		if hasMetric(rm, name) {
			t.Errorf("%s reported while the queue was not ready; want no series", name)
		}
	}
	if !hasMetric(rm, MetricLiveness) {
		t.Errorf("%s missing while the queue was not ready; it does not read the queue", MetricLiveness)
	}

	ready = true
	rm = collect()
	for _, name := range queueBound {
		if !hasMetric(rm, name) {
			t.Errorf("%s missing once the queue was ready; want its series", name)
		}
	}
	if reads == 0 {
		t.Error("the queue was never read once ready")
	}
}

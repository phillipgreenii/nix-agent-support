package main

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/x/otelsetup"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// shutdownSignals runs observeShutdown for a clock whose context ended
// `ago` before now against `deadline`, and returns the recorded histogram
// count and the overrun counter total.
func shutdownSignals(t *testing.T, ago, deadline time.Duration) (count uint64, overruns int64) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	sig := registerCommonSignals(mp)
	if sig == nil {
		t.Fatal("registerCommonSignals returned nil")
	}
	clk := &shutdownClock{deadline: deadline}
	clk.endedAt.Store(time.Now().Add(-ago).UnixNano())
	observeShutdown(context.Background(), sig, clk)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Histogram[float64]:
				if m.Name == otelsetup.MetricShutdownDuration {
					for _, dp := range d.DataPoints {
						count += dp.Count
					}
				}
			case metricdata.Sum[int64]:
				if m.Name == otelsetup.MetricShutdownOverruns {
					for _, dp := range d.DataPoints {
						overruns += dp.Value
					}
				}
			}
		}
	}
	return count, overruns
}

func TestObserveShutdownOnTimeRecordsDurationNoOverrun(t *testing.T) {
	count, overruns := shutdownSignals(t, time.Second, time.Minute)
	if count != 1 || overruns != 0 {
		t.Fatalf("on time: duration count=%d overruns=%d, want 1 and 0", count, overruns)
	}
}

func TestObserveShutdownLateCountsOverrun(t *testing.T) {
	count, overruns := shutdownSignals(t, time.Minute, time.Second)
	if count != 1 || overruns != 1 {
		t.Fatalf("late: duration count=%d overruns=%d, want 1 and 1", count, overruns)
	}
}

func TestObserveShutdownBeforeContextEndsRecordsNothing(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	sig := registerCommonSignals(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	observeShutdown(context.Background(), sig, &shutdownClock{deadline: time.Second})
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == otelsetup.MetricShutdownDuration {
				if h, ok := m.Data.(metricdata.Histogram[float64]); ok && len(h.DataPoints) > 0 {
					t.Fatalf("duration recorded before the context ended: %+v", h.DataPoints)
				}
			}
		}
	}
}

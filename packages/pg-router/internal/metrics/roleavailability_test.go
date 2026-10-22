package metrics

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestWithRoleAvailability_OneSeriesPerRoleOneOrZero: the gauge reports 1 for an
// available role and 0 for an unavailable one, reading the function on each collect.
func TestWithRoleAvailability_OneSeriesPerRoleOneOrZero(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	avail := map[string]bool{"a": true, "b": false}
	if _, err := New(mp, func() map[string]int { return nil }, WithRoleAvailability(func() map[string]bool { return avail })); err != nil {
		t.Fatalf("New: %v", err)
	}
	read := func() map[string]int64 {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatalf("collect: %v", err)
		}
		out := map[string]int64{}
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name != MetricRoleAvailable {
					continue
				}
				for _, dp := range m.Data.(metricdata.Gauge[int64]).DataPoints {
					v, _ := dp.Attributes.Value(attribute.Key("role"))
					out[v.AsString()] = dp.Value
				}
			}
		}
		return out
	}
	got := read()
	if len(got) != 2 || got["a"] != 1 || got["b"] != 0 {
		t.Fatalf("series = %v, want a=1 b=0", got)
	}
	avail["b"] = true
	if got := read(); got["b"] != 1 {
		t.Fatalf("after recovery series = %v, want b=1 (read live)", got)
	}
}

// Without the option the gauge is not registered.
func TestWithoutRoleAvailability_NoGauge(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if _, err := New(mp, func() map[string]int { return nil }); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	_ = reader.Collect(context.Background(), &rm)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == MetricRoleAvailable {
				t.Fatal("gauge registered without the option")
			}
		}
	}
}

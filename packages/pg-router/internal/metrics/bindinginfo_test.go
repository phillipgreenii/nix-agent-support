package metrics

import (
	"context"
	"sort"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func collectBindingInfo(t *testing.T, opts ...Option) (pairs []string, registered bool) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if _, err := New(t.Context(), mp, func() map[string]int { return nil }, opts...); err != nil {
		t.Fatalf("New: %v", err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != MetricBindingInfo {
				continue
			}
			registered = true
			for _, dp := range m.Data.(metricdata.Gauge[int64]).DataPoints {
				role, _ := dp.Attributes.Value(attribute.Key("role"))
				typ, _ := dp.Attributes.Value(attribute.Key("type"))
				if dp.Value != 1 {
					t.Errorf("binding_info{%s,%s} = %d, want 1", role.AsString(), typ.AsString(), dp.Value)
				}
				pairs = append(pairs, role.AsString()+"|"+typ.AsString())
			}
		}
	}
	sort.Strings(pairs)
	return pairs, registered
}

// WithBindings reports one series of value 1 per distinct (role, type) pair,
// labelled role and type; a role may bind several types.
func TestWithBindings_OneSeriesPerPair(t *testing.T) {
	got, ok := collectBindingInfo(t, WithBindings([]Binding{
		{Role: "r1", Type: "a.ready"},
		{Role: "r1", Type: "b.ready"},
		{Role: "r2", Type: "c.ready"},
		{Role: "r2", Type: "c.ready"}, // duplicate collapses
	}))
	if !ok {
		t.Fatal("pg_router_binding_info not registered")
	}
	want := []string{"r1|a.ready", "r1|b.ready", "r2|c.ready"}
	if len(got) != len(want) {
		t.Fatalf("series = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("series = %v, want %v", got, want)
		}
	}
}

// Without the option, or with no bindings, the metric is not registered.
func TestWithoutBindings_NoMetric(t *testing.T) {
	if _, ok := collectBindingInfo(t); ok {
		t.Error("registered without the option")
	}
	if _, ok := collectBindingInfo(t, WithBindings(nil)); ok {
		t.Error("registered with no bindings")
	}
}

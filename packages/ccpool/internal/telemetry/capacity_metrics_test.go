package telemetry

import (
	"context"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func collectCapacity(t *testing.T, root string, c PoolCapacity) map[string]map[string]int64 {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = mp.Shutdown(context.Background()) }()
	if err := RecordPoolCapacityTo(context.Background(), mp.Meter("test"), root, c); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]int64{} // pool -> dim -> value
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != PoolCapacityMetric {
				t.Fatalf("unexpected metric %q", m.Name)
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("not an int64 gauge: %T", m.Data)
			}
			for _, dp := range g.DataPoints {
				if dp.Attributes.Len() != 2 {
					t.Fatalf("want exactly pool+dim attrs, got %v", dp.Attributes)
				}
				pv, _ := dp.Attributes.Value("pool")
				dv, _ := dp.Attributes.Value("dim")
				if out[pv.AsString()] == nil {
					out[pv.AsString()] = map[string]int64{}
				}
				out[pv.AsString()][dv.AsString()] = dp.Value
			}
		}
	}
	return out
}

func TestRecordPoolCapacity_DimsAndBasenamePool(t *testing.T) {
	got := collectCapacity(t, "/some/deep/path/pg-router-ccpool-review", PoolCapacity{6, 3, 1, 2, 4})
	want := map[string]int64{"max_sessions": 6, "live": 3, "preserved": 1, "counted": 2, "free": 4}
	if len(got) != 1 {
		t.Fatalf("want one pool, got %v", got)
	}
	dims := got["pg-router-ccpool-review"]
	if len(dims) != len(want) {
		t.Fatalf("dims = %v", dims)
	}
	for k, v := range want {
		if dims[k] != v {
			t.Errorf("dim %s = %d, want %d", k, dims[k], v)
		}
	}
}

func TestRecordPoolCapacity_DefaultMode(t *testing.T) {
	got := collectCapacity(t, "", PoolCapacity{})
	if _, ok := got["default"]; !ok {
		t.Fatalf("default-mode pool = %v, want \"default\"", got)
	}
}

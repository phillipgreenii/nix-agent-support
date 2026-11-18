package telemetry

import (
	"context"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestRecorderCounters_countByOp: each counter carries exactly the op attribute
// and sums per op.
func TestRecorderCounters_countByOp(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = mp.Shutdown(context.Background()) }()
	m := mp.Meter("test")
	ctx := context.Background()
	for _, op := range []string{"query", "query", "hello"} {
		if err := RecordRecorderRetryTo(ctx, m, op); err != nil {
			t.Fatal(err)
		}
	}
	if err := RecordRecorderReadFailureTo(ctx, m, "query"); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]int64{} // metric -> op -> sum
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			s, ok := mt.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s: not an int64 sum: %T", mt.Name, mt.Data)
			}
			got[mt.Name] = map[string]int64{}
			for _, dp := range s.DataPoints {
				if dp.Attributes.Len() != 1 {
					t.Fatalf("%s: want only the op attribute, got %v", mt.Name, dp.Attributes)
				}
				v, _ := dp.Attributes.Value("op")
				got[mt.Name][v.AsString()] = dp.Value
			}
		}
	}
	if r := got[RecorderRetriesMetric]; r["query"] != 2 || r["hello"] != 1 {
		t.Errorf("retries = %v; want query 2, hello 1", r)
	}
	if f := got[RecorderReadFailuresMetric]; f["query"] != 1 || len(f) != 1 {
		t.Errorf("failures = %v; want query 1 only", f)
	}
}

// TestRecordRecorder_globalProviderDoesNotPanic: the no-provider path is a no-op.
func TestRecordRecorder_globalProviderDoesNotPanic(t *testing.T) {
	RecordRecorderRetry("query")
	RecordRecorderReadFailure("query")
}

package metrics

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestOnSettleFailureCountsPerQuery(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	e, err := New(t.Context(), mp, func() map[string]int { return nil })
	if err != nil {
		t.Fatal(err)
	}
	e.OnSettleFailure("feed")
	e.OnSettleFailure("feed")
	e.OnSettleFailure("other")
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	m := findMetric(t, rm, MetricSourceSettleFailures)
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("data = %T", m.Data)
	}
	got := map[string]int64{}
	for _, dp := range sum.DataPoints {
		v, _ := dp.Attributes.Value(attribute.Key("query"))
		got[v.AsString()] = dp.Value
	}
	if got["feed"] != 2 || got["other"] != 1 {
		t.Fatalf("counts = %v", got)
	}
}

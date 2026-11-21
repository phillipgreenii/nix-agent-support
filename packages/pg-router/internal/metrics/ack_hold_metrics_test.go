package metrics

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestAckAbandonedCountsPerQueryAndType(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	e, err := New(t.Context(), sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)), func() map[string]int { return nil })
	if err != nil {
		t.Fatal(err)
	}
	e.OnAckAbandoned("feed", "a")
	e.OnAckAbandoned("feed", "a")
	e.OnAckAbandoned("feed", "b")
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	sum := findMetric(t, rm, MetricSourceAckAbandoned).Data.(metricdata.Sum[int64])
	got := map[string]int64{}
	for _, dp := range sum.DataPoints {
		q, _ := dp.Attributes.Value(attribute.Key("query"))
		ty, _ := dp.Attributes.Value(attribute.Key("type"))
		got[q.AsString()+"/"+ty.AsString()] = dp.Value
	}
	if got["feed/a"] != 2 || got["feed/b"] != 1 || len(got) != 2 {
		t.Fatalf("counts = %v", got)
	}
}

func TestFrontierAgeGaugeReportsPerLane(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	e, err := New(t.Context(), sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)), func() map[string]int { return nil })
	if err != nil {
		t.Fatal(err)
	}
	e.SetFrontierAges(func() []FrontierAge {
		return []FrontierAge{{Query: "feed", Lane: "l1", Age: 90 * time.Second}, {Query: "feed", Lane: "l2"}}
	})
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	g := findMetric(t, rm, MetricSourceFrontierAge).Data.(metricdata.Gauge[float64])
	got := map[string]float64{}
	for _, dp := range g.DataPoints {
		l, _ := dp.Attributes.Value(attribute.Key("lane"))
		got[l.AsString()] = dp.Value
	}
	if got["l1"] != 90 || got["l2"] != 0 || len(got) != 2 {
		t.Fatalf("gauge = %v", got)
	}
}

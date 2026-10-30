package telemetry

import (
	"context"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func collectOne(t *testing.T, reader sdkmetric.Reader) metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	if len(rm.ScopeMetrics) != 1 || len(rm.ScopeMetrics[0].Metrics) != 1 {
		t.Fatalf("want exactly one metric, got %+v", rm)
	}
	return rm.ScopeMetrics[0].Metrics[0]
}

func TestRecordGaugeTo_floatValueNameAndLabels(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = mp.Shutdown(context.Background()) }()
	err := RecordGaugeTo(context.Background(), mp.Meter("test"), "ccpool_janitor_reclaimed", 4.5, map[string]string{"pass": "worktrees"})
	if err != nil {
		t.Fatal(err)
	}
	m := collectOne(t, reader)
	g, ok := m.Data.(metricdata.Gauge[float64])
	if m.Name != "ccpool_janitor_reclaimed" || !ok || len(g.DataPoints) != 1 {
		t.Fatalf("metric = %+v", m)
	}
	dp := g.DataPoints[0]
	if v, _ := dp.Attributes.Value("pass"); dp.Value != 4.5 || v.AsString() != "worktrees" || dp.Attributes.Len() != 1 {
		t.Fatalf("datapoint = %+v", dp)
	}
}

func TestRecordGaugeTo_rejectsInvalidIdentifiers(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = mp.Shutdown(context.Background()) }()
	m := mp.Meter("test")
	if err := RecordGaugeTo(context.Background(), m, "bad-name", 1, nil); err == nil {
		t.Error("bad metric name accepted")
	}
	if err := RecordGaugeTo(context.Background(), m, "ok", 1, map[string]string{"bad-label": "v"}); err == nil {
		t.Error("bad label name accepted")
	}
}

// TestDeltaCounterFromShortLivedProcess documents the research finding of
// bead pg2-fx5x0l.8: with the DeltaTemporalitySelector newOTLPMetricExporter
// uses, an Int64Counter incremented once per process exports that increment
// as a delta, so increase()/rate() over many one-shot processes works.
func TestDeltaCounterFromShortLivedProcess(t *testing.T) {
	reader := sdkmetric.NewManualReader(sdkmetric.WithTemporalitySelector(sdkmetric.DeltaTemporalitySelector))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = mp.Shutdown(context.Background()) }()
	c, err := mp.Meter("test").Int64Counter("ccpool_orphan_events_total")
	if err != nil {
		t.Fatal(err)
	}
	c.Add(context.Background(), 1)
	sum, ok := collectOne(t, reader).Data.(metricdata.Sum[int64])
	if !ok || sum.Temporality != metricdata.DeltaTemporality || sum.DataPoints[0].Value != 1 {
		t.Fatalf("sum = %+v", sum)
	}
}

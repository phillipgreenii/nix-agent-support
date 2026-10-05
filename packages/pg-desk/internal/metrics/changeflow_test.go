package metrics

import (
	"context"
	"errors"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
)

func collectChangeFlow(t *testing.T, fn ChangeFlowFunc) (metricdata.ResourceMetrics, error) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if err := RegisterChangeFlow(mp, fn); err != nil {
		t.Fatalf("RegisterChangeFlow: %v", err)
	}
	var rm metricdata.ResourceMetrics
	err := reader.Collect(context.Background(), &rm)
	return rm, err
}

func TestChangeFlowMetricsCarryTheCatalogWithLabels(t *testing.T) {
	rm, err := collectChangeFlow(t, func() (changes.Flow, error) {
		return changes.Flow{
			Migrated: true,
			Types: []changes.TypeFlow{{
				Type:     "pr",
				Records:  []changes.RecordCount{{Kind: "created", Origin: "poll", Count: 4}},
				Due:      3,
				Stats:    changes.HydrationStats{Hydrations: 9, Failures: 2, OCCRetries: 5},
				Repeated: map[string]changes.DegradedEntity{"o/r#1": {Count: 2}},
			}},
			Consumers: []changes.ConsumerFlow{{Name: "alpha", Type: "pr", Lag: 7}},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{
		MetricChangeRecords:     4,
		MetricHydrations:        9,
		MetricHydrationFailures: 2,
		MetricOCCRetries:        5,
		MetricDueBacklog:        3,
		MetricConsumerLag:       7,
		MetricRepeatedDegraded:  1,
	}
	for name, v := range want {
		m := findMetric(t, rm, name)
		var got int64
		switch d := m.Data.(type) {
		case metricdata.Gauge[int64]:
			if len(d.DataPoints) != 1 {
				t.Fatalf("%s: %d points", name, len(d.DataPoints))
			}
			got = d.DataPoints[0].Value
			if typ, _ := d.DataPoints[0].Attributes.Value("type"); typ.AsString() != "pr" {
				t.Errorf("%s: type label = %q", name, typ.AsString())
			}
			if name == MetricChangeRecords {
				k, _ := d.DataPoints[0].Attributes.Value("kind")
				o, _ := d.DataPoints[0].Attributes.Value("origin")
				if k.AsString() != "created" || o.AsString() != "poll" {
					t.Errorf("%s: kind/origin = %q/%q", name, k.AsString(), o.AsString())
				}
			}
			if name == MetricConsumerLag {
				c, _ := d.DataPoints[0].Attributes.Value("consumer")
				if c.AsString() != "alpha" {
					t.Errorf("%s: consumer label = %q", name, c.AsString())
				}
			}
		case metricdata.Sum[int64]:
			if len(d.DataPoints) != 1 {
				t.Fatalf("%s: %d points", name, len(d.DataPoints))
			}
			got = d.DataPoints[0].Value
		default:
			t.Fatalf("%s: unexpected data %T", name, m.Data)
		}
		if got != v {
			t.Errorf("%s = %d, want %d", name, got, v)
		}
	}
}

func TestChangeFlowMetricsOmitSeriesWhenUnmigrated(t *testing.T) {
	rm, err := collectChangeFlow(t, func() (changes.Flow, error) { return changes.Flow{}, nil })
	if err != nil {
		t.Fatalf("an unmigrated store must not fail the scrape: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Gauge[int64]:
				if len(d.DataPoints) != 0 {
					t.Errorf("%s emitted %d points on an unmigrated store", m.Name, len(d.DataPoints))
				}
			case metricdata.Sum[int64]:
				if len(d.DataPoints) != 0 {
					t.Errorf("%s emitted %d points on an unmigrated store", m.Name, len(d.DataPoints))
				}
			}
		}
	}
}

func TestChangeFlowMetricsSurfaceAReadError(t *testing.T) {
	_, err := collectChangeFlow(t, func() (changes.Flow, error) { return changes.Flow{}, errors.New("store gone") })
	if err == nil {
		t.Fatal("a failing change-flow read must surface from Collect")
	}
}

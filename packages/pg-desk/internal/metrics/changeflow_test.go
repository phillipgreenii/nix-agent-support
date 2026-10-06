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

// boundPoints collects pg_desk_sweep_bound_violated as "type/tier" -> value.
func boundPoints(t *testing.T, rm metricdata.ResourceMetrics) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != MetricSweepBoundViolated {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("%s: unexpected data %T", m.Name, m.Data)
			}
			for _, dp := range g.DataPoints {
				typ, _ := dp.Attributes.Value("type")
				tier, _ := dp.Attributes.Value("tier")
				out[typ.AsString()+"/"+tier.AsString()] = dp.Value
			}
		}
	}
	return out
}

func sweepFlow() changes.Flow {
	return changes.Flow{Migrated: true, Types: []changes.TypeFlow{{Type: "pr", Active: 88}, {Type: "issue", Active: 3}}}
}

func TestSweepBoundMetricReadsOneWhenViolatedAndZeroWhenHolding(t *testing.T) {
	// pr: the local tier is violated, the remote tier holds. issue: both hold.
	eval := func(tf changes.TypeFlow, tier string) changes.BoundVerdict {
		if tf.Type == "pr" && tier == changes.TierLocal {
			return changes.BoundViolated
		}
		return changes.BoundHolds
	}
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if err := RegisterChangeFlow(mp, func() (changes.Flow, error) { return sweepFlow(), nil }, WithSweepBound(eval)); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"pr/local": 1, "pr/remote": 0, "issue/local": 0, "issue/remote": 0}
	got := boundPoints(t, rm)
	if len(got) != len(want) {
		t.Fatalf("points = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
}

func TestSweepBoundMetricHasNoSeriesWithoutAVerdict(t *testing.T) {
	unknown := func(changes.TypeFlow, string) changes.BoundVerdict { return changes.BoundUnknown }
	for name, opts := range map[string][]ChangeFlowOption{
		"unknown poll interval": {WithSweepBound(unknown)},
		"no evaluator supplied": nil,
	} {
		reader := sdkmetric.NewManualReader()
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		if err := RegisterChangeFlow(mp, func() (changes.Flow, error) { return sweepFlow(), nil }, opts...); err != nil {
			t.Fatal(err)
		}
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatal(err)
		}
		if got := boundPoints(t, rm); len(got) != 0 {
			t.Errorf("%s: emitted %v, want no series (no verdict is never a false violation)", name, got)
		}
	}
}

func TestSweepBoundMetricIgnoresNonFlowTypes(t *testing.T) {
	flow := changes.Flow{Migrated: true, Types: []changes.TypeFlow{{Type: "consumer-only-type", Active: 1}}}
	holds := func(changes.TypeFlow, string) changes.BoundVerdict { return changes.BoundHolds }
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if err := RegisterChangeFlow(mp, func() (changes.Flow, error) { return flow, nil }, WithSweepBound(holds)); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	if got := boundPoints(t, rm); len(got) != 0 {
		t.Errorf("emitted %v for a non-flow type", got)
	}
}

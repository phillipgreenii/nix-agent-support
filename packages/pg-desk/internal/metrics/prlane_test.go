package metrics

import (
	"context"
	"errors"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func collectPRLane(t *testing.T, mode func() string, fn PRUnhydratedFunc) (metricdata.ResourceMetrics, error) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if err := RegisterPRLane(mp, mode, fn); err != nil {
		t.Fatalf("RegisterPRLane: %v", err)
	}
	var rm metricdata.ResourceMetrics
	err := reader.Collect(context.Background(), &rm)
	return rm, err
}

// points returns the data points of the named gauge keyed by their "mode"
// label ("" for an unlabelled series); a gauge with no series returns nil.
func points(rm metricdata.ResourceMetrics, name string) map[string]int64 {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			out := map[string]int64{}
			for _, dp := range m.Data.(metricdata.Gauge[int64]).DataPoints {
				v, _ := dp.Attributes.Value("mode")
				out[v.AsString()] = dp.Value
			}
			return out
		}
	}
	return nil
}

func TestPRLaneModeGaugeEmitsEveryModeWithTheConfiguredOneSet(t *testing.T) {
	for _, configured := range []string{"off", "plan", "apply"} {
		rm, err := collectPRLane(t, func() string { return configured },
			func() (PRUnhydratedSnapshot, bool, error) { return PRUnhydratedSnapshot{}, true, nil })
		if err != nil {
			t.Fatal(err)
		}
		got := points(rm, MetricPRLaneMode)
		for _, mode := range []string{"off", "plan", "apply"} {
			want := int64(0)
			if mode == configured {
				want = 1
			}
			if v, ok := got[mode]; !ok || v != want {
				t.Errorf("configured %q: series mode=%q = %d (present %v), want %d", configured, mode, v, ok, want)
			}
		}
	}
}

func TestPRUnhydratedGaugesAreEmittedWhenHealthyEvenAtZero(t *testing.T) {
	rm, err := collectPRLane(t, func() string { return "off" },
		func() (PRUnhydratedSnapshot, bool, error) {
			return PRUnhydratedSnapshot{Count: 0, OldestAgeSeconds: 0}, true, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{MetricPRUnhydrated, MetricPRUnhydratedOldestAge} {
		got := points(rm, name)
		if v, ok := got[""]; !ok || v != 0 {
			t.Errorf("%s = %d (present %v), want a series with value 0 for an empty set", name, v, ok)
		}
	}
	rm, err = collectPRLane(t, func() string { return "off" },
		func() (PRUnhydratedSnapshot, bool, error) {
			return PRUnhydratedSnapshot{Count: 495, OldestAgeSeconds: 86400}, true, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if got := points(rm, MetricPRUnhydrated)[""]; got != 495 {
		t.Errorf("%s = %d, want 495", MetricPRUnhydrated, got)
	}
	if got := points(rm, MetricPRUnhydratedOldestAge)[""]; got != 86400 {
		t.Errorf("%s = %d, want 86400", MetricPRUnhydratedOldestAge, got)
	}
}

// TestPRUnhydratedFailedReadNeverYieldsZero: a failed store read, and a store
// that cannot answer, export NO series for the two gauges (never 0), while the
// mode gauge is still emitted.
func TestPRUnhydratedFailedReadNeverYieldsZero(t *testing.T) {
	for name, fn := range map[string]PRUnhydratedFunc{
		"error": func() (PRUnhydratedSnapshot, bool, error) {
			return PRUnhydratedSnapshot{}, true, errors.New("disk i/o error")
		},
		"unavailable": func() (PRUnhydratedSnapshot, bool, error) { return PRUnhydratedSnapshot{}, false, nil },
	} {
		t.Run(name, func(t *testing.T) {
			rm, _ := collectPRLane(t, func() string { return "plan" }, fn)
			for _, metric := range []string{MetricPRUnhydrated, MetricPRUnhydratedOldestAge} {
				if got := points(rm, metric); len(got) != 0 {
					t.Errorf("%s has series %v after a failed read; it must have none", metric, got)
				}
			}
			if got := points(rm, MetricPRLaneMode); got["plan"] != 1 {
				t.Errorf("the mode gauge must survive a failed store read, got %v", got)
			}
		})
	}
}

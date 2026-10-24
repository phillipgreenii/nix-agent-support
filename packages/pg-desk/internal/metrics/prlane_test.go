package metrics

import (
	"context"
	"errors"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/lanestate"
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

func TestPRLaneStateFamiliesEmitNothingWhenTheStoreCannotAnswer(t *testing.T) {
	for name, fn := range map[string]PRLaneStateFunc{
		"not schema 2": func() (lanestate.Snapshot, bool, error) { return lanestate.Snapshot{}, false, nil },
		"read failed":  func() (lanestate.Snapshot, bool, error) { return lanestate.Snapshot{}, true, errors.New("boom") },
	} {
		t.Run(name, func(t *testing.T) {
			reader := sdkmetric.NewManualReader()
			mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
			if err := RegisterPRLaneState(mp, fn); err != nil {
				t.Fatal(err)
			}
			var rm metricdata.ResourceMetrics
			_ = reader.Collect(context.Background(), &rm)
			for _, sm := range rm.ScopeMetrics {
				if len(sm.Metrics) != 0 {
					t.Errorf("%d metric families exported when the store cannot answer, want none", len(sm.Metrics))
				}
			}
		})
	}
}

func TestPRLaneResidualGaugesCarryTheLaneLevelModeLabel(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	snap := lanestate.Snapshot{LaneMode: "plan", ResidualPRs: 4, ResidualOldestAgeSeconds: 90}
	if err := RegisterPRLaneState(mp, func() (lanestate.Snapshot, bool, error) { return snap, true, nil }); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	got := points(rm, MetricPRLaneResidualPRs)
	if got["plan"] != 4 || got["apply"] != 0 || got["off"] != 0 || len(got) != 3 {
		t.Errorf("residual by mode = %v, want the 4 on plan and 0 on the other two", got)
	}
	if age := points(rm, MetricPRLaneResidualOldestAge); age["plan"] != 90 || age["apply"] != 0 {
		t.Errorf("oldest age by mode = %v", age)
	}
}

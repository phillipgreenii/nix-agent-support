package metrics

import (
	"context"
	"errors"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func collectAnchors(t *testing.T, fn PRAnchorsFunc) metricdata.ResourceMetrics {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if err := RegisterPRAnchors(mp, fn); err != nil {
		t.Fatalf("RegisterPRAnchors: %v", err)
	}
	var rm metricdata.ResourceMetrics
	_ = reader.Collect(context.Background(), &rm)
	return rm
}

var anchorMetricNames = []string{
	MetricPROpenAnchors, MetricPROpenAnchorsHidden, MetricPRStaleAnchors,
	MetricPRStaleAnchorOldestAge, MetricPROpenAnchorHydrationAge,
}

func TestPRAnchorGaugesAreEmittedWhenHealthyEvenAtZeroAndCarryTheValues(t *testing.T) {
	rm := collectAnchors(t, func() (PRAnchorsSnapshot, bool, error) { return PRAnchorsSnapshot{}, true, nil })
	for _, name := range anchorMetricNames {
		if v, ok := points(rm, name)[""]; !ok || v != 0 {
			t.Errorf("%s = %d (present %v), want a series with value 0 for an empty view", name, v, ok)
		}
	}
	rm = collectAnchors(t, func() (PRAnchorsSnapshot, bool, error) {
		return PRAnchorsSnapshot{Open: 7, OpenHidden: 2, Stale: 3, StaleOldestAgeSeconds: 900, OldestHydrationAgeSeconds: 86400}, true, nil
	})
	for name, want := range map[string]int64{
		MetricPROpenAnchors: 7, MetricPROpenAnchorsHidden: 2, MetricPRStaleAnchors: 3,
		MetricPRStaleAnchorOldestAge: 900, MetricPROpenAnchorHydrationAge: 86400,
	} {
		if got := points(rm, name)[""]; got != want {
			t.Errorf("%s = %d, want %d", name, got, want)
		}
	}
}

// TestPRAnchorGaugesFailedReadNeverYieldsZero: a failed store read and a store
// that cannot answer export NO series for any anchor gauge (never 0).
func TestPRAnchorGaugesFailedReadNeverYieldsZero(t *testing.T) {
	for name, fn := range map[string]PRAnchorsFunc{
		"error": func() (PRAnchorsSnapshot, bool, error) {
			return PRAnchorsSnapshot{}, true, errors.New("disk i/o error")
		},
		"unavailable": func() (PRAnchorsSnapshot, bool, error) { return PRAnchorsSnapshot{}, false, nil },
	} {
		t.Run(name, func(t *testing.T) {
			rm := collectAnchors(t, fn)
			for _, metric := range anchorMetricNames {
				if got := points(rm, metric); len(got) != 0 {
					t.Errorf("%s has series %v after a failed read; it must have none", metric, got)
				}
			}
		})
	}
}

package metrics

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

// The PR lane catalog (PR lane refactor, P0b). Every gauge here is emitted
// with a value when it is healthy, so "no series" never means "healthy".
const (
	// MetricPRLaneMode reports the configured lane mode (config.yaml's
	// pr_lane.mode) as one series per mode, label mode in off|plan|apply, with
	// value 1 on the configured mode and 0 on the others. All three series are
	// always emitted, so the mode is readable from one scrape and a rule can
	// select the one it needs.
	MetricPRLaneMode = "pg_desk_pr_lane_mode"
	// MetricPRUnhydrated is the number of PRs the store lists as open that no
	// hydration has ever written (open, not hidden, not marked inactive,
	// version 0): the never-hydrated set. Computed from the store at scrape
	// time. A scrape whose store read fails exports NO series for it, never 0.
	MetricPRUnhydrated = "pg_desk_pr_unhydrated"
	// MetricPRUnhydratedOldestAge is the age in seconds of the oldest member of
	// that set (0 when it is empty), emitted whenever MetricPRUnhydrated is.
	MetricPRUnhydratedOldestAge = "pg_desk_pr_unhydrated_oldest_age_seconds"
)

// PRUnhydratedSnapshot is the never-hydrated set at one scrape.
type PRUnhydratedSnapshot struct {
	Count, OldestAgeSeconds int
}

// PRUnhydratedFunc supplies the never-hydrated set at collect time. It MUST
// return ok=false (and a nil error) for a store that cannot answer, such as
// one not migrated to schema 2: the catalog then exports no series. A non-nil
// error also exports no series; neither case is ever reported as 0.
type PRUnhydratedFunc func() (snap PRUnhydratedSnapshot, ok bool, err error)

// RegisterPRLane registers the PR lane instruments on a meter from mp. mode
// supplies the configured lane mode (read per scrape, so it is never stale
// relative to the config the process holds). The two never-hydrated gauges
// are observed by their OWN callback: a failed store read there drops only
// those two series and leaves the mode gauge, and every other family, intact.
func RegisterPRLane(mp metric.MeterProvider, mode func() string, unhydrated PRUnhydratedFunc) error {
	m := mp.Meter("github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk")

	laneMode, err := m.Int64ObservableGauge(MetricPRLaneMode,
		metric.WithDescription("configured PR lane mode (config pr_lane.mode): 1 on the series whose mode label is the configured mode, 0 on the other two; a missing or invalid value reads as off"))
	if err != nil {
		return err
	}
	count, err := m.Int64ObservableGauge(MetricPRUnhydrated,
		metric.WithDescription("PRs listed as open that no hydration has written yet (open, not hidden, not inactive, version 0); no series when the store read fails, never 0"))
	if err != nil {
		return err
	}
	oldest, err := m.Int64ObservableGauge(MetricPRUnhydratedOldestAge,
		metric.WithDescription("age in seconds of the oldest never-hydrated PR, 0 when there is none; no series when the store read fails, never 0"))
	if err != nil {
		return err
	}

	if _, err := m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		configured := mode()
		for _, mm := range []string{config.PRLaneModeOff, config.PRLaneModePlan, config.PRLaneModeApply} {
			var v int64
			if mm == configured {
				v = 1
			}
			o.ObserveInt64(laneMode, v, metric.WithAttributes(attribute.String("mode", mm)))
		}
		return nil
	}, laneMode); err != nil {
		return err
	}

	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		snap, ok, err := unhydrated()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		o.ObserveInt64(count, int64(snap.Count))
		o.ObserveInt64(oldest, int64(snap.OldestAgeSeconds))
		return nil
	}, count, oldest)
	return err
}

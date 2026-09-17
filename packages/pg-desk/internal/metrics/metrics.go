// Package metrics emits pg-desk serve's real Prometheus metrics catalog
// (design doc section 8), replacing the earlier hand-written `pg_desk_up 1`
// stub that internal/httpapi's /metrics route used to serve: liveness, and
// three gauges promoted from the /api/v1/dashboard payload's own
// freshness/counter fields (age_seconds, stale, dropped_count), plus a
// sync-errors-by-repo counter. Built the same way pg-router's own catalog
// is: the OTel metrics API (go.opentelemetry.io/otel/metric) plus
// go.opentelemetry.io/otel/exporters/prometheus, so /metrics stays a
// directly-scraped Prometheus exposition endpoint — no OTLP push involved
// for metrics.
//
// pg_desk_dashboard_stale's value mapping is pinned explicitly here, not
// left to be inferred by analogy: 0 = fresh, 1 = stale. This is the
// OPPOSITE polarity from a presence-style gauge (where 1 is the GOOD
// state — e.g. "a recent snapshot exists") — a naive copy of that
// convention would invert the signal and read green while the dashboard
// data is actually stale, which is the one failure mode a freshness gauge
// exists to catch.
package metrics

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metric names (design section 8's catalog; five members).
const (
	// MetricLiveness reports 1 whenever pg-desk serve answers a scrape —
	// the OTel-native successor to the old hand-written `pg_desk_up 1`
	// stub. The route itself stays 503-gated until the store holds its
	// first interpretation (httpapi's readyGate wraps /metrics exactly as
	// it wrapped the old stub), so a scrape that reaches this callback at
	// all already proves the process is up and ready; there is no
	// separate internal "tick" concept for serve to check the way
	// pg-router's own dispatch loop has.
	MetricLiveness = "pg_desk_liveness"
	// MetricDashboardAge promotes the /api/v1/dashboard payload's
	// age_seconds field so it is alertable, not just displayed.
	MetricDashboardAge = "pg_desk_dashboard_age_seconds"
	// MetricDashboardStale promotes the payload's stale field. See the
	// package doc for its pinned polarity: 0 = fresh, 1 = stale.
	MetricDashboardStale = "pg_desk_dashboard_stale"
	// MetricDropped promotes the payload's dropped_count — a point-in-time
	// count, hence a gauge, not a counter (matches the My Work dashboard's
	// collapsed "Dropped PR Count" panel's lastNotNull semantics).
	MetricDropped = "pg_desk_dropped"
	// MetricSyncErrors counts sync failures for a PR, by repo (Phase 10),
	// mirroring pg-pr-ops's "Sync errors" panel. Registered and exposed
	// here (see RecordSyncError) but — like pg-router's own throughput and
	// dispatch_latency catalog members — with no live production call
	// site wired from this package: serve never itself runs sync (that
	// happens in a separate `pg-desk run` process against the shared
	// store), so there is nowhere in serve's own request path to observe
	// a sync failure the moment it happens. A future task that threads an
	// Emitter (or an equivalent cross-process path) into the sync stage
	// is left to wire the live call site, exactly as pg-router's own
	// RecordThroughput/RecordDispatchLatency are built and exposed
	// without one yet.
	MetricSyncErrors = "pg_desk_sync_errors"
)

// Snapshot is the subset of the /api/v1/dashboard payload the metrics
// catalog promotes to Prometheus, read fresh on every scrape via
// SnapshotFunc — the SAME store-backed freshness computation
// httpapi.BuildPayload already performs for the JSON payload, so the two
// surfaces can never disagree about what "now" means (design section 8).
type Snapshot struct {
	AgeSeconds   int
	Stale        bool
	DroppedCount int
}

// SnapshotFunc supplies the current dashboard snapshot at collect time.
type SnapshotFunc func() (Snapshot, error)

// Emitter emits the catalog over an OTel meter.
type Emitter struct {
	syncErrors metric.Int64Counter
}

// New registers the instruments above on a meter from mp and returns an
// Emitter. snapshotFn supplies the three dashboard-derived gauges, read
// fresh on every collect (never cached), matching httpapi.BuildPayload's
// own read-at-request-time contract.
func New(mp metric.MeterProvider, snapshotFn SnapshotFunc) (*Emitter, error) {
	m := mp.Meter("github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk")

	if _, err := m.Int64ObservableGauge(
		MetricLiveness,
		metric.WithDescription("1 while pg-desk serve answers this scrape, else 0 (mirrors pg_router_liveness)"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(1)
			return nil
		}),
	); err != nil {
		return nil, err
	}

	if _, err := m.Int64ObservableGauge(
		MetricDashboardAge,
		metric.WithDescription("age, in seconds, of the /api/v1/dashboard payload's generated_at (promotes that payload's age_seconds field)"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			snap, err := snapshotFn()
			if err != nil {
				return err
			}
			o.Observe(int64(snap.AgeSeconds))
			return nil
		}),
	); err != nil {
		return nil, err
	}

	if _, err := m.Int64ObservableGauge(
		MetricDashboardStale,
		metric.WithDescription("0 = fresh, 1 = stale (promotes the /api/v1/dashboard payload's stale field; explicit polarity, opposite of a presence-style gauge — see package doc)"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			snap, err := snapshotFn()
			if err != nil {
				return err
			}
			v := int64(0)
			if snap.Stale {
				v = 1
			}
			o.Observe(v)
			return nil
		}),
	); err != nil {
		return nil, err
	}

	if _, err := m.Int64ObservableGauge(
		MetricDropped,
		metric.WithDescription("point-in-time dropped_count from the /api/v1/dashboard payload (matches the My Work dashboard's collapsed Dropped PR Count panel's lastNotNull semantics)"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			snap, err := snapshotFn()
			if err != nil {
				return err
			}
			o.Observe(int64(snap.DroppedCount))
			return nil
		}),
	); err != nil {
		return nil, err
	}

	syncErrors, err := m.Int64Counter(
		MetricSyncErrors,
		metric.WithDescription("sync failures for a PR, by repo (Phase 10); registered and exposed here with no live call site wired from serve — see MetricSyncErrors' doc"),
	)
	if err != nil {
		return nil, err
	}

	return &Emitter{syncErrors: syncErrors}, nil
}

// RecordSyncError increments the sync-errors-by-repo counter. Exported for
// direct/test use; see MetricSyncErrors' doc for why no production call
// site is wired from this package.
func (e *Emitter) RecordSyncError(repo string) {
	e.syncErrors.Add(context.Background(), 1, metric.WithAttributes(attribute.String("repo", repo)))
}

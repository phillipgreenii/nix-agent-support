// Package metrics emits pg-desk serve's real Prometheus metrics catalog
// (design doc section 8), replacing the earlier hand-written `pg_desk_up 1`
// stub that internal/httpapi's /metrics route used to serve: liveness, and
// three gauges promoted from the /api/v1/dashboard payload's own
// freshness/counter fields (age_seconds, stale, dropped_count), plus the
// per-source fetch age. Built the same way pg-router's own catalog
// is: the OTel metrics API (go.opentelemetry.io/otel/metric) plus
// go.opentelemetry.io/otel/exporters/prometheus, so /metrics stays a
// directly-scraped Prometheus exposition endpoint — no OTLP push involved
// for metrics.
//
// pg_desk_dashboard_age_seconds and pg_desk_dashboard_stale report PIPELINE
// LIVENESS: the age of meta.last_heartbeat, i.e. whether the heartbeat that
// refreshes this snapshot is still running. They say nothing about how old
// the data behind the snapshot is; that is pg_desk_source_age_seconds{source},
// the age of each source's last successful origin fetch (INV-FRESH-5).
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
	// age_seconds field so it is alertable, not just displayed. It is
	// pipeline liveness (the age of meta.last_heartbeat), not data age.
	MetricDashboardAge = "pg_desk_dashboard_age_seconds"
	// MetricDashboardStale promotes the payload's stale field: pipeline
	// liveness (meta.last_heartbeat older than two heartbeat periods), not data
	// age. See the package doc for its pinned polarity: 0 = fresh, 1 = stale.
	MetricDashboardStale = "pg_desk_dashboard_stale"
	// MetricDropped promotes the payload's dropped_count — a point-in-time
	// count, hence a gauge, not a counter (matches the My Work dashboard's
	// collapsed "Dropped PR Count" panel's lastNotNull semantics).
	MetricDropped = "pg_desk_dropped"
	// MetricSourceAge is the age in seconds of each source's last SUCCESSFUL
	// origin fetch, per source (the connector backend). A source with no
	// recorded success exports NO series: it is never exported as 0, which
	// would read as freshly fetched.
	MetricSourceAge = "pg_desk_source_age_seconds"
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
	// SourceAges backs MetricSourceAge: one entry per source with a known
	// age; sources with an unknown age are omitted.
	SourceAges []SourceAge
}

// SourceAge is one source's age for MetricSourceAge.
type SourceAge struct {
	Source  string
	Seconds int
}

// SnapshotFunc supplies the current dashboard snapshot at collect time.
type SnapshotFunc func() (Snapshot, error)

// New registers the instruments above on a meter from mp.
// snapshotFn supplies the three dashboard-derived gauges, read
// fresh on every collect (never cached), matching httpapi.BuildPayload's
// own read-at-request-time contract.
func New(mp metric.MeterProvider, snapshotFn SnapshotFunc) error {
	m := mp.Meter("github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk")

	if _, err := m.Int64ObservableGauge(
		MetricLiveness,
		metric.WithDescription("1 while pg-desk serve answers this scrape, else 0 (mirrors pg_router_liveness)"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(1)
			return nil
		}),
	); err != nil {
		return err
	}

	// Every snapshot-derived gauge is observed from ONE callback, so snapshotFn
	// (a full BuildPayload plus ledger and interpretation reads) runs once per
	// collection. Registering a callback per gauge ran it nine times per scrape
	// on a single-connection store, which outlasted Prometheus's scrape timeout
	// and piled abandoned scrapes up behind each other (bead pg2-jj0ym: a
	// /metrics scrape did not answer within 40s).
	type snapGauge struct {
		name, desc string
		val        func(Snapshot) int64
		inst       metric.Int64ObservableGauge
	}
	gauges := []*snapGauge{
		{
			name: MetricDashboardAge, desc: "pipeline liveness: age, in seconds, of the /api/v1/dashboard payload's generated_at, i.e. of meta.last_heartbeat (promotes that payload's age_seconds field); NOT the age of the data, see pg_desk_source_age_seconds",
			val: func(s Snapshot) int64 { return int64(s.AgeSeconds) },
		},
		{
			name: MetricDashboardStale, desc: "pipeline liveness: 0 = fresh, 1 = stale, where stale means meta.last_heartbeat is older than two heartbeat periods (promotes the /api/v1/dashboard payload's stale field; explicit polarity, opposite of a presence-style gauge, see package doc); NOT the age of the data, see pg_desk_source_age_seconds",
			val: func(s Snapshot) int64 {
				if s.Stale {
					return 1
				}
				return 0
			},
		},
		{
			name: MetricDropped, desc: "point-in-time dropped_count from the /api/v1/dashboard payload (matches the My Work dashboard's collapsed Dropped PR Count panel's lastNotNull semantics)",
			val: func(s Snapshot) int64 { return int64(s.DroppedCount) },
		},
	}
	instruments := make([]metric.Observable, 0, len(gauges)+1)
	for _, g := range gauges {
		inst, err := m.Int64ObservableGauge(g.name, metric.WithDescription(g.desc))
		if err != nil {
			return err
		}
		g.inst = inst
		instruments = append(instruments, inst)
	}
	sourceAge, err := m.Int64ObservableGauge(
		MetricSourceAge,
		metric.WithDescription("age, in seconds, of each source's last successful origin fetch (the connector ledger's refreshed_at), per source; a source with no recorded success exports no series"),
	)
	if err != nil {
		return err
	}
	instruments = append(instruments, sourceAge)
	if _, err := m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		snap, err := snapshotFn()
		if err != nil {
			return err
		}
		for _, g := range gauges {
			o.ObserveInt64(g.inst, g.val(snap))
		}
		for _, s := range snap.SourceAges {
			o.ObserveInt64(sourceAge, int64(s.Seconds), metric.WithAttributes(attribute.String("source", s.Source)))
		}
		return nil
	}, instruments...); err != nil {
		return err
	}

	return nil
}

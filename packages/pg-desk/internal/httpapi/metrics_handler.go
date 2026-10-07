package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	otelprometheus "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/freshness"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/metrics"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/sync"
)

// newMetricsHandler replaces the earlier `pg_desk_up 1` stub (design D5)
// with a real catalog (design section 8): pg_desk_liveness,
// pg_desk_dashboard_age_seconds, pg_desk_dashboard_stale, pg_desk_dropped,
// and pg_desk_sync_errors_total, served as directly-scraped Prometheus
// exposition text — the same OTel-metrics-API + prometheus.Exporter
// approach pg-router's own catalog uses (no OTLP push involved for
// metrics), on serve's EXISTING /metrics route (no new port).
//
// Each call builds its own private prometheus.Registry — never the
// process-global prometheus.DefaultRegisterer: NewHandler is constructed
// fresh per test (and once per real serve invocation), and a shared
// global registry would make repeated construction within one test binary
// collide with "duplicate metrics collector registration attempted".
func newMetricsHandler(st *store.Store, cfg *config.Config, pollInterval PollIntervalFunc) (http.Handler, error) {
	registry := prometheus.NewRegistry()
	// WithoutScopeInfo: without it every catalog member would carry extra
	// otel_scope_name/otel_scope_version labels and the exporter would add
	// a standalone otel_scope_info series — noise the design's own PromQL
	// examples (bare names like pg_desk_liveness, no scope-label filter)
	// don't expect.
	exporter, err := otelprometheus.New(
		otelprometheus.WithRegisterer(registry),
		otelprometheus.WithoutScopeInfo(),
	)
	if err != nil {
		return nil, fmt.Errorf("httpapi: new prometheus exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))

	// snapshotFn re-derives the same three freshness/counter fields
	// BuildPayload computes for the JSON payload, read fresh from the
	// store on every scrape (never cached) — so the Prometheus gauges and
	// the /api/v1/dashboard payload can never disagree about what "now"
	// means (design section 8).
	snapshotFn := func() (metrics.Snapshot, error) {
		payload, err := buildPayload(st, cfg, nowUTC(), false)
		if err != nil {
			return metrics.Snapshot{}, err
		}
		interps, err := st.ListInterpretations()
		if err != nil {
			return metrics.Snapshot{}, err
		}
		syncRows, oldestAge := syncErrorStats(interps, nowUTC())
		retrying, exhausted, err := syncRetryStats(st, interps)
		if err != nil {
			return metrics.Snapshot{}, err
		}
		anchorCheckAge, err := sync.OldestAnchorCheckAge(st, nowUTC())
		if err != nil {
			return metrics.Snapshot{}, err
		}
		return metrics.Snapshot{
			AgeSeconds:                payload.AgeSeconds,
			Stale:                     payload.Stale,
			DroppedCount:              payload.DroppedCount,
			SyncErrorRows:             syncRows,
			OldestSyncErrorAgeSeconds: oldestAge,
			SyncErrorRetryingRows:     retrying,
			SyncErrorExhaustedRows:    exhausted,

			OldestAnchorCheckAgeSeconds: anchorCheckAge,
			SourceAges:                  sourceAges(payload.Sources),
		}, nil
	}

	if _, err := metrics.New(mp, snapshotFn); err != nil {
		return nil, fmt.Errorf("httpapi: new metrics emitter: %w", err)
	}

	// The change-flow families (design: observability) are computed fresh on
	// every scrape from the store and the persisted hydration totals. On a
	// store that is not on the new schema Observe returns a zero Flow, so
	// serve keeps working there and simply omits these series.
	flowFn := func() (changes.Flow, error) {
		return changes.Observe(st, nowUTC(), cfg.SweepMaxAge())
	}
	// The sizing bound is evaluated per (type, tier) with the SAME function
	// `doctor` uses (changes.EvaluateSweepBound). The poll interval is not
	// owned by pg-desk: without a supplier, or when it does not know the
	// type, there is no verdict and the metric emits no series.
	sweepBound := func(tf changes.TypeFlow, tier string) changes.BoundVerdict {
		var poll time.Duration
		known := false
		if pollInterval != nil {
			poll, known = pollInterval(tf.Type)
		}
		return changes.EvaluateSweepBound(changes.SweepInputsForTier(cfg, tf, tier), poll, known)
	}
	if err := metrics.RegisterChangeFlow(mp, flowFn, metrics.WithSweepBound(sweepBound)); err != nil {
		return nil, fmt.Errorf("httpapi: register change-flow metrics: %w", err)
	}

	return scrapeHandler(registry, scrapeTimeout), nil
}

// sourceAges projects the payload's sources[] to the gauge's input: a
// source with no recorded success (null age) is omitted, so it exports no
// series rather than a misleading 0.
func sourceAges(rows []freshness.Row) []metrics.SourceAge {
	var out []metrics.SourceAge
	for _, r := range rows {
		if r.AgeSeconds != nil {
			out = append(out, metrics.SourceAge{Source: r.Source, Seconds: *r.AgeSeconds})
		}
	}
	return out
}

// syncErrorStats counts interpretation rows with a non-empty sync_error and
// returns the age in seconds of the oldest (by as_of); 0 when none or when
// as_of is unparseable (pg2-kftf9.5).
func syncErrorStats(interps []store.Interpretation, now time.Time) (rows, oldestAgeSeconds int) {
	for _, i := range interps {
		if i.SyncError == "" {
			continue
		}
		rows++
		if t := parseAsOf(i.AsOf); !t.IsZero() {
			if age := int(now.Sub(t).Seconds()); age > oldestAgeSeconds {
				oldestAgeSeconds = age
			}
		}
	}
	return rows, oldestAgeSeconds
}

// syncRetryStats splits the sync_error rows by automatic-retry state (bead
// pg2-xb6fs): retrying (including a row with no recorded state yet, which is
// due for its first retry) versus exhausted (retry bound reached, or
// non-transient — no automatic retry left).
func syncRetryStats(st *store.Store, interps []store.Interpretation) (retrying, exhausted int, err error) {
	for _, i := range interps {
		if i.SyncError == "" {
			continue
		}
		r, _, err := st.GetSyncRetry(i.EntityID)
		if err != nil {
			return 0, 0, err
		}
		if r.EffectiveState() == store.SyncRetryRetrying {
			retrying++
		} else {
			exhausted++
		}
	}
	return retrying, exhausted, nil
}

// scrapeTimeout is how long one /metrics scrape may take before it is
// answered 503. Prometheus's default scrape timeout is 10s; this is
// deliberately looser so a slow-but-finishing collection still lands, while a
// wedged one cannot hold a connection forever.
const scrapeTimeout = 30 * time.Second

// scrapeHandler serves reg as Prometheus exposition text, hardened against
// the pile-up bead pg2-jj0ym observed (a scrape not answering within 40s,
// abandoned scrapes not cancelling) — bead pg2-n6d8y item 3.
//
// A collection cannot be cancelled once started: the OTel exporter collects
// with a background context, so the request context never reaches the store
// reads behind the snapshot. What the handler can bound is the work a client
// causes by scraping:
//   - CoalesceGather: every scrape arriving while a collection is in flight
//     joins it and shares its result, so abandoned scrapes cost nothing extra
//     and at most ONE collection runs at a time;
//   - Timeout: a scrape not finished within timeout is answered 503 instead of
//     holding its connection open.
func scrapeHandler(reg prometheus.Gatherer, timeout time.Duration) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		CoalesceGather: true,
		Timeout:        timeout,
	})
}

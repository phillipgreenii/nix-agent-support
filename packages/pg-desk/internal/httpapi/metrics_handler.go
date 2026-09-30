package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	otelprometheus "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/metrics"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
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
func newMetricsHandler(st *store.Store, cfg *config.Config) (http.Handler, error) {
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
		payload, err := BuildPayload(st, cfg, nowUTC())
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
		return metrics.Snapshot{
			AgeSeconds:                payload.AgeSeconds,
			Stale:                     payload.Stale,
			DroppedCount:              payload.DroppedCount,
			SyncErrorRows:             syncRows,
			OldestSyncErrorAgeSeconds: oldestAge,
			SyncErrorRetryingRows:     retrying,
			SyncErrorExhaustedRows:    exhausted,
		}, nil
	}

	if _, err := metrics.New(mp, snapshotFn); err != nil {
		return nil, fmt.Errorf("httpapi: new metrics emitter: %w", err)
	}

	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{}), nil
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

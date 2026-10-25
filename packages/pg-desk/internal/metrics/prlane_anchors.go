package metrics

import (
	"context"

	"go.opentelemetry.io/otel/metric"
)

// The PR lane open-anchor families (PR lane refactor, P8a). They are derived
// from the store's links and PR rows at scrape time (store.CountPRAnchors), not
// from the sync ledger the older anchor gauges read, because P9 deletes the
// ledger. Every gauge is emitted with a value when it is healthy, so "no
// series" never means "healthy", and a scrape whose store read fails exports
// NO series for any of them, never 0.
const (
	// MetricPROpenAnchors is the number of PRs (hidden ones excluded) that
	// have at least one open anchor work item.
	MetricPROpenAnchors = "pg_desk_pr_open_anchors"
	// MetricPROpenAnchorsHidden is the number of hidden PRs that have at
	// least one open anchor; they are counted apart and take no part in the
	// other gauges.
	MetricPROpenAnchorsHidden = "pg_desk_pr_open_anchors_hidden"
	// MetricPRStaleAnchors is how many PRs with an open anchor the store shows
	// as merged or closed.
	MetricPRStaleAnchors = "pg_desk_pr_stale_anchors"
	// MetricPRStaleAnchorOldestAge is the age in seconds, among those, of the
	// oldest (measured from the PR row's last hydration, which is when the
	// store learned it was closed); 0 when there is none.
	MetricPRStaleAnchorOldestAge = "pg_desk_pr_stale_anchor_oldest_age_seconds"
	// MetricPROpenAnchorHydrationAge is the age in seconds of the oldest last
	// hydration among the PRs with an open anchor that the store still shows
	// as open: a lost or failed `merged` hydration leaves the store saying
	// "open", which the stale-anchor count cannot see. 0 when there is none.
	MetricPROpenAnchorHydrationAge = "pg_desk_pr_open_anchor_oldest_hydration_age_seconds"
)

// PRAnchorsSnapshot is the open-anchor view at one scrape.
type PRAnchorsSnapshot struct {
	Open, OpenHidden, Stale, StaleOldestAgeSeconds, OldestHydrationAgeSeconds int
}

// PRAnchorsFunc supplies the open-anchor view at collect time. It MUST return
// ok=false (and a nil error) for a store that cannot answer, such as one not
// migrated to schema 2: the families then export no series. A non-nil error
// also exports no series; neither case is ever reported as 0.
type PRAnchorsFunc func() (snap PRAnchorsSnapshot, ok bool, err error)

// RegisterPRAnchors registers the open-anchor gauges on a meter from mp, read
// through one callback so a scrape sees one consistent snapshot.
func RegisterPRAnchors(mp metric.MeterProvider, fn PRAnchorsFunc) error {
	m := mp.Meter("github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk")

	gauge := func(name, desc string) (metric.Int64ObservableGauge, error) {
		return m.Int64ObservableGauge(name, metric.WithDescription(desc))
	}
	open, err := gauge(MetricPROpenAnchors,
		"PRs (hidden excluded) with at least one open anchor work item, from the store's links; no series when the store read fails, never 0")
	if err != nil {
		return err
	}
	hidden, err := gauge(MetricPROpenAnchorsHidden,
		"hidden PRs with at least one open anchor work item, counted apart from the open-anchor gauge")
	if err != nil {
		return err
	}
	stale, err := gauge(MetricPRStaleAnchors,
		"PRs with an open anchor that the store shows as merged or closed (hidden excluded)")
	if err != nil {
		return err
	}
	staleAge, err := gauge(MetricPRStaleAnchorOldestAge,
		"age in seconds of the oldest stale anchor, from the PR row's last hydration, 0 when there is none")
	if err != nil {
		return err
	}
	hydAge, err := gauge(MetricPROpenAnchorHydrationAge,
		"age in seconds of the oldest last hydration among PRs with an open anchor that the store shows as open, 0 when there is none")
	if err != nil {
		return err
	}

	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		snap, ok, err := fn()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		o.ObserveInt64(open, int64(snap.Open))
		o.ObserveInt64(hidden, int64(snap.OpenHidden))
		o.ObserveInt64(stale, int64(snap.Stale))
		o.ObserveInt64(staleAge, int64(snap.StaleOldestAgeSeconds))
		o.ObserveInt64(hydAge, int64(snap.OldestHydrationAgeSeconds))
		return nil
	}, open, hidden, stale, staleAge, hydAge)
	return err
}

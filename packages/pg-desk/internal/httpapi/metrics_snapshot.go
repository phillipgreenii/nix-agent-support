package httpapi

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// Defaults for the background metrics snapshot. Variables only so a test can
// shorten them.
var (
	// snapshotInterval is how often the whole /metrics catalog is rebuilt in
	// the background (30 s by default; serve overrides it with
	// --metrics-snapshot-interval, WithMetricsSnapshotInterval). Scrapes never trigger a collection; they read the last
	// snapshot, so scrape latency is independent of host load.
	snapshotInterval = 30 * time.Second
	// snapshotFirstWait bounds how long a scrape arriving before the first
	// snapshot exists waits for it. After it, the scrape is answered with
	// only the snapshot-state metrics (no age gauge yet, errors visible).
	snapshotFirstWait = 5 * time.Second
)

// snapshot is one immutable result of the catalog build. A new one replaces
// the old by a single atomic pointer store (Read-Copy-Update), so a scrape
// reads it without a lock and never sees a half-built catalog.
type snapshot struct {
	fams []*dto.MetricFamily
	at   time.Time
}

// snapshotGatherer builds src's metric families on a timer and serves the
// cached result (Producer/Consumer with an RCU hand-off: one background
// goroutine produces, every scrape consumes the published pointer). Its own
// state is exported as three families so staleness is visible:
// pg_desk_metrics_snapshot_age_seconds (absent until the first snapshot),
// pg_desk_metrics_snapshot_compute_duration_seconds (a histogram) and
// pg_desk_metrics_snapshot_errors_total.
//
// The refresh loop is the only caller of src, one build at a time, so the
// compute is single-flight by construction: a build slower than the interval
// delays the next, it never overlaps it.
type snapshotGatherer struct {
	src       prometheus.Gatherer
	interval  time.Duration
	firstWait time.Duration

	cur   atomic.Pointer[snapshot]
	ready chan struct{} // closed on the first snapshot

	duration prometheus.Histogram
	errs     prometheus.Counter
	meta     prometheus.Gatherer
}

func newSnapshotGatherer(src prometheus.Gatherer, interval, firstWait time.Duration) *snapshotGatherer {
	g := &snapshotGatherer{src: src, interval: interval, firstWait: firstWait, ready: make(chan struct{})}
	g.duration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "pg_desk_metrics_snapshot_compute_duration_seconds",
		Help:    "How long one background /metrics snapshot build took.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
	})
	g.errs = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pg_desk_metrics_snapshot_errors_total",
		Help: "Snapshot builds that reported an error; the last good snapshot stays served.",
	})
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "pg_desk_metrics_snapshot_age_seconds",
			Help: "Seconds since the served /metrics snapshot was built; grows when builds are slow or failing.",
		}, g.age),
		g.duration,
		g.errs,
	)
	g.meta = reg
	return g
}

func (g *snapshotGatherer) age() float64 {
	cur := g.cur.Load()
	if cur == nil {
		return 0
	}
	return time.Since(cur.at).Seconds()
}

// Start runs the refresh loop until ctx ends: one build immediately, then one
// per interval.
func (g *snapshotGatherer) Start(ctx context.Context) {
	go func() {
		g.refresh()
		t := time.NewTicker(g.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if ctx.Err() != nil {
					return
				}
				g.refresh()
			}
		}
	}()
}

// refresh builds one snapshot. On an error the last good snapshot stays
// published and its age keeps growing (stale-on-error); only the very first
// build publishes a partial result, so a scrape before any good snapshot still
// sees whatever families the build produced.
func (g *snapshotGatherer) refresh() {
	start := time.Now()
	fams, err := g.src.Gather()
	g.duration.Observe(time.Since(start).Seconds())
	first := g.cur.Load() == nil
	if err != nil {
		g.errs.Inc()
		if !first || len(fams) == 0 {
			return
		}
	}
	g.cur.Store(&snapshot{fams: fams, at: time.Now()})
	if first {
		close(g.ready)
	}
}

// Gather returns the cached snapshot plus the snapshot-state families. It
// never collects and never touches the store: before the first snapshot it
// waits at most firstWait.
func (g *snapshotGatherer) Gather() ([]*dto.MetricFamily, error) {
	select {
	case <-g.ready:
	case <-time.After(g.firstWait):
	}
	cur := g.cur.Load()
	meta, err := g.meta.Gather()
	if err != nil {
		return nil, err
	}
	if cur == nil {
		// No snapshot yet: drop the meaningless age series.
		kept := meta[:0]
		for _, f := range meta {
			if f.GetName() != "pg_desk_metrics_snapshot_age_seconds" {
				kept = append(kept, f)
			}
		}
		return kept, nil
	}
	return append(append([]*dto.MetricFamily{}, cur.fams...), meta...), nil
}

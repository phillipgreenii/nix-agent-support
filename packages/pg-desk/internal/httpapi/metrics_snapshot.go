package httpapi

import (
	"context"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// Defaults for the background metrics snapshot. Variables only so a test can
// shorten them.
var (
	// snapshotInterval is how often the whole /metrics catalog is rebuilt in
	// the background. Scrapes never trigger a collection; they read the last
	// snapshot, so scrape latency is independent of host load.
	snapshotInterval = 15 * time.Second
	// snapshotFirstWait bounds how long a scrape arriving before the first
	// snapshot exists waits for it. After it, the scrape is answered with
	// only the snapshot-state metrics (no age gauge yet, errors visible).
	snapshotFirstWait = 5 * time.Second
)

// snapshotGatherer builds src's metric families on a timer and serves the
// cached result. Its own state is exported as three families so staleness is
// visible: pg_desk_metrics_snapshot_age_seconds (absent until the first
// snapshot), pg_desk_metrics_snapshot_refresh_duration_seconds and
// pg_desk_metrics_snapshot_refresh_errors_total.
type snapshotGatherer struct {
	src       prometheus.Gatherer
	interval  time.Duration
	firstWait time.Duration

	mu       sync.Mutex
	fams     []*dto.MetricFamily
	at       time.Time
	have     bool
	duration time.Duration
	errs     float64
	ready    chan struct{} // closed on the first snapshot

	meta prometheus.Gatherer
}

func newSnapshotGatherer(src prometheus.Gatherer, interval, firstWait time.Duration) *snapshotGatherer {
	g := &snapshotGatherer{src: src, interval: interval, firstWait: firstWait, ready: make(chan struct{})}
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "pg_desk_metrics_snapshot_age_seconds",
			Help: "Seconds since the served /metrics snapshot was built; grows when refreshes are slow or failing.",
		}, g.age),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "pg_desk_metrics_snapshot_refresh_duration_seconds",
			Help: "How long the last /metrics snapshot rebuild took.",
		}, func() float64 { g.mu.Lock(); defer g.mu.Unlock(); return g.duration.Seconds() }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "pg_desk_metrics_snapshot_refresh_errors_total",
			Help: "Snapshot rebuilds that reported an error.",
		}, func() float64 { g.mu.Lock(); defer g.mu.Unlock(); return g.errs }),
	)
	g.meta = reg
	return g
}

func (g *snapshotGatherer) age() float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return time.Since(g.at).Seconds()
}

// Start runs the refresh loop until ctx ends: one rebuild immediately, then
// one per interval (a rebuild slower than the interval simply delays the next).
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

func (g *snapshotGatherer) refresh() {
	start := time.Now()
	fams, err := g.src.Gather()
	d := time.Since(start)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.duration = d
	if err != nil {
		g.errs++
	}
	if err != nil && len(fams) == 0 {
		return // keep the previous snapshot; its age keeps growing
	}
	g.fams, g.at = fams, time.Now()
	if !g.have {
		g.have = true
		close(g.ready)
	}
}

// Gather returns the cached snapshot plus the snapshot-state families. It
// never collects: before the first snapshot it waits at most firstWait.
func (g *snapshotGatherer) Gather() ([]*dto.MetricFamily, error) {
	select {
	case <-g.ready:
	case <-time.After(g.firstWait):
	}
	g.mu.Lock()
	fams, have := g.fams, g.have
	g.mu.Unlock()
	meta, err := g.meta.Gather()
	if err != nil {
		return nil, err
	}
	if !have {
		// No snapshot yet: drop the meaningless age series.
		kept := meta[:0]
		for _, f := range meta {
			if f.GetName() != "pg_desk_metrics_snapshot_age_seconds" {
				kept = append(kept, f)
			}
		}
		meta = kept
	}
	return append(append([]*dto.MetricFamily{}, fams...), meta...), nil
}

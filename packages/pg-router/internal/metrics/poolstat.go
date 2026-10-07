package metrics

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.opentelemetry.io/otel/metric"
)

// Worktree-pool catalog members (pg2-kftf9.22). Optional: registered only
// when WithWorktreePool is passed, so the ten-member core catalog above is
// untouched for callers (tests, drain-and-exit) that do not opt in.
const (
	// MetricWorktreeCount is the number of per-bead worktree directories
	// currently under the pool's worktree_dir.
	MetricWorktreeCount = "pg_router_worktree_count"
	// MetricWorktreeBytes is the summed apparent size (lstat) of every file
	// under those worktrees, as of the last completed scan.
	MetricWorktreeBytes = "pg_router_worktree_bytes"
	// MetricWorktreeScanTruncated is 1 when the last scan hit its entry or
	// time budget, so MetricWorktreeBytes is a lower bound; else 0.
	MetricWorktreeScanTruncated = "pg_router_worktree_scan_truncated"
)

const (
	// defaultPoolScanTTL bounds how often the worktree tree is walked: a
	// scrape inside the TTL reads the cached result.
	defaultPoolScanTTL = 10 * time.Minute
	// poolScanMaxEntries and poolScanTimeout bound one scan's cost (a
	// monorepo checkout is ~213k files per worktree; see pg2-8vn8t). The
	// timeout was 60s, which truncated routinely on a full pool and made
	// pg_router_worktree_scan_truncated flap (pg2-9q3pq); 180s is sized so a
	// full pool completes. The scan is a background single-flight refresh, so
	// a longer budget never delays a scrape.
	poolScanMaxEntries = 2_000_000
	poolScanTimeout    = 180 * time.Second
)

// poolStat is one completed scan's result.
type poolStat struct {
	count     int64
	bytes     int64
	truncated bool
}

// poolScanner caches a worktree-pool scan with a TTL. Scrapes NEVER block on a
// walk: get() returns the last completed result immediately and, when stale,
// kicks at most one background refresh (single-flight). Until the first scan
// completes get() reports ok=false and the gauges observe nothing.
type poolScanner struct {
	dir  string
	ttl  time.Duration
	now  func() time.Time
	scan func(ctx context.Context, dir string) poolStat
	// timeout bounds one scan; set from poolScanTimeout (tests override).
	timeout time.Duration

	mu       sync.Mutex
	last     poolStat
	lastAt   time.Time
	have     bool
	inflight bool
}

func newPoolScanner(dir string, ttl time.Duration, now func() time.Time) *poolScanner {
	if ttl <= 0 {
		ttl = defaultPoolScanTTL
	}
	return &poolScanner{dir: dir, ttl: ttl, now: now, scan: scanWorktreePool, timeout: poolScanTimeout}
}

// get returns the cached stat, starting a background refresh if it is stale.
func (p *poolScanner) get() (poolStat, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if (!p.have || p.now().Sub(p.lastAt) >= p.ttl) && !p.inflight {
		p.inflight = true
		go p.refresh()
	}
	return p.last, p.have
}

// refresh runs one scan and stores the result. Exposed to tests via direct call.
func (p *poolScanner) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()
	st := p.scan(ctx, p.dir)
	p.mu.Lock()
	p.last, p.lastAt, p.have, p.inflight = st, p.now(), true, false
	p.mu.Unlock()
}

// scanWorktreePool counts the immediate subdirectories of dir (one per bead
// worktree) and sums the lstat size of every entry beneath them, without
// following symlinks. A missing dir is an empty pool. The walk stops at
// poolScanMaxEntries or when ctx expires, flagging the result truncated.
func scanWorktreePool(ctx context.Context, dir string) poolStat {
	var st poolStat
	entries, err := os.ReadDir(dir)
	if err != nil {
		return st
	}
	var seen int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		st.count++
		if st.truncated {
			continue
		}
		_ = filepath.WalkDir(filepath.Join(dir, e.Name()), func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable entry: skip, keep counting the rest
			}
			if seen++; seen > poolScanMaxEntries || ctx.Err() != nil {
				st.truncated = true
				return filepath.SkipAll
			}
			if info, ierr := d.Info(); ierr == nil && !d.IsDir() {
				st.bytes += info.Size()
			}
			return nil
		})
	}
	return st
}

// WithWorktreePool registers the three worktree-pool gauges, reading dir
// through a TTL cache (ttl <= 0 selects the 10-minute default). It adds no
// polling loop: a scan is started lazily by a scrape and runs in the
// background.
func WithWorktreePool(dir string, ttl time.Duration) Option {
	return func(o *options) { o.poolDir, o.poolTTL = dir, ttl }
}

func registerWorktreePool(m metric.Meter, sc *poolScanner) error {
	specs := []struct {
		name, unit, desc string
		val              func(poolStat) int64
	}{
		{MetricWorktreeCount, "{worktree}", "per-bead worktree directories under the pool's worktree_dir (cached scan, TTL-bounded)", func(s poolStat) int64 { return s.count }},
		{MetricWorktreeBytes, "By", "summed apparent size of files under the pool's worktrees, as of the last completed scan (lower bound when scan_truncated=1)", func(s poolStat) int64 { return s.bytes }},
		{MetricWorktreeScanTruncated, "{flag}", "1 when the last worktree scan hit its entry/time budget so worktree_bytes is a lower bound, else 0", func(s poolStat) int64 {
			if s.truncated {
				return 1
			}
			return 0
		}},
	}
	for _, sp := range specs {
		val := sp.val
		if _, err := m.Int64ObservableGauge(
			sp.name,
			metric.WithUnit(sp.unit), metric.WithDescription(sp.desc),
			metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
				if st, ok := sc.get(); ok {
					o.Observe(val(st))
				}
				return nil
			}),
		); err != nil {
			return err
		}
	}
	return nil
}

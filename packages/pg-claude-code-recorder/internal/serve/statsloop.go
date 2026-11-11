package serve

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// statsFirstWait is how long after start the stats loop first checks; statsCheckEvery
// is how often it re-checks after that. The once-a-day gate itself is
// store.StatsEvery, kept in the store's meta table so a restart does not reset it.
const (
	statsFirstWait  = 5 * time.Minute
	statsCheckEvery = time.Hour
)

// statsLoop keeps sqlite_stat1 current without stalling serving: when the last
// collection is older than store.StatsEvery it runs Store.CollectStats, which
// analyses one index per write transaction with a lowered analysis_limit. It is
// its own loop (not the doctor loop's step) so the first collection, which takes
// minutes on a large store, cannot hold up doctor. A failed or cut-short pass
// retries at the next check.
func (d *Daemon) statsLoop() {
	wait := statsFirstWait
	for {
		fire, stopTimer := d.timer("stats", wait)
		select {
		case <-d.stop:
			stopTimer()
			return
		case <-fire:
			now := d.clock.Now()
			if d.st.CollectStatsDue(now, store.StatsEvery) {
				if n, err := d.st.CollectStats(d.rootCtx(), store.DefaultAnalysisLimit, now); err != nil {
					d.obs.Log.Warn("stats_collect_failed", "err", err.Error(), "statements", n)
				} else {
					d.obs.Log.Info("stats_collected", "statements", n, "took_ms", d.clock.Now().Sub(now).Milliseconds())
				}
			}
			wait = statsCheckEvery
		}
	}
}

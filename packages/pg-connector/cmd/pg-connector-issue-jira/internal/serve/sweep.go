package serve

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/telemetry"
	libfeed "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	lq "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// defaultSweepInterval is how often expiry runs.
const defaultSweepInterval = 10 * time.Minute

// sweepLoop applies expiry and retention on a timer.
func (d *daemon) sweepLoop(ctx context.Context, qrun *lq.Runner, st *store.Store, tel *telemetry.Telemetry, cf *libfeed.Feed) {
	interval := d.o.Hooks.SweepInterval
	if interval <= 0 {
		interval = defaultSweepInterval
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		d.sweepOnce(ctx, qrun, st, tel, cf)
	}
}

// sweepOnce is one pass: the summary sweep (its evictions go to the
// store.evictions metric; tel may be nil), the query runner's expiry of a
// configured query nobody asked for, and the feed's retention (the change log
// and the stale consumer cursors). Nothing here restores a watched flag: a store
// that was moved aside lost its flags and baseline markers, and the members of a
// watched query become watched again at the next run of that query, as a
// baseline run.
func (d *daemon) sweepOnce(ctx context.Context, qrun *lq.Runner, st *store.Store, tel *telemetry.Telemetry, cf *libfeed.Feed) {
	evicted, err := summary.Sweep(ctx, st, d.cfg.Expiry.Std(), time.Now())
	if err != nil {
		d.log.Log("expiry_sweep_failed", "error", err.Error())
	}
	if tel != nil {
		tel.RecordEvictions(evicted)
	}
	if _, err := qrun.Expire(ctx, d.cfg.Expiry.Std()); err != nil {
		d.log.Log("query_expiry_failed", "error", err.Error())
	}
	if _, _, err := cf.Prune(ctx); err != nil {
		d.log.Log("change_retention_failed", "error", err.Error())
	}
}

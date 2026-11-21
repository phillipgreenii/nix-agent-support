package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The heartbeat's PR age sweep (docs/behavior/pg-desk/changes.md, "PR age
// sweep for open anchors"). The daemon feeds the PR lane with change events
// only, so a quiet PR is never re-read on its own, and a lost `merged` or
// `closed` hydration leaves the store saying "open" with an open anchor for
// ever. The sweep re-hydrates, oldest first, the open-anchor PRs whose last
// hydration is older than sweep.max_age, which is the same age bound the thread
// sweep uses. It is the recovery the pg_desk_pr_open_anchor_oldest_hydration_age_seconds
// alert asks for, run for the operator instead of by hand with `pg-desk pr
// refresh`. Like the catch-up it MUST NOT make a heartbeat late or missed, so
// it is bounded in rows and in time and never fails the heartbeat.
const (
	// PRSweepMaxRows is the most PRs one heartbeat re-hydrates. A heartbeat
	// runs each minute, so this is a rate, not a backlog limit: at 3 a minute
	// 180 open-anchor PRs a hour are swept, far above what sweep.max_age needs.
	PRSweepMaxRows = 3
	// PRSweepBudget is the cap of the sweep's time budget; the heartbeat period
	// can leave less (see catchUpBudgetLeft).
	PRSweepBudget = 10 * time.Second
	// PRSweepRetryAfter is how long a PR whose hydration just failed is left
	// alone, so one PR that keeps failing does not hold the front of the queue.
	PRSweepRetryAfter = 30 * time.Minute
)

// prSweepOutcome is what one sweep pass did.
type prSweepOutcome struct {
	selected, refreshed, failed int
	// stoppedBy is "" or "budget".
	stoppedBy string
}

// runPRSweep is the heartbeat's PR sweep step. It never returns an error: a
// problem is at most one stderr line.
func runPRSweep(cmd *cobra.Command, st *store.Store, start time.Time) {
	errOut := cmd.ErrOrStderr()
	if Version == "dev" {
		return
	}
	budget := catchUpBudgetLeft(start, heartbeatClock())
	if budget > PRSweepBudget {
		budget = PRSweepBudget
	}
	if budget < catchUpMinBudget {
		_, _ = fmt.Fprintf(errOut, "heartbeat: pr age sweep skipped: the period has %s left\n", budget.Round(time.Second))
		return
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), budget)
	defer cancel()
	out, err := prSweep(ctx, cmd, st, PRSweepMaxRows)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "heartbeat: pr age sweep: %v\n", err)
		return
	}
	if out.failed > 0 || out.stoppedBy != "" {
		_, _ = fmt.Fprintf(errOut, "heartbeat: pr age sweep: %d selected, %d refreshed, %d failed (stopped by %q)\n",
			out.selected, out.refreshed, out.failed, out.stoppedBy)
	}
}

// prSweep re-hydrates at most maxRows open-anchor PRs whose last hydration is
// older than sweep.max_age, oldest first, stopping between rows when ctx is
// done. Each PR is read through refreshEntity (the one hydration path a
// targeted refresh uses), so a failed or degraded read writes nothing and the
// PR stays due; the attempt time is stamped before the read so a failing PR
// steps aside for PRSweepRetryAfter.
func prSweep(ctx context.Context, cmd *cobra.Command, st *store.Store, maxRows int) (prSweepOutcome, error) {
	var out prSweepOutcome
	cfg, err := deskConfigLoad(ctx)
	if err != nil {
		return out, fmt.Errorf("load config: %w", err)
	}
	if len(cfg.Repos) == 0 {
		return out, errors.New("no repository configured")
	}
	repo := cfg.Repos[0].Remote
	if err := requireNewSchemaForTypedVerb(ctx, st); err != nil {
		return out, err
	}
	// A second sweep skips rather than queueing behind this one.
	unlock, err := store.NewLocker(store.LockerOptions{LockDir: filepath.Dir(st.Path()), Timeout: time.Millisecond}).Lock(repo, "pr-sweep", "heartbeat")
	if err != nil {
		if errors.Is(err, store.ErrLockTimeout) {
			return out, nil
		}
		return out, err
	}
	defer unlock()

	now := heartbeatClock()
	ids, err := st.SweepPRAnchors(ctx, now, cfg.SweepMaxAge(), PRSweepRetryAfter, maxRows)
	if err != nil {
		return out, err
	}
	out.selected = len(ids)
	for _, id := range ids {
		if ctx.Err() != nil {
			out.stoppedBy = "budget"
			break
		}
		attempt := heartbeatClock().UTC().Format(time.RFC3339Nano)
		if err := st.SetEntityReadTimes(ctx, repo, entityTypePR, id, &attempt, nil); err != nil && !errors.Is(err, store.ErrNoEntity) {
			return out, err
		}
		res, herr := refreshEntity(ctx, cfg, st, entityTypePR, id)
		if herr != nil || res.Degraded != "" {
			out.failed++
			continue
		}
		out.refreshed++
	}
	return out, nil
}

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The heartbeat catch-up (docs/behavior/pg-desk/reinterpret.md, "The heartbeat
// catch-up"; INV-REINTERPRET-10). It re-interprets the open PRs whose
// interpretation is behind the running stamp, and it MUST NOT make a heartbeat
// late or missed (operator, 2026-11-10: "we can try it as part of the
// heartbeat, but it think we need to ensure there is some kind of limit so that
// heartbeats aren't missed").
const (
	// CatchUpBudget is the cap of the catch-up's time budget. The actual budget
	// is the smaller of this and the time the heartbeat period leaves (see
	// catchUpBudgetLeft), because the steps before the catch-up are themselves
	// bounded only by 45 s (the probe) and the step after it by 30 s (the
	// ledger read): 45 + 30 already exceeds the 60 s period, so no fixed
	// budget is safe on its own. The budget MUST NOT be raised.
	CatchUpBudget = 10 * time.Second
	// CatchUpMaxRows is the most rows one pass re-interprets, open PRs first.
	CatchUpMaxRows = 25

	// heartbeatPeriod is the period of the deployed desk-heartbeat trigger
	// (operator-commands.md, "heartbeat / heartbeat-item"); it lives in the
	// deployment module, so this is a stated assumption, not read config.
	heartbeatPeriod = 60 * time.Second
	// catchUpMargin is kept free for the store's 5 s busy timeout, which can
	// stretch the one row in flight past the deadline.
	catchUpMargin = 5 * time.Second
	// catchUpMinBudget is the least time worth starting a pass for.
	catchUpMinBudget = time.Second
)

// heartbeatClock is the monotonic clock of the budget arithmetic; tests
// replace it.
var heartbeatClock = time.Now

// catchUpBeforeRow, when set, runs before each row (a test seam for a slow
// row).
var catchUpBeforeRow func(store.InterpretationKey)

// catchUpBudgetLeft is the time the catch-up may run when the heartbeat began
// at start and it is now: CatchUpBudget, cut so that the step after it (the
// ledger read, bounded by heartbeatLedgerShowTimeout) plus catchUpMargin still
// finishes inside the period. A result below catchUpMinBudget means skip.
func catchUpBudgetLeft(start, now time.Time) time.Duration {
	left := heartbeatPeriod - heartbeatLedgerShowTimeout - catchUpMargin - now.Sub(start)
	if left > CatchUpBudget {
		return CatchUpBudget
	}
	return left
}

// catchUpOutcome is what one catch-up pass did.
type catchUpOutcome struct {
	counts reinterpretCounts
	// stoppedBy is "", "budget" or "row cap".
	stoppedBy string
	// remaining is the number of interpretations still behind after a pass that
	// stopped at a limit; it is not computed otherwise.
	remaining int
}

// runCatchUp is the heartbeat's catch-up step. It never returns an error: a
// problem is at most one stderr line, so it can neither fail nor delay the
// heartbeat beyond the budget it was given.
func runCatchUp(cmd *cobra.Command, st *store.Store, start time.Time) {
	errOut := cmd.ErrOrStderr()
	if Version == "dev" {
		return
	}
	budget := catchUpBudgetLeft(start, heartbeatClock())
	if budget < catchUpMinBudget {
		_, _ = fmt.Fprintf(errOut, "heartbeat: interpretation catch-up skipped: the period has %s left\n", budget.Round(time.Second))
		return
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), budget)
	defer cancel()
	out, err := catchUp(ctx, cmd, st, CatchUpMaxRows)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "heartbeat: interpretation catch-up: %v\n", err)
		return
	}
	if out.stoppedBy != "" {
		_, _ = fmt.Fprintf(errOut, "heartbeat: interpretation catch-up stopped at the %s: %d changed, %d unchanged, %d stamped only, %d failed, %d remaining\n",
			out.stoppedBy, out.counts.Changed, out.counts.Unchanged, out.counts.StampedOnly, out.counts.Failed, out.remaining)
	}
}

// catchUp runs one pass over at most maxRows rows behind the running stamp,
// stopping when ctx is done. A row in flight is never interrupted (it is one
// short transaction), so a deadline cannot half-write a row or mark one failed.
func catchUp(ctx context.Context, cmd *cobra.Command, st *store.Store, maxRows int) (catchUpOutcome, error) {
	var out catchUpOutcome
	cfg, err := deskConfigLoad(ctx)
	if err != nil {
		return out, fmt.Errorf("load config: %w", err)
	}
	if len(cfg.Repos) == 0 {
		return out, errors.New("no repository configured")
	}
	repo := cfg.Repos[0].Remote
	if err := requireNewSchemaForTypedVerb(st); err != nil {
		return out, err
	}
	// A second catch-up skips rather than queueing behind this one.
	unlock, err := store.NewLocker(store.LockerOptions{LockDir: filepath.Dir(st.Path()), Timeout: time.Millisecond}).Lock(repo, "catch-up", "heartbeat")
	if err != nil {
		if errors.Is(err, store.ErrLockTimeout) {
			return out, nil
		}
		return out, err
	}
	defer unlock()

	r := newReinterpreter(cmd, cfg, st, repo, false, false)
	r.ctx = context.WithoutCancel(ctx)
	r.out = io.Discard
	keys, known, err := st.InterpretationsBehind(repo, maxRows)
	if err != nil {
		return out, err
	}
	if !known || len(keys) == 0 {
		return out, nil
	}
	for _, k := range keys {
		if ctx.Err() != nil {
			out.stoppedBy = "budget"
			break
		}
		if h := catchUpBeforeRow; h != nil {
			h(k)
		}
		r.one(k, false)
	}
	if out.stoppedBy == "" && len(keys) == maxRows {
		out.stoppedBy = "row cap"
	}
	out.counts = r.counts
	if out.stoppedBy != "" {
		// Best effort: the count only feeds the stderr line, so a failed read
		// makes the line vanish rather than the pass report an error.
		rest, _, _ := st.InterpretationsBehind(repo, 0)
		out.remaining = len(rest)
		if out.remaining == 0 {
			out.stoppedBy = ""
		}
	}
	return out, nil
}

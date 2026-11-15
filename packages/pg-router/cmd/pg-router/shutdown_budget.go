package main

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/x/lifecycle"
)

// defaultShutdownDeadline is the shutdown deadline when --shutdown-deadline is
// not passed. The deployment (the nix module) owns the real value: it passes
// the flag and derives launchd's ExitTimeOut from the same number, so this
// default only serves a hand-started daemon and run-until-idle. It equals the
// value the module passes today.
const defaultShutdownDeadline = 53 * time.Second

// shutdownBudget splits ONE shutdown deadline between the phases that run once
// the signal context ends. Every phase draws its allowance from the deadline
// (a share of it, never a fixed constant), so the sum of the shares cannot
// exceed the deadline, whatever the deployment passes.
//
// The shares are per mille of the deadline; at the 53 s default they come to
// about 20 s of drain, 5 s of preShutdown hooks, 8 s of in-flight tail and 3 s
// of metrics flush, accept-loop close and store close, the figures the daemon
// used before the deadline became a flag.
type shutdownBudget struct {
	Deadline    time.Duration
	Drain       time.Duration // in-flight dispatches finish before their context is cancelled
	PreShutdown time.Duration // the handlers' preShutdown hooks
	Tail        time.Duration // the cancelled dispatches unwind before the store closes
	Flush       time.Duration // metrics flush, accept-loop close, store close
}

// Per-mille shares of the deadline. Their sum MUST stay under 1000 so the
// deadline keeps a margin over the phases.
const (
	drainShare       = 380
	preShutdownShare = 95
	tailShare        = 150
	flushShare       = 55
)

// newShutdownBudget derives every phase budget from deadline (zero or negative
// selects defaultShutdownDeadline).
func newShutdownBudget(deadline time.Duration) shutdownBudget {
	if deadline <= 0 {
		deadline = defaultShutdownDeadline
	}
	share := func(permille int64) time.Duration { return deadline / 1000 * time.Duration(permille) }
	return shutdownBudget{
		Deadline:    deadline,
		Drain:       share(drainShare),
		PreShutdown: share(preShutdownShare),
		Tail:        share(tailShare),
		Flush:       share(flushShare),
	}
}

// Sum is the total of the phase budgets.
func (b shutdownBudget) Sum() time.Duration { return b.Drain + b.PreShutdown + b.Tail + b.Flush }

// shutdownClock tracks how much of the one deadline is left, from the moment
// the signal context ends, so a phase never outlives the deadline even when an
// earlier phase overran its own share.
type shutdownClock struct {
	deadline time.Duration
	endedAt  atomic.Int64 // unix nanoseconds; zero until the context ends
}

// newShutdownClock starts the clock when ctx ends.
func newShutdownClock(ctx context.Context, deadline time.Duration) *shutdownClock {
	c := &shutdownClock{deadline: deadline}
	context.AfterFunc(ctx, func() { c.endedAt.Store(time.Now().UnixNano()) })
	return c
}

// Elapsed returns the time since the context ended; ok is false while it has
// not ended.
func (c *shutdownClock) Elapsed() (d time.Duration, ok bool) {
	ended := c.endedAt.Load()
	if ended == 0 {
		return 0, false
	}
	return time.Since(time.Unix(0, ended)), true
}

// Within returns the allowance for a phase with the given share: the share, or
// what is left of the deadline when that is less (never negative).
func (c *shutdownClock) Within(share time.Duration) time.Duration {
	ended := c.endedAt.Load()
	if ended == 0 {
		return min(share, c.deadline)
	}
	left := c.deadline - time.Since(time.Unix(0, ended))
	return max(min(share, left), 0)
}

// runGroupOptions builds the lifecycle options of a run or run-until-idle
// process. The log MUST be a raw logger: a hung telemetry path must not block
// the overrun line. nil log selects stderr. exit nil selects os.Exit.
func runGroupOptions(deadline time.Duration, log *slog.Logger, exit func(int)) lifecycle.Options {
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if deadline <= 0 {
		deadline = defaultShutdownDeadline
	}
	return lifecycle.Options{Deadline: deadline, ExitCode: 10, Log: log, Exit: exit}
}

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/usagelimit"
)

// exitUsageLimited is the exit code `ccpool new` and `ccpool reply` return when
// they decline to accept work because an account usage window (the 5-hour block
// or the weekly limit) is at its limit. A distinct code >= 2 (1 stays the
// generic error; 5, 6 and 7 are reply's busy, cancel-unconfirmed and
// prompt-not-ingested) so a caller can branch on "not right now, try after the
// reset" without parsing stderr. The refusal line names the window and its reset.
const exitUsageLimited = 8

// newUsageChecker builds the Checker for a pool's [usage_gate] config. It is a
// package-level indirection (the same seam style as recordRetry) so tests inject
// a fake and never run the real monitor binary.
var newUsageChecker = productionUsageChecker

// productionUsageChecker maps the [usage_gate] config onto its Checker: Off when
// disabled, otherwise the Gate that asks the configured monitor command.
func productionUsageChecker(g config.UsageGate) usagelimit.Checker {
	if !g.Enabled {
		return usagelimit.Off{}
	}
	return usagelimit.Gate{
		Command:      g.Command,
		ThresholdPct: g.ThresholdPct,
		Timeout:      time.Duration(g.Timeout),
	}
}

// currentUsageLimit returns the usage window that is at its limit, or nil when
// none is or when that cannot be determined. It FAILS OPEN: a monitor that is
// absent or down is logged and read as "not blocked", never as a refusal — ccpool
// must keep working when it cannot find out whether it should.
func currentUsageLimit(ctx context.Context, cfg config.Config) *usagelimit.Limit {
	l, err := newUsageChecker(cfg.UsageGate).Check(ctx)
	if err != nil {
		slog.Warn("usage gate unavailable; accepting work", "err", err)
		return nil
	}
	return l
}

// refuseOnUsageLimit is the admission check `new` and `reply` run before doing
// anything that accepts work. When a usage window is at its limit it prints the
// refusal (window, percentage, reset) to stderr and reports (exitUsageLimited,
// true); otherwise (0, false) and the caller proceeds. cmd names the command in
// the message.
func refuseOnUsageLimit(cmd string, cfg config.Config) (int, bool) {
	l := currentUsageLimit(context.Background(), cfg)
	if l == nil {
		return 0, false
	}
	fmt.Fprintf(os.Stderr, "%s: not accepting work: %s\n", cmd, l)
	return exitUsageLimited, true
}

package runner

import (
	"context"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
)

const (
	// CatchUpCooldown is how long after its newest attempt ended a Skipped
	// scheduled window waits before catch-up tries it again.
	CatchUpCooldown = 55 * time.Minute
	// CatchUpMaxAttempts caps the runs that may share one run key.
	CatchUpMaxAttempts = 24
)

// CatchUpParams are the `job catch-up` inputs. Lens and Scope narrow the
// search; Force ignores the cooldown, the attempt cap and the expiry and also
// accepts a window whose latest run Failed.
type CatchUpParams struct {
	Lens, Scope string
	Force       bool
}

// NothingToCatchUpError means no scheduled window is eligible (exit 20, no row).
type NothingToCatchUpError struct{}

func (*NothingToCatchUpError) Error() string { return "no skipped scheduled window to catch up" }

// CatchUp re-runs at most one scheduled window whose latest run ended Skipped
// and that no run ever Filed. It reuses the window, run key and schedule id of
// that run, so a Filed result consumes the key and the window stops being
// offered; a Skipped result writes another Skipped row, as a scheduled run
// would. Failed runs (an analyst failure) are not retried unless forced.
func CatchUp(ctx context.Context, d *Deps, p CatchUpParams) (Result, error) {
	d.defaults()
	if p.Force && p.Lens == "" {
		return Result{}, &UsageError{Msg: "--force needs a lens"}
	}
	ws, err := d.Ledger.ScheduledWindows(ctx)
	if err != nil {
		return Result{}, err
	}
	now := d.Now()
	var cands []ledger.ScheduledWindow
	for _, w := range ws {
		if p.Lens != "" && w.Lens != p.Lens {
			continue
		}
		if p.Scope != "" && w.Scope != p.Scope {
			continue
		}
		if eligible(d, w, now, p.Force) {
			cands = append(cands, w)
		}
	}
	if len(cands) == 0 {
		return Result{}, &NothingToCatchUpError{}
	}
	// Oldest window end first: it is the closest to expiring.
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].Latest.WindowTo < cands[j].Latest.WindowTo })
	r := cands[0].Latest
	return Run(ctx, d, Params{
		Lens: r.Lens, Scope: r.Scope, From: r.WindowFrom, To: r.WindowTo,
		RunKey: r.RunKey, ScheduleID: r.ScheduleID,
	})
}

func eligible(d *Deps, w ledger.ScheduledWindow, now time.Time, force bool) bool {
	if w.Filed || w.Latest.RunKey == "" {
		return false
	}
	if _, ok := d.Cfg.Lens(w.Lens); !ok {
		return false
	}
	if _, ok := d.Cfg.Scope(w.Scope); !ok {
		return false
	}
	from, to, ok := w.Bounds()
	if !ok {
		return false
	}
	switch w.Latest.State {
	case ledger.RunSkipped:
	case ledger.RunFailed:
		if !force {
			return false
		}
	default:
		return false
	}
	if force {
		return true
	}
	if w.Attempts >= CatchUpMaxAttempts {
		return false
	}
	last := w.Latest.EndedAt
	if last.IsZero() {
		last = w.Latest.CreatedAt
	}
	if now.Sub(last) < CatchUpCooldown {
		return false
	}
	return now.Before(to.Add(to.Sub(from)))
}

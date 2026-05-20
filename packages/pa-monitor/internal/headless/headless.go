package headless

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/cmuxstatus"
	"github.com/phillipgreenii/pa-monitor/internal/render"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

type Poller interface {
	Snapshot(ctx context.Context) (*aggregate.Tree, bool, error)
}

type Opts struct {
	Poller                Poller
	Interval              time.Duration
	ConsecutiveIdleChecks int
	Maximum               time.Duration
	Writer                io.Writer
	CmuxSidebarEnable     bool
}

func Run(ctx context.Context, o Opts) int {
	reporter := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable: o.CmuxSidebarEnable,
		// Headless mode has no shared ErrorLogger; drop log lines for v1.
		Logf: func(string) {},
	})
	defer reporter.Clear()

	start := time.Now()
	idleStreak := 0
	for {
		snapCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		tree, anyWorking, err := o.Poller.Snapshot(snapCtx)
		cancel()
		if err != nil {
			fmt.Fprintf(o.Writer, "error: %v\n", err)
		} else {
			now := time.Now()
			fmt.Fprintln(o.Writer, render.Controls(render.ControlsOpts{}))
			fmt.Fprintln(o.Writer, render.BlockRow(tree, render.BlockRowOpts{Now: now}))
			if a := render.Alerts(tree, render.AlertsOpts{Now: now}); a != "" {
				fmt.Fprintln(o.Writer, a)
			}
			fmt.Fprint(o.Writer, render.Tree(tree, render.TreeOpts{}))

			snapshot := buildHeadlessSnapshot(tree, false, false)
			reporter.Push(snapshot)
		}
		if anyWorking {
			idleStreak = 0
		} else {
			idleStreak++
			if idleStreak >= o.ConsecutiveIdleChecks {
				return 0
			}
		}
		if time.Since(start) >= o.Maximum {
			return 1
		}
		select {
		case <-time.After(o.Interval):
		case <-ctx.Done():
			return 1
		}
	}
}

// buildHeadlessSnapshot mirrors Model.buildSidebarSnapshot but takes raw inputs
// since headless does not own a *Model. autoResume is always false in headless.
func buildHeadlessSnapshot(tree *aggregate.Tree, caffeinateOn bool, autoResume bool) cmuxstatus.Snapshot {
	state, resetAt := aggregateHeadlessState(tree)
	prog, label, ok := windowProgressHeadless(tree, time.Now())
	return cmuxstatus.Snapshot{
		CaffeinateOn:  caffeinateOn,
		NudgeOn:       autoResume,
		State:         state,
		PausedResetAt: resetAt,
		Progress:      prog,
		ProgressLabel: label,
		HasProgress:   ok,
	}
}

// aggregateHeadlessState duplicates the tui-package aggregateState so headless
// can compute without importing the tui package (which would pull in Bubble
// Tea). Keep this in sync if the algorithm changes.
func aggregateHeadlessState(tree *aggregate.Tree) (cmuxstatus.State, time.Time) {
	if tree == nil {
		return cmuxstatus.StateUnknown, time.Time{}
	}
	if !tree.WindowResetsAt.IsZero() {
		return cmuxstatus.StatePaused, tree.WindowResetsAt
	}
	anyWorking, anyIdle := false, false
	for _, d := range tree.Dirs {
		for _, sv := range d.Sessions {
			switch sv.Status {
			case session.Working:
				anyWorking = true
			case session.Dormant:
				// ignore
			default:
				anyIdle = true
			}
		}
	}
	switch {
	case anyWorking:
		return cmuxstatus.StateWorking, time.Time{}
	case anyIdle:
		return cmuxstatus.StateIdle, time.Time{}
	default:
		return cmuxstatus.StateDormant, time.Time{}
	}
}

func windowProgressHeadless(tree *aggregate.Tree, now time.Time) (float64, string, bool) {
	_ = now // retained for signature parity; the cost-based metric doesn't depend on wall-clock
	if tree == nil {
		return 0, "", false
	}
	if !tree.WindowResetsAt.IsZero() {
		return 1.0, "5h block exhausted — waiting for reset", true
	}
	b := tree.ActiveBlock
	if b == nil || tree.PlanCapUSD <= 0 {
		return 0, "", false
	}
	pct := 100 * b.CostUSD / tree.PlanCapUSD
	v := pct / 100
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	return v, fmt.Sprintf("5h block %.0f%% of cap", pct), true
}

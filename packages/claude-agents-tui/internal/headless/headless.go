package headless

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/render"
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
}

func Run(ctx context.Context, o Opts) int {
	start := time.Now()
	idleStreak := 0
	for {
		snapCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		tree, anyWorking, err := o.Poller.Snapshot(snapCtx)
		cancel()
		if err != nil {
			fmt.Fprintf(o.Writer, "error: %v\n", err)
		} else {
			fmt.Fprint(o.Writer, render.Header(tree, render.HeaderOpts{}))
			fmt.Fprint(o.Writer, render.Tree(tree, render.TreeOpts{}))
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

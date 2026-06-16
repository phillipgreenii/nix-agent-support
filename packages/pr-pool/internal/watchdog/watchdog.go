// Package watchdog meters a running worker session against a Budget and escalates.
package watchdog

import (
	"context"
	"errors"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/budget"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/eventlog"
	"github.com/phillipgreenii/pr-pool/internal/usage"
)

// ErrBudgetExceeded is returned by Run when the session hit 100% of its budget.
var ErrBudgetExceeded = errors.New("session budget exceeded")

// GitRunner runs `git -C <dir> <args...>` (injectable for tests).
type GitRunner interface {
	Run(ctx context.Context, dir string, args ...string) error
}

// Watchdog meters a session against a Budget and fires escalation actions.
type Watchdog struct {
	Reader                 usage.Reader
	CC                     ccpool.Runner
	BD                     beads.Runner
	Log                    *eventlog.Writer // may be nil (no-op)
	Budget                 budget.Budget
	RepoRoot, WorktreeDir  string
	ReminderMsg, WrapUpMsg string
	Git                    GitRunner
	Now                    func() time.Time
	Poll                   time.Duration

	// ClaimTerminal, if set, is called once the hard stop fires, BEFORE the
	// terminal bead mutations. It must return true to exactly one racer across
	// all parties competing for the single terminal outcome (here: this watchdog
	// vs the orchestrator's bead-poll). A false return means the bead-poll
	// already owns the outcome, so Run skips its mutations and waits for ctx
	// cancellation. nil ⇒ no racer ⇒ always own (standalone use / tests). (pg2-c1vp)
	ClaimTerminal func() bool
}

func (w *Watchdog) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Watchdog) emit(level, kind, msg string, fields map[string]any) {
	if w.Log != nil {
		_ = w.Log.Emit(level, kind, msg, fields)
	}
}

// Run meters the session until ctx is cancelled (the bead-poll won the race) or
// the budget hard stop fires. Returns ctx.Err() on cancellation (no action), or
// ErrBudgetExceeded after running the terminal sequence at 100%.
func (w *Watchdog) Run(ctx context.Context, sessionName, beadID string) error {
	start := w.now()
	highest := budget.None
	for {
		snap, _ := w.Reader.Read(ctx, w.transcriptPath(ctx, sessionName))
		_, level := w.Budget.Evaluate(snap, w.now().Sub(start))
		if level > highest {
			highest = level
			switch level {
			case budget.Reminder:
				_ = w.CC.Send(ctx, sessionName, w.ReminderMsg, ccpool.ModeQueue)
				w.emit("info", "reminder", "budget reminder threshold reached",
					map[string]any{"session": sessionName, "bead": beadID})
			case budget.Cancel:
				_ = w.CC.Cancel(ctx, sessionName)
				_ = w.CC.Send(ctx, sessionName, w.WrapUpMsg, ccpool.ModeQueue)
				w.emit("warn", "cancel", "budget cancel threshold reached",
					map[string]any{"session": sessionName, "bead": beadID})
			case budget.Hard:
				if w.ClaimTerminal == nil || w.ClaimTerminal() {
					w.terminal(ctx, sessionName, beadID)
					return ErrBudgetExceeded
				}
				// Lost the terminal race: the bead-poll owns the outcome. Touch
				// nothing; wait for the orchestrator to cancel, then exit. (pg2-c1vp)
				<-ctx.Done()
				return ctx.Err()
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(w.Poll):
		}
	}
}

// transcriptPath looks up the session's transcript path from ccpool.List.
func (w *Watchdog) transcriptPath(ctx context.Context, name string) string {
	sessions, err := w.CC.List(ctx)
	if err != nil {
		return ""
	}
	for _, s := range sessions {
		if s.Name == name {
			return s.TranscriptPath
		}
	}
	return ""
}

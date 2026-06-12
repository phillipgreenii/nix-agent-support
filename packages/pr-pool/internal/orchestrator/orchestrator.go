// Package orchestrator is pr-pool's mechanical drive loop: discover → per-role
// bounded drain → teardown-all. It owns no claude/tmux mechanics (ccpool does)
// and no LLM. Completion is bead-status-based; ccpool state is liveness only.
package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/complete"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/eventlog"
	"github.com/phillipgreenii/pr-pool/internal/roles"
	"github.com/phillipgreenii/pr-pool/internal/usage"
	"github.com/phillipgreenii/pr-pool/internal/watchdog"
)

type Orchestrator struct {
	CC          ccpool.Runner
	BD          beads.Runner
	Reg         roles.Registry
	Cfg         config.Config
	Log         *eventlog.Writer                           // may be nil (no-op); threaded onto Watchdog
	now         func() time.Time                           // clock seam (default time.Now)
	tick        func(context.Context, time.Duration) error // cancellable wait (default below)
	usageReader usage.Reader                               // default usage.NewTranscriptReader()
}

func (o *Orchestrator) reader() usage.Reader {
	if o.usageReader != nil {
		return o.usageReader
	}
	return usage.NewTranscriptReader()
}

func (o *Orchestrator) clock() time.Time {
	if o.now != nil {
		return o.now()
	}
	return time.Now()
}

// waitPoll blocks for d or until ctx is cancelled; returns ctx.Err() if cancelled.
func (o *Orchestrator) waitPoll(ctx context.Context, d time.Duration) error {
	if o.tick != nil {
		return o.tick(ctx, d)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// DrainOnce runs one pass: gate check, discover, drain each role up to its cap,
// teardown all pr-pool sessions. Returns nil even when individual beads fail
// (failures are recorded on the beads via OnFailure), matching the bash.
func (o *Orchestrator) DrainOnce(ctx context.Context, selfLogin string) error {
	if o.gated() {
		slog.Info("gated; pausing without dispatch")
		return nil // NOTE: gated exit does NOT teardown (no sessions were created)
	}
	defer o.teardownAll(ctx) // always run teardown after the gated check, even on error
	dispatches, err := discover.Discover(ctx, o.BD, o.Reg, selfLogin)
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}
	o.drain(ctx, o.Reg.Feedback, dispatches)
	o.drain(ctx, o.Reg.Worker, dispatches)
	return nil
}

func (o *Orchestrator) drain(ctx context.Context, role roles.Role, all []discover.Dispatch) {
	worked := 0
	for _, d := range all {
		if d.Role.Kind != role.Kind {
			continue
		}
		if worked >= role.Cap {
			break
		}
		if err := o.workOne(ctx, d); err != nil {
			slog.Warn("bead flagged", "role", role.Name, "bead", d.BeadID, "err", err)
		} else {
			slog.Info("bead complete", "role", role.Name, "bead", d.BeadID)
		}
		worked++
	}
}

// workOne dispatches a single bead: Ensure a fresh per-bead session, Send the
// nudge (async), then wait for completion. The session is torn down by the
// pass-level teardownAll, not here (so strays are reaped uniformly).
// For worker dispatches the nudge includes the budget prompt line and completion
// races against the budget watchdog. Feedback dispatches keep the prior behavior.
func (o *Orchestrator) workOne(ctx context.Context, d discover.Dispatch) error {
	name := d.Role.SessionName(o.Cfg.SessionPrefix, d.BeadID)
	env := map[string]string{
		"BEADS_ACTOR":    d.Role.Actor,
		"BEADS_DIR":      o.Cfg.RepoRoot + "/.beads",
		"WORKSPACE_ROOT": o.Cfg.RepoRoot,
	}
	if err := o.CC.Ensure(ctx, name, o.Cfg.RepoRoot, env); err != nil {
		// Could not even create the session. Match the bash (work_one:
		// `ensure_session || return 1`): NO failure action here — the bead was
		// never dispatched, so we do not flag/unclaim it. A transient ccpool
		// launch hiccup must not permanently mark a worker bead `human`.
		return fmt.Errorf("ensure %s: %w", name, err)
	}
	nudge := d.Role.Nudge(d.BeadID, o.Cfg.WorktreeDir)
	if d.Role.Kind == roles.Worker {
		nudge += o.Cfg.WorkerBudget().PromptLine()
	}
	if err := o.CC.Send(ctx, name, nudge, ccpool.ModeNoWait); err != nil {
		// J-dispatch-fail: feedback unclaims; worker is left for human inspection.
		if d.Role.Kind == roles.Feedback {
			_ = beads.Unclaim(ctx, o.BD, d.BeadID)
		}
		return fmt.Errorf("send %s: %w", name, err)
	}
	if d.Role.Kind != roles.Worker {
		return o.waitDone(ctx, d, name) // feedback: no watchdog (unchanged behavior)
	}
	return o.workerWaitWithWatchdog(ctx, d, name)
}

// workerWaitWithWatchdog runs waitDone and the budget watchdog concurrently.
// First to return a terminal result wins and cancels the other. The cancelled
// loser returns ctx.Err() (waitDone skips its failure action by design), so only
// the winner's outcome takes effect.
func (o *Orchestrator) workerWaitWithWatchdog(ctx context.Context, d discover.Dispatch, name string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	wd := &watchdog.Watchdog{
		Reader:      o.reader(),
		CC:          o.CC,
		BD:          o.BD,
		Log:         o.Log,
		Budget:      o.Cfg.WorkerBudget(),
		RepoRoot:    o.Cfg.RepoRoot,
		WorktreeDir: o.Cfg.WorktreeDir,
		ReminderMsg: o.Cfg.ReminderMsg,
		WrapUpMsg:   o.Cfg.WrapUpMsg,
		Git:         watchdog.OSGit{},
		Now:         o.now,
		Poll:        o.Cfg.PollInterval,
	}

	type res struct{ err error }
	done := make(chan res, 2) // buffered 2: both goroutines can send without blocking
	go func() { done <- res{o.waitDone(ctx, d, name)} }()
	go func() { done <- res{wd.Run(ctx, name, d.BeadID)} }()

	first := <-done // first terminal result wins
	cancel()        // stop the loser
	<-done          // drain the loser (it returns ctx.Err(), no terminal action)
	return first.err
}

// waitDone polls the bead status until DoneSignal fires (success) or MAX_WAIT
// elapses / the session dies (failure). On detecting death it re-reads the bead
// status once more before failing (a bead that closed in the same instant the
// session ended is a success). On failure it applies the role's OnFailure.
// On ctx cancellation it returns ctx.Err() WITHOUT calling o.fail (the watchdog
// owns the terminal outcome in that case — single-terminal guarantee).
func (o *Orchestrator) waitDone(ctx context.Context, d discover.Dispatch, name string) error {
	deadline := o.clock().Add(o.Cfg.MaxWait)
	seenClaimed := false
	for {
		// transient bd hiccup => "" => not-done, keep polling (matches bash bead_status 2>/dev/null)
		status, _ := beads.Status(ctx, o.BD, d.BeadID)
		if complete.DoneSignal(d.Role.Kind, status, seenClaimed) {
			return nil
		}
		if d.Role.Kind == roles.Worker && status == "in_progress" {
			seenClaimed = true
		}
		if !o.live(ctx, name) {
			// re-check-after-death: the bead may have closed as the session ended.
			status, _ = beads.Status(ctx, o.BD, d.BeadID)
			if complete.DoneSignal(d.Role.Kind, status, seenClaimed) {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return o.fail(ctx, d, "session exited before completing")
		}
		if !o.clock().Before(deadline) {
			// final status check after the deadline.
			status, _ = beads.Status(ctx, o.BD, d.BeadID)
			if complete.DoneSignal(d.Role.Kind, status, seenClaimed) {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return o.fail(ctx, d, fmt.Sprintf("not complete within %s", o.Cfg.MaxWait))
		}
		// cancellable wait — on cancellation return ctx.Err() and DO NOT fail
		// (the watchdog won the race and owns the terminal outcome).
		if err := o.waitPoll(ctx, o.Cfg.PollInterval); err != nil {
			return err
		}
	}
}

func (o *Orchestrator) fail(ctx context.Context, d discover.Dispatch, reason string) error {
	_ = complete.OnFailure(ctx, o.BD, d.Role, d.BeadID)
	return fmt.Errorf("%s: %s", d.BeadID, reason)
}

// live reports whether the named session is still alive per ccpool. A session
// that is not Live, or whose store state is "failed", counts as dead. ccpool
// store states done/needs_input (a finished/paused TURN) are normal multi-turn
// operation, NOT death.
func (o *Orchestrator) live(ctx context.Context, name string) bool {
	sessions, err := o.CC.List(ctx)
	if err != nil {
		return true // can't tell ⇒ assume alive; the deadline still bounds us
	}
	for _, s := range sessions {
		if s.Name == name {
			return s.Live && s.State != ccpool.StateFailed
		}
	}
	return false // absent ⇒ gone
}

// teardownAll closes every session whose name carries pr-pool's prefix — this
// pass's sessions AND strays left by a crashed prior run (the only self-healing
// behavior). Sessions outside the prefix are left untouched.
func (o *Orchestrator) teardownAll(ctx context.Context) {
	sessions, err := o.CC.List(ctx)
	if err != nil {
		slog.Warn("teardown list failed", "err", err)
		return
	}
	for _, s := range sessions {
		if strings.HasPrefix(s.Name, o.Cfg.SessionPrefix) {
			if err := o.CC.Close(ctx, s.Name); err != nil {
				slog.Warn("teardown close failed", "session", s.Name, "err", err)
			}
		}
	}
}

func (o *Orchestrator) gated() bool {
	if o.Cfg.QuotaPaused != "" && fileExists(o.Cfg.QuotaPaused) {
		return true
	}
	if o.Cfg.CICDDown != "" && fileExists(o.Cfg.CICDDown) {
		return true
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

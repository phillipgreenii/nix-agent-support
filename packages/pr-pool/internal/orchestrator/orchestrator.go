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
	"sync/atomic"
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

// RunOne dispatches a single DispatchContext through the full workOne path and then
// closes that one session (the drain's pass-level teardownAll is not involved). It is
// the single-bead entry behind `pr-pool run-role`: smoke-test one role against one
// bead without running discovery. Unlike DrainOnce it does NOT consult the quota/CICD
// gates and does NOT reap stray pr-pool-* sessions — it is a manual, intentional
// single dispatch where the operator is in control.
func (o *Orchestrator) RunOne(ctx context.Context, d discover.DispatchContext) error {
	name := d.Role.SessionName(o.Cfg.SessionPrefix, d.BeadID)
	defer func() {
		if err := o.CC.Close(ctx, name); err != nil {
			slog.Warn("run-one teardown close failed", "session", name, "err", err)
		}
	}()
	return o.workOne(ctx, d)
}

func (o *Orchestrator) drain(ctx context.Context, role roles.Role, all []discover.DispatchContext) {
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
func (o *Orchestrator) workOne(ctx context.Context, d discover.DispatchContext) error {
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
		return o.waitDone(ctx, nil, d, name) // feedback: no watchdog, so no race; always own the outcome
	}
	return o.workerWaitWithWatchdog(ctx, d, name)
}

// workerWaitWithWatchdog runs waitDone and the budget watchdog concurrently.
// First to return a terminal result wins and cancels the other. The cancelled
// loser returns ctx.Err() (and skips its terminal action by design), so only
// the winner's outcome takes effect.
//
// The single-terminal guarantee is enforced by an atomic owner claim, NOT by
// cancel() timing: cancel() only fires after the winner returns, which is too
// late to stop a loser already mid terminal bead mutation. So whichever of
// {waitDone, watchdog} reaches its terminal bead mutation first claims ownership;
// the loser then performs NO bead mutation. Without this the watchdog's unclaim
// and waitDone's add-human could both fire (bead ends open AND human), or the
// watchdog's unclaim could be misread by waitDone as a successful hand-back
// (a budget hard-stop reported as success). (pg2-c1vp)
func (o *Orchestrator) workerWaitWithWatchdog(ctx context.Context, d discover.DispatchContext, name string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var owner atomic.Bool
	claimTerminal := func() bool { return owner.CompareAndSwap(false, true) }

	wd := &watchdog.Watchdog{
		Reader:        o.reader(),
		CC:            o.CC,
		BD:            o.BD,
		Log:           o.Log,
		Budget:        o.Cfg.WorkerBudget(),
		RepoRoot:      o.Cfg.RepoRoot,
		WorktreeDir:   o.Cfg.WorktreeDir,
		ReminderMsg:   o.Cfg.ReminderMsg,
		WrapUpMsg:     o.Cfg.WrapUpMsg,
		Git:           watchdog.OSGit{},
		Now:           o.now,
		Poll:          o.Cfg.PollInterval,
		ClaimTerminal: claimTerminal,
	}

	type res struct{ err error }
	done := make(chan res, 2) // buffered 2: both goroutines can send without blocking
	go func() { done <- res{o.waitDone(ctx, claimTerminal, d, name)} }()
	go func() { done <- res{wd.Run(ctx, name, d.BeadID)} }()

	first := <-done // the winner's terminal result (the loser blocks until cancel)
	cancel()        // release the loser
	<-done          // drain the loser (it returns ctx.Err(), no terminal action)
	return first.err
}

// waitDone polls the bead status until DoneSignal fires (success) or MAX_WAIT
// elapses / the session dies (failure). On detecting death it re-reads the bead
// status once more before failing (a bead that closed in the same instant the
// session ended is a success). On failure it applies the role's OnFailure.
//
// claimTerminal arbitrates the single-terminal race with the budget watchdog:
// EVERY terminal outcome (success or failure) is gated through it, so exactly
// one of {waitDone, watchdog} owns the bead's final state. The loser performs no
// bead mutation and waits for the orchestrator to cancel ctx. A nil claimTerminal
// means no watchdog is racing (feedback dispatches / direct tests) — always own.
// (pg2-c1vp)
func (o *Orchestrator) waitDone(ctx context.Context, claimTerminal func() bool, d discover.DispatchContext, name string) error {
	deadline := o.clock().Add(o.Cfg.MaxWait)
	seenClaimed := false
	// won reports whether this loop owns the single terminal outcome.
	won := func() bool { return claimTerminal == nil || claimTerminal() }
	// lose is the loser's exit: take NO bead action, wait for the orchestrator to
	// cancel the shared ctx, then return ctx.Err() (so the winner's result, not
	// this one, is reported by workerWaitWithWatchdog).
	lose := func() error { <-ctx.Done(); return ctx.Err() }
	for {
		// If ctx is already cancelled the watchdog won (or we're shutting down):
		// do not trust a fresh status read as completion (the watchdog's unclaim
		// would look like an "open" hand-back), and run no failure action.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// transient bd hiccup => "" => not-done, keep polling (matches bash bead_status 2>/dev/null)
		status, _ := beads.Status(ctx, o.BD, d.BeadID)
		if complete.DoneSignal(d.Role.Kind, status, seenClaimed) {
			if won() {
				return nil
			}
			return lose()
		}
		if d.Role.Kind == roles.Worker && status == "in_progress" {
			seenClaimed = true
		}
		if !o.active(ctx, name) {
			// re-check-after-death: the bead may have closed as the session ended.
			status, _ = beads.Status(ctx, o.BD, d.BeadID)
			if complete.DoneSignal(d.Role.Kind, status, seenClaimed) {
				if won() {
					return nil
				}
				return lose()
			}
			if won() {
				return o.fail(ctx, d, "session exited before completing")
			}
			return lose()
		}
		if !o.clock().Before(deadline) {
			// final status check after the deadline.
			status, _ = beads.Status(ctx, o.BD, d.BeadID)
			if complete.DoneSignal(d.Role.Kind, status, seenClaimed) {
				if won() {
					return nil
				}
				return lose()
			}
			if won() {
				return o.fail(ctx, d, fmt.Sprintf("not complete within %s", o.Cfg.MaxWait))
			}
			return lose()
		}
		// cancellable wait — on cancellation return ctx.Err() and DO NOT fail
		// (the watchdog won the race and owns the terminal outcome).
		if err := o.waitPoll(ctx, o.Cfg.PollInterval); err != nil {
			return err
		}
	}
}

func (o *Orchestrator) fail(ctx context.Context, d discover.DispatchContext, reason string) error {
	_ = complete.OnFailure(ctx, o.BD, d.Role, d.BeadID)
	return fmt.Errorf("%s: %s", d.BeadID, reason)
}

// active reports whether it is still worth waiting on the named session. A session
// is active while it can still make progress: starting/ready/working, and
// needs_input (paused awaiting a human who may attach and move it along — still
// bounded by MaxWait). It is NOT active once it reaches done (the agent finished its
// turn and nothing re-nudges it) or failed, or once it is absent from ccpool list.
// A list error is treated as active (can't tell ⇒ keep waiting; MaxWait bounds us).
func (o *Orchestrator) active(ctx context.Context, name string) bool {
	sessions, err := o.CC.List(ctx)
	if err != nil {
		return true // can't tell ⇒ assume active; the deadline still bounds us
	}
	for _, s := range sessions {
		if s.Name == name {
			return s.Live && s.State != ccpool.StateFailed && s.State != ccpool.StateDone
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

package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	ct "github.com/phillipgreenii/claude-transcript"
	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/budget"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/complete"
	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/prompt"
	"github.com/phillipgreenii/pr-pool/internal/report"
	"github.com/phillipgreenii/pr-pool/internal/roles"
	"github.com/phillipgreenii/pr-pool/internal/watchdog"
	"github.com/phillipgreenii/pr-pool/internal/worktree"
)

type ccpoolExecutor struct{}

func (ccpoolExecutor) Dispatch(ctx context.Context, d discover.DispatchContext, deps Deps) (report.Result, error) {
	return (&ccpoolRun{deps: deps}).run(ctx, d)
}

// ccpoolRun carries Deps so the moved methods keep their original signatures
// (only o.X → r.deps.X). It exists per-Dispatch; no cross-dispatch state.
type ccpoolRun struct{ deps Deps }

// run is the ccpool dispatch: Ensure a fresh per-attempt session, Send the
// rendered nudge (async), then wait for completion — racing the budget watchdog when
// the role carries a finite budget. The session is addressed by ExternalID; the
// stable DisplayName is passed only as ccpool --name.
func (r *ccpoolRun) run(ctx context.Context, d discover.DispatchContext) (report.Result, error) {
	cc := d.Role.CCPool
	display := d.Role.DisplayName(r.deps.Cfg.SessionPrefix, d.Item.ID)

	// Fresh per-bead worktree so the worker never runs on a stale unrelated branch
	// (pg2-yukh root cause #2). On failure to create one, treat it like a launch
	// failure (escalate per ADR 0015) — running in the shared monorepo is exactly
	// the bug we are fixing, so we do NOT silently fall back to RepoRoot.
	wt, wtErr := worktree.Ensure(ctx, r.deps.git(), r.deps.Cfg.WorktreeDir, r.deps.Cfg.RepoRoot, d.Item.ID)
	if wtErr != nil {
		var res report.Result
		if r.escalateLaunchFailure(ctx, d.Item.ID) {
			res = failureAction(report.Escalated, d.Item.ID)
		}
		return res, fmt.Errorf("worktree %s: %w", d.Item.ID, wtErr)
	}

	env := map[string]string{
		"BEADS_ACTOR": cc.Actor,
		// BEADS_DIR stays repo-rooted: worktrees share .git but the beads dolt store
		// is repo-rooted, so the worker must read/write the SAME bead store, just on
		// its own working tree (pg2-yukh).
		"BEADS_DIR":      r.deps.Cfg.RepoRoot + "/.beads",
		"WORKSPACE_ROOT": wt,
	}
	if err := r.deps.CC.Ensure(ctx, r.deps.ExternalID, display, wt, env); err != nil {
		// Could not even create the session. The bead was never dispatched, so we
		// do not flag/unclaim it on a transient hiccup. But a bead that fails to
		// launch repeatedly is escalated (ADR 0015): stamp pool-launch-fail on the
		// first failure; on a subsequent failure (label already present) add human
		// so discovery stops retrying it (worker discovery excludes human).
		var res report.Result
		if r.escalateLaunchFailure(ctx, d.Item.ID) {
			res = failureAction(report.Escalated, d.Item.ID)
		}
		return res, fmt.Errorf("ensure %s: %w", r.deps.ExternalID, err)
	}
	// The bead was dispatched: clear any pool-launch-fail from a prior attempt so
	// the escalation counts CONSECUTIVE launch failures, not lifetime ones (ADR
	// 0015). Best-effort.
	_ = beads.RemoveLabel(ctx, r.deps.BD, d.Item.ID, "pool-launch-fail")
	nudge := r.renderNudge(cc, d, wt)
	if err := r.deps.CC.Send(ctx, r.deps.ExternalID, nudge, ccpool.ModeNoWait); err != nil {
		var res report.Result
		// A confirmed dropped nudge (exit 7): the model never ingested the task, so
		// hand the bead back unclaimed regardless of on_dispatch_fail — leaving it
		// claimed would let the budget watchdog later nudge a context-less model
		// (the pg2-yukh incident). The session never did anything, so no other bead
		// can have been touched.
		if ccpool.IsNotIngested(err) {
			_ = beads.Unclaim(ctx, r.deps.BD, d.Item.ID)
			res = failureAction(report.Unclaimed, d.Item.ID)
			return res, fmt.Errorf("send %s: prompt not ingested: %w", r.deps.ExternalID, err)
		}
		// J-dispatch-fail: apply the role's configured on_dispatch_fail action.
		if cc.OnDispatchFail == roles.DispatchUnclaim {
			_ = beads.Unclaim(ctx, r.deps.BD, d.Item.ID)
			res = failureAction(report.Unclaimed, d.Item.ID)
		}
		return res, fmt.Errorf("send %s: %w", r.deps.ExternalID, err)
	}
	// A finite budget => run the watchdog (it races waitDone); unlimited => no
	// watchdog, so no race and waitDone always owns the outcome.
	var werr error
	if budgetUnlimited(cc.Budget) {
		werr = r.waitDone(ctx, nil, d, r.deps.ExternalID)
	} else {
		werr = r.workerWaitWithWatchdog(ctx, d, r.deps.ExternalID, wt)
	}
	return r.waitFailureResult(cc, d.Item.ID, werr), werr
}

// waitFailureResult maps a wait-path error to the verb actually applied to the
// bead: a budget hard-stop (watchdog won) always unclaimed; any other failure
// went through fail → complete.OnFailure(OnFailure). nil/ctx errors → no verb.
// (pg2-kj7j)
func (r *ccpoolRun) waitFailureResult(cc *roles.CCPoolConfig, beadID string, err error) report.Result {
	if err == nil {
		return report.Result{}
	}
	if errors.Is(err, watchdog.ErrBudgetExceeded) {
		return failureAction(report.Unclaimed, beadID)
	}
	switch cc.OnFailure {
	case roles.Unclaim:
		return failureAction(report.Unclaimed, beadID)
	case roles.AddHuman:
		return failureAction(report.Escalated, beadID)
	}
	return report.Result{}
}

// budgetUnlimited reports whether a budget imposes no finite bound (so no watchdog
// is needed and no budget prompt-line is appended).
func budgetUnlimited(b budget.Budget) bool {
	return b.Tokens.Unlimited() && b.Cost.Unlimited() && b.Time <= 0
}

// renderNudge builds the prompt sent to a ccpool session: the (non-editable) safety
// preamble when authorship_guard is set, then the role's rendered task prompt, then
// the budget prompt-line (empty when the budget is unlimited).
func (r *ccpoolRun) renderNudge(cc *roles.CCPoolConfig, d discover.DispatchContext, worktreeDir string) string {
	pctx := prompt.Context{
		Item:        d.Item,
		WorktreeDir: worktreeDir,
		SkillMD:     cc.SkillMD,
		SelfLogin:   r.deps.Cfg.SelfLogin,
		RepoRoot:    r.deps.Cfg.RepoRoot,
	}
	body, err := prompt.Render(cc.Prompt, pctx)
	if err != nil {
		// A prompt that references an unknown var fails here; fall back to the raw
		// template source so the dispatch still carries the task (and log it).
		slog.Warn("prompt render failed; sending raw body", "role", d.Role.Name, "err", err)
		body = cc.PromptBody
	}
	var sb strings.Builder
	if cc.AuthorshipGuard {
		sb.WriteString(prompt.AuthorshipPreamble())
	}
	sb.WriteString(body)
	sb.WriteString(cc.Budget.PromptLine()) // "" when unlimited
	return sb.String()
}

// escalateLaunchFailure escalates a bead that ccpool could not launch. First
// failure: add pool-launch-fail. Repeat failure (label already present): add
// human and stop retrying (worker discovery excludes the human label). Reads are
// best-effort — a bd hiccup here just means we retry next pass rather than
// escalate, which is the safe direction. (ADR 0015)
//
// Returns true iff it escalated to human (repeat failure); false on the first
// failure (label only) or on a bd read hiccup. (pg2-kj7j)
func (r *ccpoolRun) escalateLaunchFailure(ctx context.Context, beadID string) bool {
	already, err := beads.HasLabel(ctx, r.deps.BD, beadID, "pool-launch-fail")
	if err != nil {
		return false // can't tell ⇒ do nothing this pass; the next launch failure retries
	}
	if already {
		_ = beads.AddHuman(ctx, r.deps.BD, beadID)
		return true
	}
	_ = beads.AddLabel(ctx, r.deps.BD, beadID, "pool-launch-fail")
	return false
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
func (r *ccpoolRun) workerWaitWithWatchdog(ctx context.Context, d discover.DispatchContext, name, worktreeDir string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var owner atomic.Bool
	claimTerminal := func() bool { return owner.CompareAndSwap(false, true) }

	wd := &watchdog.Watchdog{
		Reader:      r.deps.reader(),
		CC:          r.deps.CC,
		BD:          r.deps.BD,
		Log:         r.deps.Log,
		Budget:      d.Role.CCPool.Budget,
		RepoRoot:    r.deps.Cfg.RepoRoot,
		WorktreeDir: worktreeDir, // the per-bead worktree the worker ran in (pg2-yukh)
		ReminderMsg: r.deps.Cfg.ReminderMsg,
		WrapUpMsg:   r.deps.Cfg.WrapUpMsg,
		Git:         r.deps.git(),
		// FirstTurnStarted gates the budget NUDGES on a real model turn so a worker
		// that never ingested its task is never prompted (pg2-yukh #3b). The hard
		// STOP is NOT gated — it unclaims, it does not nudge.
		FirstTurnStarted: func(path string) bool {
			if path == "" {
				return false
			}
			_, ok := ct.LastMessageActivity(path)
			return ok
		},
		Now:           r.deps.Now,
		Poll:          r.deps.Cfg.PollInterval,
		ClaimTerminal: claimTerminal,
	}

	type res struct{ err error }
	done := make(chan res, 2) // buffered 2: both goroutines can send without blocking
	go func() { done <- res{r.waitDone(ctx, claimTerminal, d, name)} }()
	go func() { done <- res{wd.Run(ctx, name, d.Item.ID)} }()

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
func (r *ccpoolRun) waitDone(ctx context.Context, claimTerminal func() bool, d discover.DispatchContext, name string) error {
	completion := d.Role.CCPool.Completion
	deadline := r.deps.clock().Add(r.deps.Cfg.MaxWait)
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
		status, _ := beads.Status(ctx, r.deps.BD, d.Item.ID)
		if complete.DoneSignal(completion, status, seenClaimed) {
			if won() {
				return nil
			}
			return lose()
		}
		if completion == roles.CloseOrHandback && status == "in_progress" {
			seenClaimed = true
		}
		if !r.active(ctx, name) {
			// re-check-after-death: the bead may have closed as the session ended.
			status, _ = beads.Status(ctx, r.deps.BD, d.Item.ID)
			if complete.DoneSignal(completion, status, seenClaimed) {
				if won() {
					return nil
				}
				return lose()
			}
			if won() {
				return r.fail(ctx, d, "session exited before completing")
			}
			return lose()
		}
		if !r.deps.clock().Before(deadline) {
			// final status check after the deadline.
			status, _ = beads.Status(ctx, r.deps.BD, d.Item.ID)
			if complete.DoneSignal(completion, status, seenClaimed) {
				if won() {
					return nil
				}
				return lose()
			}
			if won() {
				return r.fail(ctx, d, fmt.Sprintf("not complete within %s", r.deps.Cfg.MaxWait))
			}
			return lose()
		}
		// cancellable wait — on cancellation return ctx.Err() and DO NOT fail
		// (the watchdog won the race and owns the terminal outcome).
		if err := r.deps.waitPoll(ctx, r.deps.Cfg.PollInterval); err != nil {
			return err
		}
	}
}

func (r *ccpoolRun) fail(ctx context.Context, d discover.DispatchContext, reason string) error {
	_ = complete.OnFailure(ctx, r.deps.BD, d.Role.CCPool.OnFailure, d.Item.ID)
	return fmt.Errorf("%s: %s", d.Item.ID, reason)
}

// active reports whether it is still worth waiting on the session addressed by
// externalID. A session is active while it can still make progress:
// starting/ready/working, and needs_input (paused awaiting a human who may attach
// and move it along — still bounded by MaxWait). It is NOT active once the ccpool
// session reaches idle (Claude Stop: the turn ended and nothing re-nudges it) or
// errored (Claude StopFailure), or once it is absent from ccpool list. These are
// session FACTS, not work judgments — on !active the caller re-reads the BEAD to
// decide success vs failure (ADR 0015). A list error is treated as active (can't
// tell ⇒ keep waiting; MaxWait bounds us).
func (r *ccpoolRun) active(ctx context.Context, externalID string) bool {
	sessions, err := r.deps.CC.List(ctx)
	if err != nil {
		return true // can't tell ⇒ assume active; the deadline still bounds us
	}
	for _, s := range sessions {
		if s.ExternalID == externalID {
			return s.Live && s.State != ccpool.StateErrored && s.State != ccpool.StateIdle
		}
	}
	return false // absent ⇒ gone
}

// sessionState returns the current ccpool state of the session addressed by
// externalID and whether it was present in the list. Unlike active() (which
// collapses state to a keep-waiting bool), this preserves the raw state so the
// caller can detect the EDGE into needs_input. A list error returns ("", false)
// — can't tell ⇒ no edge fires this poll (the next poll retries).
func (r *ccpoolRun) sessionState(ctx context.Context, externalID string) (ccpool.SessionState, bool) {
	sessions, err := r.deps.CC.List(ctx)
	if err != nil {
		return "", false
	}
	for _, s := range sessions {
		if s.ExternalID == externalID {
			return s.State, true
		}
	}
	return "", false
}

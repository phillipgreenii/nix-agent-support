package executor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ct "github.com/phillipgreenii/claude-transcript"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/budget"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/complete"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/failsig"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/prompt"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/report"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/watchdog"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/worktree"
	"github.com/phillipgreenii/x/gitclient"
)

// purgeCloseTimeout bounds the abandoned-session purge-close (both attempts).
const purgeCloseTimeout = 2 * time.Minute

type ccpoolExecutor struct{}

func (ccpoolExecutor) Dispatch(ctx context.Context, d DispatchContext, deps Deps) (report.Result, error) {
	return (&ccpoolRun{deps: deps}).run(ctx, d)
}

// ccpoolRun carries Deps so the moved methods keep their original signatures
// (only o.X → r.deps.X). It exists per-Dispatch; no cross-dispatch state.
type ccpoolRun struct {
	deps Deps

	// sigMu guards the failure-signature state below (INV-CCH-9).
	sigMu      sync.Mutex
	transcript string            // last-seen TranscriptPath of the dispatched session
	exit       failsig.ExitFacts // last-observed ccpool session facts (state, liveness, close reason)
	captured   *failsig.Result   // signature captured before teardown; first capture wins
}

// ErrPoolAtCapacity: the pool reported free == 0 — a healthy, expected
// backpressure signal (the pool is simply full). No bead was mutated and no
// isolation prepared; the cmd layer maps this to the transport's pre-accept
// busy decline so the core re-offers the event with backoff (INV-CCH-6, ADR
// 0072's Decision item 3).
//
// Scoped to the capacity.Free == 0 case ONLY (bead pg2-j4uwg): before this
// bead it ALSO covered the capErr != nil case below, so a genuinely
// worth-investigating "the capacity query itself is failing" was
// indistinguishable from this ordinary, expected case — confirmed live
// 2026-09-22, declined counts climbing into the hundreds while ccpool
// capacity showed a healthy, simply-full pool. See ErrPoolCapacityUnknown
// for that now-separate sentinel.
var ErrPoolAtCapacity = errors.New("ccpool: no free slot")

// ErrPoolCapacityUnknown: the pool's capacity could not be queried at all
// (capErr != nil) — split off ErrPoolAtCapacity by bead pg2-j4uwg because
// this case is genuinely worth investigating, unlike ErrPoolAtCapacity's
// healthy/expected backpressure. No bead was mutated and no isolation
// prepared here either, and the cmd layer maps THIS sentinel to the SAME
// transport pre-accept busy decline (INV-CCH-6, ADR 0072's Decision item
// 3) — the wire-level exit code and the core's retry/backoff cadence are
// IDENTICAL for either sentinel (INV-FAIL-1: every DeclineReason re-offers
// alike); only cmd/pg-router-ccpool-handler/dispatch.go's OPTIONAL reply
// body, and from there pg-router core's declined metric/status breakdown,
// tell the two apart. Unknown capacity still fails CLOSED here: launching
// blind is the failure mode the gate exists to stop, and a re-offer costs
// nothing.
var ErrPoolCapacityUnknown = errors.New("ccpool: pool capacity unknown")

// ErrLowDisk re-exports internal/worktree.ErrLowDisk under this package's own
// Err* naming (bead pg2-8vn8t), mirroring ErrPoolAtCapacity/
// ErrPoolCapacityUnknown just above: a worktree-isolation failure caused by
// the filesystem running out of room is a transient, SYSTEM-WIDE condition,
// never a bead-specific defect — the 2026-09-23 incident's repeated
// 'git worktree add ... exit 128' failures escalated several UNRELATED beads
// to `human` (one per dispatch that happened to land while the volume was
// full), which is exactly the noise this sentinel exists to stop. It is the
// SAME error value as worktree.ErrLowDisk (not a distinct wrapping sentinel),
// so errors.Is(err, ErrLowDisk) and errors.Is(err, worktree.ErrLowDisk) match
// identically; this alias exists purely so callers outside this file (e.g.
// cmd/pg-router-ccpool-handler/dispatch.go's busyDeclineReason, which already
// references this package's other two sentinels this same way) never need
// their own import of internal/worktree just to check it.
var ErrLowDisk = worktree.ErrLowDisk

// run is the ccpool dispatch: Ensure a fresh per-attempt session, Send the
// rendered nudge (async), then wait for completion — racing the budget watchdog when
// the role carries a finite budget. The session is addressed by ExternalID; the
// stable DisplayName is passed only as ccpool --name.
func (r *ccpoolRun) run(ctx context.Context, d DispatchContext) (report.Result, error) {
	cc := d.Role.CCPool
	display := d.Role.DisplayName(r.deps.Cfg.SessionPrefix, d.Item.ID)

	// This realizes INV-CCH-2's own duplicate-tolerance obligation (docs/
	// behavior/invariants.md — a handler session MUST tolerate a duplicate
	// event, matching INTF-HANDLER's obligations on every implementer).
	// INV-EVT-2: a crash-window redelivery of the same dispatched item mints a
	// FRESH per-attempt ExternalID (Role.ExternalID's own stamp), so ExternalID
	// cannot be the correlation key that catches the duplicate — but the ccpool
	// --name label (display, above) is stable per bead across attempts. Before
	// launching a fresh session, check whether one already exists under that
	// stable name; if so this dispatch is an already-in-flight duplicate, and
	// absorbing it (rather than starting a second session for the same bead)
	// closes register row INV-EVT-2 for real (ADR 0065's "Register" section).
	if existing, ok := r.findSessionByName(ctx, display, d.EventID, dispatchHeadSHA(d.Item)); ok {
		// Take the per-session lock and refresh the supervision lease BEFORE
		// waiting, so a concurrent orphan reconcile either already finished (the
		// row is then closed and not absorbable: fall through to a fresh launch)
		// or sees the fresh lease and stands down (INV-CCH-18).
		existing, ok, err := r.takeOverForAbsorb(ctx, existing, d.EventID, dispatchHeadSHA(d.Item))
		if err != nil {
			return report.Result{}, fmt.Errorf("absorb %s: %w", display, err)
		}
		if ok {
			return r.absorbDuplicate(ctx, d, existing)
		}
	}

	// Admission gate (INV-CCH-6, ADR 0072's Decision item 3): consult the
	// pool's capacity before preparing isolation or launching anything. No
	// bead is mutated and no worktree is prepared on this path. Unknown
	// capacity fails CLOSED — treated as full, never as "launch anyway" —
	// but as its OWN sentinel (ErrPoolCapacityUnknown, bead pg2-j4uwg), kept
	// distinct from the capacity.Free == 0 branch just below.
	capacity, capErr := r.deps.CC.Capacity(ctx)
	if capErr != nil {
		slog.Warn("dispatch declined: pool capacity unknown", "role", d.Role.Name, "bead", d.Item.ID, "err", capErr)
		return report.Result{}, fmt.Errorf("%w: %v", ErrPoolCapacityUnknown, capErr)
	}
	if capacity.Free == 0 {
		if ul := capacity.UsageLimit; ul != nil {
			// The account's usage window is at its limit, so ccpool reports no free
			// slot whatever the occupancy. This is the SAME healthy backpressure as a
			// full pool (and keeps the same at-capacity tag, so the failure-rate alert
			// that excludes it stays quiet for the hours a window takes to reset): the
			// core re-offers the event with backoff until the window clears or the
			// event's TTL expires. No bead is touched.
			slog.Info("dispatch declined: usage limit hit", "role", d.Role.Name, "bead", d.Item.ID,
				"window", ul.Window, "used_pct", ul.UsedPct, "resets_at", ul.ResetsAt)
			return report.Result{}, fmt.Errorf("%w: usage limit %s hit until %s",
				ErrPoolAtCapacity, ul.Window, ul.ResetsAt.Format(time.RFC3339))
		}
		slog.Info("dispatch declined: pool at capacity", "role", d.Role.Name, "bead", d.Item.ID,
			"counted", capacity.Counted, "max", capacity.MaxSessions, "preserved", capacity.Preserved)
		return report.Result{}, fmt.Errorf("%w: counted=%d max=%d preserved=%d",
			ErrPoolAtCapacity, capacity.Counted, capacity.MaxSessions, capacity.Preserved)
	}

	// Prepare WORKSPACE_ROOT per the role's isolation strategy (roles.IsolationConfig;
	// default "worktree" — a fresh per-bead worktree so the worker never runs on a
	// stale unrelated branch, pg2-yukh root cause #2). On failure, treat it like a
	// launch failure (escalate per ADR 0015) — running in the shared monorepo is
	// exactly the bug the worktree default is fixing, so we do NOT silently fall
	// back to RepoRoot for that strategy (a role using "none" gets RepoRoot on
	// purpose, by its own explicit config, not as a fallback).
	//
	// Remember whether the per-bead worktree already existed: only a worktree
	// THIS dispatch created is reclaimed if the dispatch dies before a session
	// takes ownership of it (reclaimAbandonedWorktree, pg2-w3usi).
	//
	// Hold the per-bead worktree lock (SHARED) from before the worktree exists
	// until this dispatch returns, so the worktree-keyed sweep can tell a worktree
	// a live dispatch is using from one a killed handler leaked (pg2-ganjb,
	// INV-CCH-19). A SIGKILL drops the lock with the process.
	if usesWorktreeIsolation(cc.Isolation) {
		defer r.holdWorktreeLock(ctx, d.Item.ID)()
	}
	preexisting := usesWorktreeIsolation(cc.Isolation) && r.worktreePathExists(d.Item.ID)
	wt, wtErr := newIsolation(cc.Isolation, r.deps).Ensure(ctx, d.Item.ID)
	if wtErr != nil {
		if errors.Is(wtErr, ErrLowDisk) {
			// Low disk is the ONE isolation failure this function does NOT treat
			// like an ordinary launch failure (bead pg2-8vn8t): it is a
			// transient, system-wide condition that will hit whichever bead
			// happens to dispatch next, not a defect in THIS bead — escalating
			// it per-bead (below) is exactly what flooded several unrelated
			// beads with `human` during the 2026-09-23 incident. Logged clearly
			// (distinguishable from a generic exit-128 at a glance, unlike
			// before this fix) and returned with no bead mutation at all, so the
			// caller (cmd/pg-router-ccpool-handler/dispatch.go's
			// busyDeclineReason) can map it to a pre-accept busy decline instead
			// — the core simply re-offers later with backoff, same as
			// ErrPoolAtCapacity.
			slog.Warn("dispatch declined: low disk space preparing isolation", "role", d.Role.Name, "bead", d.Item.ID, "err", wtErr)
			return report.Result{}, fmt.Errorf("isolation %s: %w", d.Item.ID, wtErr)
		}
		var res report.Result
		if r.escalateLaunchFailure(ctx, d.Item.ID) {
			res = failureAction(report.Escalated, d.Item.ID)
		}
		return res, fmt.Errorf("isolation %s: %w", d.Item.ID, wtErr)
	}

	env := map[string]string{
		"BEADS_ACTOR": cc.Actor,
		// BEADS_DIR stays repo-rooted: worktrees share .git but the beads dolt store
		// is repo-rooted, so the worker must read/write the SAME bead store, just on
		// its own working tree (pg2-yukh).
		"BEADS_DIR":      beadsDirFor(r.deps.Cfg.RepoRoot, cc) + "/.beads",
		"WORKSPACE_ROOT": wt,
	}
	if err := r.deps.CC.Ensure(ctx, r.deps.ExternalID, display, wt, env, ccpool.DispatchMeta(d.Item.ID, d.Role.Name, r.deps.clock(), r.deps.Cfg.LeaseTTL, d.EventID, dispatchHeadSHA(d.Item))); err != nil {
		// Could not even create the session. The bead was never dispatched, so we
		// do not flag/unclaim it on a transient hiccup. But a bead that fails to
		// launch repeatedly is escalated (ADR 0015): stamp pool-launch-fail on the
		// first failure; on a subsequent failure (label already present) add human
		// so discovery stops retrying it (worker discovery excludes human).
		//
		// Best-effort purge-close the abandoned session (pg2-u2v2p): `ccpool new`
		// may have already inserted a store row (state=starting) before it failed
		// or timed out waiting for ready ("did not reach ready before timeout
		// (state=starting)") — ccpool never auto-reaps a session nobody explicitly
		// closed. Left alone, that row stays state=starting, live=true forever,
		// leaking pool capacity (the zombie-session incident this bead fixes).
		// Purge (not a soft close) since a session that never reached ready has
		// no transcript and nothing resumable worth preserving. If ensure never
		// got far enough to create a row at all, ccpool close on an unknown
		// external_id just errors, which this ignores exactly like every other
		// best-effort Close call in this file.
		r.purgeAbandonedSession(ctx, d.Item.ID)
		// The worktree created just above has no session to own it now, so no
		// reconcile keyed on a session row can ever find it (pg2-w3usi).
		r.reclaimAbandonedWorktree(ctx, cc, d.Item.ID, wt, !preexisting)
		if ccpool.IsUsageLimited(err) {
			// ccpool declined the launch because the account's usage window filled
			// between the capacity pre-check and `new` (exit 8). That is "not right
			// now", never a defect of THIS bead: no pool-launch-fail stamp, no
			// escalation to human. The abandoned session and worktree are already
			// reclaimed above; the busy decline re-offers the event with backoff.
			slog.Info("dispatch declined: usage limit hit at launch", "role", d.Role.Name, "bead", d.Item.ID, "err", err)
			return report.Result{}, fmt.Errorf("%w: ensure %s: %v", ErrPoolAtCapacity, r.deps.ExternalID, err)
		}
		var res report.Result
		if r.escalateLaunchFailure(ctx, d.Item.ID) {
			res = failureAction(report.Escalated, d.Item.ID)
		}
		return res, fmt.Errorf("ensure %s: %w", r.deps.ExternalID, err)
	}
	// From here until Dispatch returns (Send, the wait, the watchdog, the
	// worktree cleanup's quiet wait and the settled-session close) a background
	// ticker keeps the supervision lease fresh, so a later dispatch can tell this
	// live session from an orphan (INV-CCH-18).
	defer r.startLease(ctx, r.deps.ExternalID)()
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
			// The session never took the task: close it (reason handler, via
			// the CLI runner) so a ready-but-empty row does not hold a
			// counted slot until idle_ttl — cap eviction no longer reclaims
			// ready rows (ADR 0072's Decision item 2).
			_ = r.deps.CC.Close(ctx, r.deps.ExternalID, false)
			_ = beads.Unclaim(ctx, r.deps.BD, d.Item.ID)
			// The session did nothing, so the worktree created for it is
			// reclaimed too (pg2-w3usi); reclaimAbandonedWorktree skips it if
			// the Close above did not actually stop the session.
			r.reclaimAbandonedWorktree(ctx, cc, d.Item.ID, wt, !preexisting)
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
		werr = r.workerWaitWithWatchdog(ctx, d, r.deps.ExternalID, wt, time.Time{})
	}
	return r.finishWait(ctx, cc, d, r.deps.ExternalID, wt, werr)
}

// purgeAbandonedSession purge-closes the session of a failed launch and, unlike
// the other best-effort Close calls in this file, does NOT discard the result
// (pg2-rkjbu): a failed close left the session to reach ready AFTER the launch
// was reported failed (2026-09-29 incident), so a failure is logged with its
// error and retried once. The close runs on a context detached from ctx's
// cancellation, since the launch failure is often ctx expiry ("did not reach
// ready before timeout") and a close on a dead ctx would fail for that reason
// alone. A close that still fails after the retry is logged at Error level.
func (r *ccpoolRun) purgeAbandonedSession(ctx context.Context, beadID string) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), purgeCloseTimeout)
	defer cancel()
	err := r.deps.CC.Close(cctx, r.deps.ExternalID, true)
	if err == nil {
		return
	}
	slog.Warn("purge-close of abandoned session failed; retrying once",
		"external_id", r.deps.ExternalID, "bead", beadID, "err", err)
	if err = r.deps.CC.Close(cctx, r.deps.ExternalID, true); err != nil {
		slog.Error("purge-close of abandoned session failed after retry; session may still be live and reach ready",
			"external_id", r.deps.ExternalID, "bead", beadID, "err", err)
	}
}

// worktreePathExists reports whether the per-bead worktree directory for
// beadID is already on disk (worktree.Ensure keys it as WorktreeDir/<beadID>).
// Read BEFORE Ensure to tell a worktree this dispatch creates from a reused
// one. A stat error other than "does not exist" counts as existing: it fails
// toward NOT reclaiming.
func (r *ccpoolRun) worktreePathExists(beadID string) bool {
	_, err := os.Stat(filepath.Join(r.deps.Cfg.WorktreeDir, beadID))
	return err == nil || !errors.Is(err, fs.ErrNotExist)
}

// reclaimAbandonedWorktree removes the per-bead worktree and pg-router/<bead>
// anchor branch that run() created via isolation.Ensure when the dispatch
// returns early, before any session took ownership of them (pg2-w3usi):
// CC.Ensure failed or was cancelled (ctx expiry, handler SIGTERM), or the
// nudge was confirmed dropped. Every other cleanup keys on a session row
// (finishWait; the dispatch-time and shutdown reconciles), and by construction
// no row exists for such a worktree, so without this it leaked forever.
//
// Guards: createdHere must be true (a worktree that pre-existed this dispatch
// may carry a previous attempt's commits and is left to finishWait or the
// reconciles); a session row for this dispatch that is still live, or that
// cannot be read, is never deprived of its working directory; everything
// else is cleanupWorktree's own (needs_input, live peer, quiet window,
// RemoveWorktree force=false so git refuses a dirty tree, branch deleted only
// after the worktree is gone). Runs on a context detached from ctx's
// cancellation, since the early return is often caused by ctx expiry itself
// and every git/ccpool call on a dead ctx would fail for that reason alone.
// Fails soft like cleanupWorktree.
//
// A SIGKILLed handler runs none of this; that window is covered by the
// worktree-keyed sweep (cmd/pg-router-ccpool-handler/worktreesweep.go,
// pg2-ganjb, INV-CCH-19), which keys on the worktree rather than a session row.
func (r *ccpoolRun) reclaimAbandonedWorktree(ctx context.Context, cc *roles.CCPoolConfig, beadID, wt string, createdHere bool) {
	if wt == "" || !createdHere || !usesWorktreeIsolation(cc.Isolation) {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), purgeCloseTimeout)
	defer cancel()
	name := r.deps.ExternalID
	sessions, err := r.deps.CC.List(cctx)
	if err != nil {
		slog.Warn("dispatch: abandoned worktree left for next sweep -- session list failed",
			"session", name, "bead", beadID, "worktree", wt, "err", err)
		return
	}
	for _, s := range sessions {
		if s.ExternalID == name && s.Live {
			slog.Warn("dispatch: abandoned worktree kept -- its session is still live",
				"session", name, "bead", beadID, "worktree", wt)
			return
		}
	}
	slog.Info("dispatch: reclaiming worktree of a dispatch that never reached a running session",
		"session", name, "bead", beadID, "worktree", wt)
	r.cleanupWorktree(cctx, cc, name, beadID, wt)
}

// waitFailureResult maps a wait-path error to the verb actually applied to the
// bead: a budget hard-stop (watchdog won) always unclaimed; an external close
// (ErrExternallyClosed) unclaimed, or escalated when it was the second
// consecutive strike; any other failure went through fail →
// complete.OnFailure(OnFailure). nil/ctx errors → no verb. (pg2-kj7j, INV-CCH-7)
func (r *ccpoolRun) waitFailureResult(cc *roles.CCPoolConfig, beadID string, err error) report.Result {
	if err == nil {
		return report.Result{}
	}
	if errors.Is(err, watchdog.ErrBudgetExceeded) {
		return failureAction(report.Unclaimed, beadID)
	}
	if errors.Is(err, ErrExternallyClosed) {
		var ec *externallyClosed
		if errors.As(err, &ec) && ec.escalated {
			return failureAction(report.Escalated, beadID)
		}
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

// finishWait is run()/absorbDuplicate()'s shared tail: classify werr into the
// verb actually applied to the bead (waitFailureResult, unchanged), and — as
// the SAME step — run this dispatch's terminal-outcome worktree cleanup
// (pg2-4roho decision item 1's "primary fix"). werr's three possible shapes
// (nil/success, a complete.OnFailure-driven failure, or watchdog.
// ErrBudgetExceeded) are exactly the "success, failure, OR timeout" outcomes
// the decision names as "already distinguished elsewhere in this code" — one
// cleanupWorktree call here covers all three, rather than three separate call
// sites.
//
// It then closes (non-purge) the settled session this dispatch launched or
// absorbed (closeSettledSession, INV-CCH-17, ADR 0082), so a finished session
// does not hold a counted slot of a max_sessions=1 pool until idle_ttl.
func (r *ccpoolRun) finishWait(ctx context.Context, cc *roles.CCPoolConfig, d DispatchContext, name, wt string, werr error) (report.Result, error) {
	r.recordDispatchFailure(d, name, werr)
	q := r.cleanupWorktree(ctx, cc, name, d.Item.ID, wt)
	r.closeSettledSession(ctx, d.Item.ID, name, werr, q)
	if werr == nil {
		// A successful completion resets the eviction strike counter so
		// escalateEviction's two-strike count stays CONSECUTIVE, not lifetime
		// (mirrors run()'s own pool-launch-fail removal after a successful
		// Ensure, line 118). Best-effort.
		_ = beads.RemoveLabel(ctx, r.deps.BD, d.Item.ID, "pool-evicted")
		// The budget-stop record is cleared only once the bead is CLOSED; a
		// reopened bead keeps its history (pg2-6akgz).
		if cc.BudgetStopEscalateAfter > 0 {
			_ = beads.ClearBudgetStops(ctx, r.deps.BD, d.Item.ID)
		}
	}
	return r.waitFailureResult(cc, d.Item.ID, werr), werr
}

// cleanupWorktree removes name's per-dispatch worktree at wt now that the
// dispatch has reached a terminal outcome (finishWait's own doc comment).
// This is the single owner of the common-case cleanup, immediately, with no
// new timer — replacing reliance on the once-per-process-lifetime
// preShutdown sweep (cmd/pg-router-ccpool-handler/preshutdown.go's
// closeUnlessNeedsInput, pg2-a8h6c's short-term stopgap) for the case this
// dispatch call itself can observe. preshutdown.go's own sweep is left
// unchanged — it remains the belt-and-suspenders path for a crashed prior
// run's stray sessions, which this dispatch-scoped call can never see.
//
// Scoped to "worktree" isolation ONLY (roles.IsolationConfig's default,
// usesWorktreeIsolation below): "none" isolation hands back RepoRoot itself
// (never removable — same invariant watchdog/terminal.go's safeToReset
// enforces for the reset path), "path" isolation is one FIXED directory
// reused across every dispatch (removing it after one dispatch would break
// the next one that reuses it), and "workforest" isolation has its own,
// unrelated lifecycle (pn-workspace-rules) — none of those are a per-bead
// git worktree this executor itself created and may unilaterally remove.
//
// needs_input is excluded by design (pg2-4roho decision item 2, ADR 0037):
// a needs_input session's worktree lifetime is tied to the SESSION's own
// eventual close, not to this dispatch call returning — reimplementing that
// tie is explicitly out of this bead's scope, but honoring it here (rather
// than naively removing on every terminal outcome, including the "not
// complete within MaxWait while still needs_input" failure shape) is not:
// this is a no-op while the session is still alive and needs_input, or while
// its state can't be determined at all (needsInputAlive's own can't-tell
// case). The worktree is removed later, once the session actually closes, by
// closeUnlessNeedsInput's own existing RemoveWorktree call.
//
// Deletion safety guard (decision item 5): RemoveWorktree runs with
// force=false, so git itself refuses removal whenever the worktree is dirty
// (uncommitted changes) rather than force-deleting live work (gitclient's
// own documented behavior — see x/gitclient's WorktreeManager.RemoveWorktree
// doc comment). Any failure here (dirty worktree, already gone, not a linked
// worktree, a transient git error) fails soft — log and continue, leaving it
// for the next sweep (a future dispatch of the same bead reusing the same
// worktree path, or pg-disk-reclaimer's independent belt-and-suspenders
// sweep, decision item 4) rather than force-removing.
//
// The returned quietResult reports whether this call already ran
// waitSessionQuiet (and what it found), so finishWait's following
// closeSettledSession reuses the verdict instead of polling a second time.
func (r *ccpoolRun) cleanupWorktree(ctx context.Context, cc *roles.CCPoolConfig, name, beadID, wt string) quietResult {
	if wt == "" || !usesWorktreeIsolation(cc.Isolation) {
		return quietResult{}
	}
	if r.needsInputAlive(ctx, name) {
		slog.Info("dispatch: worktree cleanup deferred -- session still needs_input",
			"session", name, "worktree", wt)
		return quietResult{}
	}
	if r.worktreeSharedWithLivePeer(ctx, name, wt) {
		slog.Info("dispatch: worktree cleanup deferred -- another live session still uses it",
			"session", name, "worktree", wt)
		return quietResult{}
	}
	if !r.waitSessionQuiet(ctx, name) {
		slog.Warn("dispatch: worktree cleanup deferred -- session or its subagents still active (left for next sweep)",
			"session", name, "worktree", wt, "quietWindow", r.deps.Cfg.WorktreeQuietWindow)
		return quietResult{checked: true, quiet: false}
	}
	q := quietResult{checked: true, quiet: true}
	wm, err := r.deps.gitOpener()(ctx, wt)
	if err != nil {
		slog.Warn("dispatch: worktree cleanup: open failed (left for next sweep)",
			"session", name, "worktree", wt, "err", err)
		return q
	}
	if err := wm.RemoveWorktree(ctx, wt, false); err != nil {
		slog.Warn("dispatch: worktree cleanup: remove failed (left for next sweep)",
			"session", name, "worktree", wt, "err", err)
		return q
	}
	slog.Info("dispatch: worktree removed", "session", name, "worktree", wt)
	r.deleteBranch(ctx, name, beadID)
	return q
}

// quietResult is cleanupWorktree's report of whether it already ran
// waitSessionQuiet for this dispatch (checked) and, if so, its verdict
// (quiet). The zero value means "not checked yet".
type quietResult struct {
	checked bool
	quiet   bool
}

// closeSettledSessionTimeout bounds the settled-session close.
const closeSettledSessionTimeout = time.Minute

// closeSettledSession closes (non-purge, --reason handler) the session name
// that this dispatch launched or absorbed, once the dispatch has reached a
// terminal outcome and the session has settled (INV-CCH-17, ADR 0082). Without
// it a finished session stays live and idle, counting against the pool's
// max_sessions until ccpool's idle_ttl + reap tick, because a gated dispatcher
// never pushes the pool over cap, so cap eviction never fires (ADR 0072).
//
// It closes ONLY name -- never another row -- and only when ALL hold:
//   - neither ctx nor werr is a cancellation (a daemon shutdown is owned by
//     the INV-CCH-14 preShutdown sweep, not by this dispatch tail);
//   - the session is quiet (no write to its transcript or its Agent-tool
//     subagents' for WorktreeQuietWindow; reuses cleanupWorktree's verdict
//     when it already ran, otherwise runs waitSessionQuiet), for EVERY
//     isolation type -- closing the tmux pane would kill running subagents;
//   - a fresh List row for name is present, carries no CloseReason (nobody
//     closed it already: watchdog hard stop, waitDone's death branch,
//     idle_ttl/cap_eviction/operator), and is idle or errored (never
//     starting/ready/working, and never needs_input, ADR 0037).
//
// A close failure is best effort: WARN and continue; werr and the dispatch
// result are never changed. Every close logs INFO.
func (r *ccpoolRun) closeSettledSession(ctx context.Context, beadID, name string, werr error, q quietResult) {
	if ctx.Err() != nil || errors.Is(werr, context.Canceled) || errors.Is(werr, context.DeadlineExceeded) {
		return
	}
	if !q.checked {
		q.quiet = r.waitSessionQuiet(ctx, name)
	}
	if ctx.Err() != nil {
		return // cancelled while waiting for quiet
	}
	if !q.quiet {
		slog.Info("settled-session close deferred -- subagents active", "session", name, "bead", beadID)
		return
	}
	sessions, err := r.deps.CC.List(ctx)
	if err != nil {
		slog.Warn("settled-session close skipped: could not re-read session", "session", name, "bead", beadID, "err", err)
		return
	}
	var row ccpool.Session
	found := false
	for _, s := range sessions {
		if s.ExternalID == name {
			row, found = s, true
			break
		}
	}
	if !found || row.CloseReason != "" || (row.State != ccpool.StateIdle && row.State != ccpool.StateErrored) {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeSettledSessionTimeout)
	defer cancel()
	if err := r.deps.CC.Close(cctx, name, false); err != nil {
		slog.Warn("settled-session close failed (left to idle_ttl)", "session", name, "bead", beadID, "err", err)
		return
	}
	slog.Info("settled session closed", "session", name, "bead", beadID,
		"state", string(row.State), "outcome", settledOutcome(werr))
}

// settledOutcome labels a dispatch's terminal outcome for closeSettledSession's
// INFO log line.
func settledOutcome(werr error) string {
	switch {
	case werr == nil:
		return "success"
	case errors.Is(werr, watchdog.ErrBudgetExceeded):
		return "budget_stop"
	case errors.Is(werr, ErrExternallyClosed):
		return "externally_closed"
	default:
		return "failure"
	}
}

// deleteBranch removes this bead's own throwaway pg-router/<beadID> anchor
// branch (worktree.Ensure's own doc comment: "a dedicated branch
// pg-router/<beadID>") now that cleanupWorktree has confirmed its worktree is
// gone. x/gitclient's RemoveWorktree runs only `git worktree remove` and
// never touches the branch (bead pg2-ci75j's root cause) -- without this
// call, the branch is orphaned in RepoRoot forever, which is exactly the
// ~100+-stray-branch state pg2-ci75j found in the ZR monorepo.
//
// Deliberately reopens the gitclient at r.deps.Cfg.RepoRoot rather than
// reusing wm/wt from the caller: wt was just removed by RemoveWorktree above,
// so a client anchored there (cmd.Dir) can no longer spawn git at all, and a
// branch is a repo-level ref that must be deleted from a still-existing
// checkout anyway -- the same anchor worktree.Ensure itself uses to create
// the branch in the first place (CreateWorktree is likewise called with wm
// opened at repoRoot, not at the not-yet-created worktree path).
//
// These are pg-router's own internal per-dispatch anchor branches, reset at
// HEAD on every dispatch (CreateWorktree's ResetBranch/-B) -- never a user
// branch with independent value -- so once the worktree is confirmed removed
// the branch has fully served its purpose. Most are never merged into
// anything (they are throwaway checkout anchors, not feature branches), so
// deletion uses force (`git branch -D`) rather than plain `-d`, which would
// refuse constantly for exactly that reason (pg2-ci75j's own comments).
//
// Fails soft exactly like RemoveWorktree above: any error here (open
// failure, the branch already gone, a client that isn't a BranchManager, a
// transient git error) is logged and left for the next sweep, never
// escalated -- an orphaned branch costs disk, not correctness.
func (r *ccpoolRun) deleteBranch(ctx context.Context, name, beadID string) {
	branch := "pg-router/" + beadID
	root, err := r.deps.gitOpener()(ctx, r.deps.Cfg.RepoRoot)
	if err != nil {
		slog.Warn("dispatch: worktree cleanup: branch delete: open repo root failed (left for next sweep)",
			"session", name, "branch", branch, "err", err)
		return
	}
	bm, ok := root.(gitclient.BranchManager)
	if !ok {
		return
	}
	if err := bm.DeleteBranch(ctx, branch, true); err != nil {
		slog.Warn("dispatch: worktree cleanup: branch delete failed (left for next sweep)",
			"session", name, "branch", branch, "err", err)
		return
	}
	slog.Info("dispatch: branch deleted", "session", name, "branch", branch)
}

// needsInputAlive reports whether the session addressed by externalID is
// currently alive and in ccpool.StateNeedsInput. Mirrors active()'s own
// can't-tell/absent split (just below in this file) but resolves the
// can't-tell case in the OPPOSITE direction, since the two callers need
// opposite fail-safe biases: active() can't-tell ⇒ assume active (keep
// waiting; MaxWait still bounds it), while this guard's can't-tell ⇒ assume
// STILL needs_input (skip cleanup) — deleting a worktree the operator may be
// actively inspecting is the failure mode decision item 2 exists to prevent,
// so an ambiguous read must fail toward NOT deleting, not toward deleting. An
// ABSENT session (definitively gone, not merely unreadable) is unambiguous
// either way: nothing is left to preserve, so cleanup may proceed.
func (r *ccpoolRun) needsInputAlive(ctx context.Context, externalID string) bool {
	sessions, err := r.deps.CC.List(ctx)
	if err != nil {
		return true // can't tell ⇒ treat as still needs_input ⇒ skip cleanup
	}
	for _, s := range sessions {
		if s.ExternalID == externalID {
			return s.State == ccpool.StateNeedsInput
		}
	}
	return false // absent ⇒ gone ⇒ safe to clean up
}

// worktreeSharedWithLivePeer reports whether a session other than name that
// is live and not idle/errored still uses wt as its working directory
// (pg2-aqpqx). A per-bead worktree path is keyed by bead id alone
// (worktree.Ensure), so every role's session for one bead shares it: the
// session that finishes first must not delete the directory out from under
// a peer that is starting, ready, working or awaiting input -- the last
// session to detach removes it. Mirrors the reconcile path's
// worktreeInUseByPeer (cmd/pg-router-ccpool-handler/reconcile.go), except
// that a needs_input peer is also protected here: its own cleanup is
// deferred until it closes, so removing its cwd now would strand it. A list
// error is treated as "in use" (can't tell => don't delete).
func (r *ccpoolRun) worktreeSharedWithLivePeer(ctx context.Context, name, wt string) bool {
	sessions, err := r.deps.CC.List(ctx)
	if err != nil {
		return true
	}
	for _, s := range sessions {
		if s.ExternalID == name || s.CWD != wt || !s.Live {
			continue
		}
		if s.State != ccpool.StateIdle && s.State != ccpool.StateErrored {
			return true
		}
	}
	return false
}

// usesWorktreeIsolation reports whether cfg selects the "worktree" isolation
// strategy (the default: an empty Type, or the explicit "worktree" value —
// matching newIsolation's own switch in isolation.go) — the only strategy
// whose Ensure result is a per-dispatch git worktree this executor itself
// created, and so the only one cleanupWorktree may remove.
func usesWorktreeIsolation(cfg roles.IsolationConfig) bool {
	return cfg.Type == "" || cfg.Type == "worktree"
}

// findSessionByName looks up a ccpool session by its --name display label
// (Session.Name) — the stable per-bead identity (Role.DisplayName) INV-EVT-2's
// duplicate-absorption check correlates a redelivered dispatch against,
// unlike the per-attempt ExternalID (ADR 0065's "Register" section's
// INV-EVT-2 note). A list error is treated as "no match" (can't tell ⇒ fall
// through to launching normally), the same can't-tell posture active() and
// sessionState() already take elsewhere in this file.
//
// A NAME MATCH ALONE IS NOT ENOUGH (pg2-04bf7): a name match on a row that is
// crashOrphaned (below) must be treated as ABSENT here, or a crashed prior
// attempt's stale row collides with every subsequent dispatch for the same
// (bead, role) forever (the observed zombie-session incident) — letting the
// fresh per-attempt ExternalID flow through to Ensure/ccpool new normally.
// This is deliberately a NARROWER exclusion than active()'s own live/dead
// split just below: a row that is Live=false but reached a legitimate,
// hook-driven terminal state (idle/errored) WITHOUT ever being explicitly
// closed still correlates a genuine already-settled duplicate (INV-EVT-2's
// own "absorb rather than start a second session" guarantee) — only a row
// that either crashed mid-flight (Live=false, never reached idle/errored) or
// has ALREADY been explicitly closed by someone other than this handler's own
// settled-session close (CloseReason set otherwise — nothing left to absorb)
// counts as dead here. A settled row this handler closed itself (CloseReason
// "handler", idle/errored; INV-CCH-17) is still a duplicate to absorb.
//
// A handler-closed settled row is further bounded by the dispatching event
// (pg2-uprw5, ADR 0082) AND the item's pinned head (pg2-afre3): it is a
// duplicate only for a redelivery of the SAME event at the SAME head, never for
// a later re-dispatch of the same bead and role (see staleSettledRow).
func (r *ccpoolRun) findSessionByName(ctx context.Context, name, eventID, headSHA string) (ccpool.Session, bool) {
	sessions, err := r.deps.CC.List(ctx)
	if err != nil {
		return ccpool.Session{}, false
	}
	for _, s := range sessions {
		if s.Name == name && !crashOrphaned(s) && !staleSettledRow(s, eventID, headSHA) {
			return s, true
		}
	}
	return ccpool.Session{}, false
}

// crashOrphaned reports whether session s is a dead row that findSessionByName
// must treat as ABSENT rather than a genuine duplicate worth absorbing
// (pg2-04bf7):
//
//   - Live sessions are never crash-orphaned -- still literally running,
//     whatever their store State (active()'s own polling logic decides
//     separately whether it's still worth WAITING on).
//   - A settled row this handler itself closed (CloseReason == "handler" with
//     State idle/errored) is NOT orphaned: closeSettledSession closes every
//     session it settles (INV-CCH-17, ADR 0082), so a crash-window
//     redelivery of that same dispatch finds exactly this row and must still
//     absorb it (INV-EVT-2) rather than launch a second session for an
//     already-settled bead.
//   - Any OTHER row ccpool has ALREADY explicitly closed (CloseReason != "" --
//     idle_ttl/cap_eviction/operator, or a handler close of a non-terminal row
//     such as waitDone's own dead-row cleanup) has nothing left to absorb:
//     re-matching it would just re-run the SAME dead-row collision waitDone
//     already resolved once.
//   - Otherwise (never closed by anyone, not live): a row that reached a
//     Claude-hook-driven terminal state (Idle: Stop hook, the turn legitimately
//     ended; Errored: StopFailure hook, an API error legitimately ended the
//     turn) is a genuinely-settled prior duplicate -- Live going false on its
//     own here is NOT a crash signal. A row stuck in a NON-terminal state
//     (starting/ready/working/needs_input) while not live never reached
//     either hook: its tmux pane died mid-flight -- a genuine crash.
func crashOrphaned(s ccpool.Session) bool {
	// A row whose two-phase teardown is still pending (INV-CCH-20) is ABSENT
	// whatever its liveness or close reason: the handler decided to discard it, and
	// the purge_pending retry (not an absorbing dispatch) finishes the job.
	if s.PurgePending() {
		return true
	}
	if s.Live {
		return false
	}
	// A row the orphan reconcile closed (INV-CCH-18) was ABANDONED work, not a
	// settled duplicate: a redelivered dispatch must launch afresh rather than
	// absorb it and fail on its stale outcome.
	if s.Meta[ccpool.MetaKeyOrphanReclaimed] != "" {
		return true
	}
	settled := s.State == ccpool.StateIdle || s.State == ccpool.StateErrored
	if s.CloseReason != "" {
		return !(s.CloseReason == "handler" && settled)
	}
	return !settled
}

// staleSettledRow reports whether s is a handler-closed settled row (the case
// crashOrphaned deliberately keeps absorbable, ADR 0082) that belongs to an
// EARLIER event than the one now being dispatched, so absorbing it would be
// wrong (bead pg2-uprw5).
//
// A handler-closed idle/errored row is kept absorbable only so that a
// crash-window redelivery of the SAME accepted event (INV-EVT-2) finds it and
// does not launch a second session for already-settled work. A later,
// legitimate re-dispatch for the same bead and role -- a review bead reopened
// after a head advance -- is a NEW event, and absorbing it into the dead row
// would report the old outcome and fail with "session exited before
// completing". The two are told apart by the event id the launching dispatch
// stamped on the row (ccpool.MetaKeyEventID):
//
//   - this dispatch carries no event id: no bound can be applied, so the row is
//     not stale (the pre-pg2-uprw5 behavior);
//   - the row carries no event id (launched by an older build, or by a dispatch
//     with none): it cannot be proven a redelivery, so it IS stale;
//   - otherwise it is stale exactly when the ids differ.
//
// The event id alone is NOT enough for an item-carrying event (bead pg2-afre3):
// event.FingerprintID is deterministic per (event type, bead), so a review
// re-dispatched after a head advance carries the SAME id as the review that
// already settled, and the id check above never fires. The id comparison is
// therefore refined by the item's pinned head (ccpool.MetaKeyHeadSHA), applied
// only to a row whose event id matched:
//
//   - this dispatch carries no head_sha (a worker/feedback role, or a bead
//     without one): no head bound applies;
//   - the row carries no head (launched by an older build): it cannot be proven
//     the same head, so it IS stale;
//   - otherwise it is stale exactly when the heads differ.
//
// Absorbing such a row would adopt the OLD head's outcome and, worse, measure the
// time budget from its original launch, tripping an instant false
// "session budget exceeded" without any model work.
//
// Only a handler-closed settled row is ever stale here. Rows still open, or
// closed by anyone else, are judged by crashOrphaned alone.
func staleSettledRow(s ccpool.Session, eventID, headSHA string) bool {
	if eventID == "" || s.CloseReason != "handler" {
		return false
	}
	if s.State != ccpool.StateIdle && s.State != ccpool.StateErrored {
		return false
	}
	if s.Meta[ccpool.MetaKeyEventID] != eventID {
		return true
	}
	return headSHA != "" && s.Meta[ccpool.MetaKeyHeadSHA] != headSHA
}

// dispatchHeadSHA returns the pinned PR head of the dispatched item (its
// head_sha metadata, refreshed from the bead at dispatch time), or "" when the
// item carries none -- worker and feedback beads have no head, so no head bound
// applies to them (bead pg2-afre3).
func dispatchHeadSHA(it item.Item) string {
	h, _ := it.Metadata["head_sha"].(string)
	return h
}

// absorbDuplicate treats this dispatch as an already-in-flight duplicate of
// existing's session: it never re-Ensures/Sends (that would launch a SECOND
// ccpool session for the same bead — exactly what INV-EVT-2 forbids), it only
// waits on the EXISTING session's own outcome, addressing it by its own
// ExternalID and reusing its own worktree (existing.CWD) for the watchdog's
// guarded reset. If that session has already settled (idle/errored), waitDone
// returns on its very first poll with the existing outcome; if it is still
// in-flight, this call blocks until it settles — the "deferred ack pointing
// at the same in-flight work" this task's Contract calls for, held open the
// same way a normal inline dispatch already holds the call open for a
// long-running session (cmd/pg-router-ccpool-handler/dispatch.go's own
// doc comment). Mirrors run()'s own tail (budget-gated watchdog race +
// waitFailureResult) so the terminal verb reported here is identical to what
// a first-time dispatch of the same work would have reported.
func (r *ccpoolRun) absorbDuplicate(ctx context.Context, d DispatchContext, existing ccpool.Session) (report.Result, error) {
	r.noteSession(existing)
	cc := d.Role.CCPool
	// The lease covers this absorbed session for as long as this dispatch is alive
	// (takeOverForAbsorb already wrote the first refresh under the session lock).
	defer r.startLease(ctx, existing.ExternalID)()
	// pg2-oq6cy: a live session still in `ready` never took a turn — no prompt
	// was ever delivered (a healthy dispatched session moves ready->working
	// within seconds of Send, and afterwards settles at idle, never back to
	// ready). This is the leftover of a launch that timed out after ccpool
	// reported failure but before the session became ready: absorbing it
	// without Send would wait to the budget hard stop on every re-dispatch
	// (28 review budget events for one bead, pg2-uwnjp). Deliver the prompt to
	// it instead, into its own worktree. If that fails, purge-close it so the
	// next dispatch relaunches fresh, and hand the bead back.
	if existing.Live && existing.State == ccpool.StateReady {
		nudge := r.renderNudge(cc, d, existing.CWD)
		slog.Warn("absorbed duplicate is ready but never received its prompt; sending it",
			"role", d.Role.Name, "bead", d.Item.ID, "session", existing.ExternalID)
		if err := r.deps.CC.Send(ctx, existing.ExternalID, nudge, ccpool.ModeNoWait); err != nil {
			_ = r.deps.CC.Close(ctx, existing.ExternalID, true)
			_ = beads.Unclaim(ctx, r.deps.BD, d.Item.ID)
			return failureAction(report.Unclaimed, d.Item.ID),
				fmt.Errorf("send %s: stranded ready session: %w", existing.ExternalID, err)
		}
	}
	var werr error
	if budgetUnlimited(cc.Budget) {
		werr = r.waitDone(ctx, nil, d, existing.ExternalID)
	} else {
		// An absorbed session keeps its original launch time as the budget's
		// start, so absorbing an under-budget orphan does not grant it a fresh
		// budget (INV-CCH-18; pg2-3j76b's case). Absent launched_at (an older
		// build) falls back to now, as before.
		start, _ := existing.LaunchedAt()
		werr = r.workerWaitWithWatchdog(ctx, d, existing.ExternalID, existing.CWD, start)
	}
	return r.finishWait(ctx, cc, d, existing.ExternalID, existing.CWD, werr)
}

// renderNudge builds the prompt sent to a ccpool session: the (non-editable) safety
// preamble when authorship_guard is set, the (non-editable) protected-path notice
// when the session runs under permissionMode=dontAsk, then the role's rendered task
// prompt, then the budget prompt-line (empty when the budget is unlimited).
func (r *ccpoolRun) renderNudge(cc *roles.CCPoolConfig, d DispatchContext, worktreeDir string) string {
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
	if r.deps.Cfg.PermissionMode == "dontAsk" {
		sb.WriteString(prompt.ProtectedPathNotice())
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
func (r *ccpoolRun) workerWaitWithWatchdog(ctx context.Context, d DispatchContext, name, worktreeDir string, start time.Time) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var owner atomic.Bool
	claimTerminal := func() bool { return owner.CompareAndSwap(false, true) }

	wd := &watchdog.Watchdog{
		Reader: r.deps.reader(),
		CC:     r.deps.CC,
		BD:     r.deps.BD,
		Log:    r.deps.Log,
		Budget: d.Role.CCPool.Budget,
		Role:   d.Role.Name,
		Pool:   d.Role.CCPool.PoolDir,
		// Per-bead budget-stop counter (pg2-6akgz); 0 = disabled.
		BudgetStopEscalateAfter: d.Role.CCPool.BudgetStopEscalateAfter,
		RepoRoot:                r.deps.Cfg.RepoRoot,
		WorktreeDir:             worktreeDir, // the per-bead worktree the worker ran in (pg2-yukh)
		Start:                   start,       // zero = now; an absorbed session passes its recorded launch time
		ReminderMsg:             r.deps.Cfg.ReminderMsg,
		WrapUpMsg:               r.deps.Cfg.WrapUpMsg,
		Git:                     r.deps.git(),
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
// session ended is a success), then — if that is still not a DoneSignal — gives
// it ONE bounded extra poll interval and re-reads again before failing, so a
// same-bead external mutation racing the exact instant of death (e.g. the
// ACL's own head-advance reopen, pg2-qmltm) has a chance to become visible
// before this is classified as an unexplained death. On failure it applies the
// role's OnFailure.
//
// claimTerminal arbitrates the single-terminal race with the budget watchdog:
// EVERY terminal outcome (success or failure) is gated through it, so exactly
// one of {waitDone, watchdog} owns the bead's final state. The loser performs no
// bead mutation and waits for the orchestrator to cancel ctx. A nil claimTerminal
// means no watchdog is racing (feedback dispatches / direct tests) — always own.
// (pg2-c1vp)
func (r *ccpoolRun) waitDone(ctx context.Context, claimTerminal func() bool, d DispatchContext, name string) error {
	completion := d.Role.CCPool.Completion
	deadline := r.deps.clock().Add(r.deps.Cfg.MaxWait)
	var tr complete.Tracker
	alertedNeedsInput := false // edge latch: fire the needs_input alert at most once
	// check reads the bead (from the role's own tracker, pg2-2grpj) and reports
	// whether the role's completion rule is satisfied; a failed read is
	// "not done" (transient bd hiccup, matches bash bead_status 2>/dev/null).
	check := func() (bool, string) {
		iss, err := beads.ShowObj(ctx, r.deps.BD, d.Item.ID)
		obs := complete.Observation{Status: iss.Status, Labels: iss.Labels, Comments: iss.CommentCount}
		return tr.Done(completion, obs, err == nil), iss.Status
	}
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
		// Edge-detect needs_input and alert the operator exactly once. Pure
		// observation: it does not affect the terminal decision below (active()
		// still treats needs_input as keep-waiting, bounded by MaxWait).
		if !alertedNeedsInput {
			if st, ok := r.sessionState(ctx, name); ok && st == ccpool.StateNeedsInput {
				r.alertNeedsInput(name, d.Item.ID)
				alertedNeedsInput = true
			}
		}
		// transient bd hiccup => "" => not-done, keep polling (matches bash bead_status 2>/dev/null)
		done, status := check()
		if done {
			if won() {
				return nil
			}
			return lose()
		}
		tr.Observe(completion, status)
		if !r.active(ctx, name) {
			// re-check-after-death: the bead may have closed as the session ended.
			if done, _ = check(); done {
				if won() {
					return nil
				}
				return lose()
			}
			// pg2-qmltm: a legitimate concurrent external mutation of THIS SAME
			// bead -- e.g. pg-router's ACL reopening a review-pr bead on PR
			// head-advance (ReopenReview: status=open, assignee cleared;
			// docs/pr-review-flow.md JR4) -- can land microseconds after the
			// session is observed dead but before that write is visible to the
			// read just above. One bounded re-read (mirrors closeReason's own
			// bounded re-read just below, for the identical class of race) gives
			// the write a chance to become visible before this branch commits to
			// "unexplained death." seenClaimed still gates it exactly as it gates
			// the ordinary DoneSignal check above, so a session that never
			// claimed the bead before dying (the true startup-race failure
			// DoneSignal's own seenClaimed guard exists for) is unaffected.
			if err := r.deps.waitPoll(ctx, r.deps.Cfg.PollInterval); err != nil {
				return err
			}
			if done, _ = check(); done {
				if won() {
					return nil
				}
				return lose()
			}
			if won() {
				r.captureSignature() // before any teardown (INV-CCH-9)
				// INV-CCH-7: distinguish an EXTERNAL close (ccpool itself ended the
				// session — idle_ttl, cap_eviction, operator) from a genuine worker
				// failure. On external close, release the bead regardless of the
				// role's configured on_failure and escalate via the two-strike
				// idiom; only an unexplained death or the handler's own close still
				// applies on_failure below.
				if reason := r.closeReason(ctx, name); externalCloseReasons[reason] {
					_ = beads.Comment(ctx, r.deps.BD, d.Item.ID,
						fmt.Sprintf("released: ccpool closed the session (%s) before the bead completed; retrying", reason))
					_ = beads.Unclaim(ctx, r.deps.BD, d.Item.ID)
					escalated := r.escalateEviction(ctx, d.Item.ID)
					return fmt.Errorf("%s: %w", d.Item.ID, &externallyClosed{reason: reason, escalated: escalated})
				}
				// Unexplained death (not an external close): close the dead row
				// now, mirroring run()'s own not-ingested cleanup (r.deps.CC.Close
				// call above) — otherwise this exact row sits in ccpool's list
				// forever and collides with every future dispatch attempt for
				// this (bead, role) pair (pg2-04bf7's zombie-session root cause,
				// part b — defense-in-depth alongside findSessionByName's own
				// dead-row-is-absent fix). Best-effort: a failed Close here does
				// not change the failure outcome already decided below.
				_ = r.deps.CC.Close(ctx, name, false)
				return r.fail(ctx, d, "session exited before completing")
			}
			return lose()
		}
		if !r.deps.clock().Before(deadline) {
			// final status check after the deadline.
			if done, _ = check(); done {
				if won() {
					return nil
				}
				return lose()
			}
			if won() {
				r.captureSignature() // INV-CCH-9
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

// beadsDirFor returns the workspace dir whose .beads holds the role's items:
// the role's BeadsDir when set (pg2-2grpj), else repoRoot.
func beadsDirFor(repoRoot string, cc *roles.CCPoolConfig) string {
	if cc != nil && cc.BeadsDir != "" {
		return cc.BeadsDir
	}
	return repoRoot
}

func (r *ccpoolRun) fail(ctx context.Context, d DispatchContext, reason string) error {
	_ = complete.OnFailure(ctx, r.deps.BD, d.Role.CCPool.OnFailure, d.Item.ID)
	return fmt.Errorf("%s: %s", d.Item.ID, reason)
}

// externalCloseReasons: SOMETHING ELSE ended the session while the worker was
// fine — the reaper (idle_ttl, cap_eviction) or an operator. "handler" is this
// process's own close (watchdog hard-stop, not-ingested cleanup), which already
// owns its outcome. (INV-CCH-7, ADR 0072)
var externalCloseReasons = map[string]bool{"idle_ttl": true, "cap_eviction": true, "operator": true}

// ErrExternallyClosed wraps a death whose close reason was external; the wait
// failure mapping reports it as Unclaimed (or Escalated on the second
// consecutive strike — see externallyClosed below).
var ErrExternallyClosed = errors.New("session closed externally by ccpool")

// externallyClosed is the concrete error waitDone's death branch returns for an
// external close: it carries the specific reason plus whether escalateEviction
// found this to be the SECOND consecutive external close (escalated). Its Is
// method matches ErrExternallyClosed directly, so errors.Is(err,
// ErrExternallyClosed) sees this type without needing a %w-wrapped sentinel;
// waitFailureResult additionally errors.As's the struct to read escalated.
type externallyClosed struct {
	reason    string
	escalated bool
}

func (e *externallyClosed) Error() string {
	return fmt.Sprintf("session closed externally by ccpool (%s)", e.reason)
}

func (e *externallyClosed) Is(target error) bool {
	return target == ErrExternallyClosed
}

// closeReason returns ccpool's recorded close reason for the session, "" when
// absent, unstamped, or unreadable. ccpool stamps before teardown, so an empty
// reason on a just-dead row is rare; one bounded re-read covers a list that
// raced the stamp.
func (r *ccpoolRun) closeReason(ctx context.Context, externalID string) string {
	read := func() (string, bool) {
		sessions, err := r.deps.CC.List(ctx)
		if err != nil {
			return "", false
		}
		for _, s := range sessions {
			if s.ExternalID == externalID {
				return s.CloseReason, true
			}
		}
		return "", false
	}
	reason, present := read()
	if reason == "" && present {
		if err := r.deps.waitPoll(ctx, r.deps.Cfg.PollInterval); err == nil {
			reason, _ = read()
		}
	}
	return reason
}

// escalateEviction mirrors escalateLaunchFailure for external closes: label on
// the first, human on the second consecutive one (INV-CCH-7). Returns true iff
// it escalated to human (repeat external close); false on the first (label
// only) or on a bd read hiccup.
func (r *ccpoolRun) escalateEviction(ctx context.Context, beadID string) bool {
	already, err := beads.HasLabel(ctx, r.deps.BD, beadID, "pool-evicted")
	if err != nil {
		return false // can't tell ⇒ do nothing this pass; the next external close retries
	}
	if already {
		_ = beads.AddHuman(ctx, r.deps.BD, beadID)
		return true
	}
	_ = beads.AddLabel(ctx, r.deps.BD, beadID, "pool-evicted")
	return false
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
			r.noteSession(s)
			return s.Live && s.State != ccpool.StateErrored && s.State != ccpool.StateIdle
		}
	}
	r.noteAbsent()
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
			r.noteSession(s)
			return s.State, true
		}
	}
	return "", false
}

// alertNeedsInput fires the one-shot operator alert when a dispatched session
// enters needs_input: a distinct eventlog record (kind "needs_input") plus a
// warn log line that NAMES the session and the attach command. It does NOT
// change completion semantics — needs_input stays non-terminal and waitDone
// keeps polling to MaxWait. ccpool's own desktop notifier still fires
// independently (internal/notify, On=[needs_input,failed]); this surfaces the
// same event into pg-router's log/eventlog so an operator watching pg-router sees
// which session to attach to. (pg2-th35)
func (r *ccpoolRun) alertNeedsInput(externalID, beadID string) {
	slog.Warn("session needs input — attach to continue",
		"session", externalID, "bead", beadID, "attach", "ccpool attach "+externalID)
	if r.deps.Log != nil {
		_ = r.deps.Log.Emit("warn", "needs_input",
			"session needs input; operator must attach",
			map[string]any{"session": externalID, "bead": beadID, "attach": "ccpool attach " + externalID})
	}
}

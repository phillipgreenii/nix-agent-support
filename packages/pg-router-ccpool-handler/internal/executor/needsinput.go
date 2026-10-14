package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// Stall bound for a drain session parked in needs_input (bead pg2-zybju,
// INV-CCH-29; amends ADR 0037 and ADR 0083 -- see ADR 0083's Amendment on the
// needs_input stall bound).
//
// An unattended drain session (a ccpool role whose completion is close-or-release)
// that raises a permission dialog nobody can answer sits in needs_input until a
// person attaches. ccpool's reaper spares it (ADR 0037), the orphan reconcile
// leaves it alone (ADR 0083), and the lane it occupies is the one that would
// otherwise work the next bead (a pool of size 1 per tracker, ADR 0089): six
// stalls were recorded between 2026-10-10 and 2026-10-13, the longest about 80
// minutes. This file is the bound on that stall. It lives in the SUPERVISING
// dispatch's wait loop, not in a
// reconcile that runs at the next dispatch, because the dispatch of the wedged
// session is exactly the one occupying the lane: no later dispatch of the role
// runs while it is blocked.
//
// Rules, per observed needs_input episode (one poll per Cfg.PollInterval, on the
// Deps.Now clock so a test never sleeps):
//
//  1. The clock starts when the session is first seen in needs_input (or at the
//     row's last_activity_at when that is older and the transcript is
//     observable, so a handler restart does not grant a fresh bound), and
//     restarts at the newest write to the session's transcript or its subagents'
//     (see the liveness guard below). A poll that sees any other state ends the
//     episode: the timer is reset.
//  2. After the bound (default 15m) of quiet needs_input, send Escape: the
//     existing `ccpool cancel` (an Escape burst, then a confirmed-static pane,
//     which also resets the row to ready). That dismisses the dialog.
//  3. After a further GRACE (the role's worktree quiet window, default 2m) in
//     which nothing wrote to the transcript and the session is still needs_input
//     or the ready ccpool reset it to, the session is stuck for good: close it
//     (non-purge, reason handler) and release the bead claim in ONE bd call
//     (status open and assignee cleared together), but only when the bead is
//     still in_progress under the role's own actor. A closed bead, an unassigned
//     bead or a bead a peer holds is never written to.
//  4. The release is struck twice at most: the first stall of a bead labels it
//     (drainStallLabel), the second consecutive one adds human in the same call
//     as the release, so a bead that deterministically wedges every worker stops
//     being re-offered (the same two-strike idiom as pool-evicted and
//     drain-unclaimed-end). A completed dispatch clears the label.
//
// LIVENESS GUARD. ccpool never leaves needs_input when a dialog is resolved (no
// hook edge for it): a session whose dialog was answered, by a person who
// attached or by the orchestrator stopping the blocked subagent, keeps reading
// needs_input until its turn ends, which for a drain orchestrator can be hours.
// Acting on state alone would Escape and then kill a healthy session. A session
// that is blocked on a dialog writes nothing; a healthy one writes on every tool
// call. So the bound counts QUIET needs_input: any write to the transcript or a
// subagent transcript after the episode began restarts it. A transcript that
// cannot be observed offers no evidence of life and does not delay the bound
// (waitSessionQuiet takes the same direction).

// drainStallLabel counts a bead's consecutive needs_input stall clears: added on
// the first, read on the second, which adds human. Like drain-unclaimed-end it is
// a plain label and deliberately not the budget-stop: prefix (INV-CCH-11).
const drainStallLabel = "drain-needs-input-stall"

// Event kinds the stall bound writes to the handler event log.
const (
	needsInputEscapeKind  = "needs_input_escape"
	needsInputClearedKind = "needs_input_cleared"
)

// ErrNeedsInputStalled wraps the end of a drain session the stall bound closed.
// Like ErrExternallyClosed it is a returned error because waitDone's other
// non-failure results have no carrier; waitFailureResult reports it as Unclaimed
// (the claim was released), Escalated (the second consecutive strike) or no verb
// (nothing was written to the bead).
var ErrNeedsInputStalled = errors.New("session stalled in needs_input and was cleared by the handler")

// needsInputStalled is the concrete error behind ErrNeedsInputStalled.
type needsInputStalled struct {
	// released: the claim was released (one bd call).
	released bool
	// escalated: the release also added human (the second consecutive strike).
	escalated bool
}

func (e *needsInputStalled) Error() string {
	switch {
	case e.escalated:
		return "session stalled in needs_input and was cleared (claim released, second consecutive stall: escalated to human)"
	case e.released:
		return "session stalled in needs_input and was cleared (claim released)"
	}
	return "session stalled in needs_input and was closed (no claim was released)"
}

func (e *needsInputStalled) Is(target error) bool { return target == ErrNeedsInputStalled }

// needsInputStall tracks one supervised session's needs_input episode.
type needsInputStall struct {
	bound time.Duration // 0 = the bound does not apply to this dispatch
	grace time.Duration // Escape -> close gap
	// start is when the current episode began; zero when no episode is running.
	start time.Time
	// escapedAt is when this episode's Escape finished; zero before it.
	escapedAt time.Time
}

func (st *needsInputStall) reset() { st.start, st.escapedAt = time.Time{}, time.Time{} }

// earlierOf returns the earlier of two instants (a write time is clamped to now,
// so a clock that disagrees with the filesystem never puts the episode start in
// the future).
func earlierOf(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// needsInputBound is the stall bound for a dispatch of cc: 0 (none) for any role
// that is not a drain role, 0 when the handler-wide kill switch is set, else the
// role's own override or the handler-wide default.
func (r *ccpoolRun) needsInputBound(cc *roles.CCPoolConfig) time.Duration {
	if cc == nil || cc.Completion != roles.CloseOrRelease || r.deps.Cfg.NeedsInputStall <= 0 {
		return 0
	}
	if cc.NeedsInputStall > 0 {
		return cc.NeedsInputStall
	}
	return r.deps.Cfg.NeedsInputStall
}

// newNeedsInputStall builds the tracker for a dispatch of cc.
func (r *ccpoolRun) newNeedsInputStall(cc *roles.CCPoolConfig) *needsInputStall {
	st := &needsInputStall{bound: r.needsInputBound(cc)}
	poll := r.deps.Cfg.PollInterval
	if poll <= 0 {
		poll = time.Second
	}
	st.grace = max(r.quietWindow(cc), 2*poll)
	return st
}

// observed returns the row noteSession/noteAbsent last recorded: the session as
// the most recent poll saw it, and whether it was present in the list.
func (r *ccpoolRun) observed() (ccpool.Session, bool) {
	r.sigMu.Lock()
	defer r.sigMu.Unlock()
	return r.row, r.rowOK
}

// latestActivity returns the newest write among the transcript of path and its
// subagents' (the Deps seam, nil = the filesystem scan).
func (r *ccpoolRun) latestActivity(path string) (time.Time, bool) {
	if path == "" {
		return time.Time{}, false
	}
	if r.deps.LatestActivity != nil {
		return r.deps.LatestActivity(path)
	}
	return LatestTranscriptActivity(path)
}

// stallStep runs one poll of the stall bound. It returns stop=true, with the
// dispatch's terminal error, only when it cleared the session; (false, nil)
// means keep waiting. won is waitDone's single-terminal arbiter against the
// budget watchdog: the claim is taken only once the bead has been read and
// before the first bead write, exactly like every other terminal bead mutation.
func (r *ccpoolRun) stallStep(ctx context.Context, d DispatchContext, name string, st *needsInputStall, won func() bool, lose func() error) (stop bool, err error) {
	if st.bound <= 0 {
		return false, nil
	}
	row, ok := r.observed()
	now := r.deps.clock()
	inEpisode := ok && row.Live && row.State == ccpool.StateNeedsInput
	postEscape := ok && row.Live && row.State == ccpool.StateReady && !st.escapedAt.IsZero()
	if !inEpisode && !postEscape {
		st.reset()
		return false, nil
	}
	latest, haveLatest := r.latestActivity(row.TranscriptPath)
	if st.start.IsZero() {
		st.start = now
		if haveLatest && row.LastActivityAt > 0 {
			if t := time.Unix(row.LastActivityAt, 0); t.Before(now) {
				st.start = t
			}
		}
	}
	// A write after the Escape means the session reacted to it: progress, not a
	// stall. A session that still reads needs_input (its hook edge never came)
	// starts a new episode at that write; one that reads ready is simply working.
	if !st.escapedAt.IsZero() && haveLatest && latest.After(st.escapedAt) {
		st.reset()
		if inEpisode {
			st.start = earlierOf(latest, now)
		}
		return false, nil
	}
	quietSince := st.start
	if haveLatest && latest.After(quietSince) {
		quietSince = earlierOf(latest, now)
	}

	cc := d.Role.CCPool
	if st.escapedAt.IsZero() {
		if now.Sub(quietSince) < st.bound {
			return false, nil
		}
		r.stallEscape(ctx, d, name, st, now.Sub(quietSince))
		return false, nil
	}
	if now.Sub(st.escapedAt) < st.grace {
		return false, nil
	}
	return r.stallClear(ctx, d, name, cc, now.Sub(quietSince), st, won, lose)
}

// stallEscape sends the Escape: `ccpool cancel` through the pool runner. A
// failure (the cancel could not be confirmed, ccpool errored) is logged and the
// attempt still counts: the close that follows after the grace is the remedy.
func (r *ccpoolRun) stallEscape(ctx context.Context, d DispatchContext, name string, st *needsInputStall, quiet time.Duration) {
	beadID := d.Item.ID
	slog.Warn("session stalled in needs_input; sending Escape",
		"session", name, "bead", beadID, "quiet", quiet, "bound", st.bound)
	err := r.deps.CC.Cancel(ctx, name)
	st.escapedAt = r.deps.clock()
	if err != nil {
		slog.Warn("stall bound: Escape was not confirmed; the close follows after the grace unless the session recovers",
			"session", name, "bead", beadID, "err", err)
	}
	f := r.stallFields(d, name, quiet, st.bound)
	f["escape_ok"] = err == nil
	if err != nil {
		f["escape_error"] = err.Error()
	}
	f["grace_s"] = st.grace.Seconds()
	r.emitStall("warn", needsInputEscapeKind, "drain session stalled in needs_input; Escape sent", f)
}

// stallClear closes the stuck session and releases its claim.
func (r *ccpoolRun) stallClear(ctx context.Context, d DispatchContext, name string, cc *roles.CCPoolConfig, quiet time.Duration, st *needsInputStall, won func() bool, lose func() error) (bool, error) {
	beadID := d.Item.ID
	// Read the bead BEFORE taking terminal ownership or closing anything: if it
	// cannot be read nobody can tell who holds the claim, and closing the session
	// would strand a claim nobody sees. The next poll retries.
	iss, err := beads.ShowObj(ctx, r.deps.BD, beadID)
	if err != nil {
		slog.Warn("stall bound: bead lookup failed; retrying next poll", "session", name, "bead", beadID, "err", err)
		return false, nil
	}
	if iss.Status == "closed" {
		// The bead finished while the row still read needs_input: the next poll's
		// completion check ends the dispatch.
		return false, nil
	}
	if !won() {
		return true, lose()
	}
	// Close FIRST: releasing the claim while the session is still live would let
	// another worker take the bead while this one could still act on it. A close
	// that fails leaves the claim alone; the dispatch ends with an error and the
	// next dispatch of the bead absorbs the live session and tries again.
	if err := r.deps.CC.Close(ctx, name, false); err != nil {
		slog.Error("stall bound: close failed; claim left in place", "session", name, "bead", beadID, "err", err)
		return true, fmt.Errorf("%s: close stalled session %s: %w", beadID, name, err)
	}
	res := &needsInputStalled{}
	mine := cc.Actor != "" && iss.Status == "in_progress" && iss.Assignee == cc.Actor
	switch {
	case mine:
		if iss.HasLabel(drainStallLabel) {
			// Second consecutive stall of this bead: human and the release in ONE call,
			// preceded by the park note (bead pg2-78mxs): nothing here is a question
			// for a person, and the note names the drain worktree the session left.
			note := r.parkNote(ctx, d, name,
				fmt.Sprintf("needs_input stall bound hit twice in a row (bound %s)", st.bound),
				[]string{
					fmt.Sprintf("the session sat in needs_input for %s without a person (a permission dialog nobody can answer in an unattended session)", quiet.Round(time.Second)),
					"the handler sent Escape, then closed the session; the bead already carried " + drainStallLabel + " from the previous stall",
				})
			err = beads.AddHumanAndUnclaim(ctx, r.deps.BD, beadID, note)
			res.escalated = err == nil
		} else {
			err = beads.Unclaim(ctx, r.deps.BD, beadID)
		}
		if err != nil {
			slog.Error("stall bound: claim release failed; the claim is stranded under a closed session",
				"session", name, "bead", beadID, "err", err)
		}
		res.released = err == nil
		if res.released && !res.escalated {
			_ = beads.Comment(ctx, r.deps.BD, beadID, fmt.Sprintf(
				"released: session %s sat in needs_input for %s without a person (a permission dialog nobody can answer in an unattended session); the handler sent Escape, then closed the session (needs_input stall bound %s)",
				name, quiet.Round(time.Second), st.bound,
			))
			_ = beads.AddLabel(ctx, r.deps.BD, beadID, drainStallLabel)
		}
	case iss.Assignee != "" && iss.Assignee != cc.Actor:
		slog.Info("stall bound: session closed; the bead is held by another actor: no write",
			"session", name, "bead", beadID, "assignee", iss.Assignee, "status", iss.Status)
		res = nil
	default:
		slog.Info("stall bound: session closed; the bead holds no claim of the role's own: no write",
			"session", name, "bead", beadID, "assignee", iss.Assignee, "status", iss.Status)
	}
	f := r.stallFields(d, name, quiet, st.bound)
	f["released"] = res != nil && res.released
	f["escalated"] = res != nil && res.escalated
	f["bead_status"] = iss.Status
	f["bead_assignee"] = iss.Assignee
	r.emitStall("warn", needsInputClearedKind, "drain session stalled in needs_input; closed by the handler", f)
	slog.Warn("session stalled in needs_input; closed", "session", name, "bead", beadID,
		"released", f["released"], "escalated", f["escalated"])
	if res == nil {
		return true, fmt.Errorf("%s: %w", beadID, ErrPeerHeld)
	}
	return true, fmt.Errorf("%s: %w", beadID, res)
}

func (r *ccpoolRun) stallFields(d DispatchContext, name string, quiet, bound time.Duration) map[string]any {
	pool := "default"
	if d.Role.CCPool != nil && d.Role.CCPool.PoolDir != "" {
		pool = d.Role.CCPool.PoolDir
	}
	return map[string]any{
		"session": name, "bead": d.Item.ID, "role": d.Role.Name, "pool": pool,
		"needs_input_s": quiet.Seconds(), "bound_s": bound.Seconds(),
	}
}

func (r *ccpoolRun) emitStall(level, kind, msg string, fields map[string]any) {
	if r.deps.Log != nil {
		_ = r.deps.Log.Emit(level, kind, msg, fields)
	}
}

package watchdog

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/complete"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/gitenv"
	"github.com/phillipgreenii/x/gitclient"
)

// gitCallTimeout bounds each git probe/mutation this file issues through
// x/gitclient so a wedged git can neither hang the hard-stop sequence nor
// defeat ctx cancellation (pg2-yy42). gitclient's own Client.run already
// wraps ctx cancellation/deadlines explicitly and sets a WaitDelay bounding
// how long a killed child's inherited I/O pipes are waited on (design
// §4.4's Context contract); this per-call context.WithTimeout is this
// file's OWN outer bound layered on top of that. It replaces the retired
// execGit helper's identical bound for the read-only toplevel probe
// (safeToReset), and is newly applied to the mutating reset/clean calls in
// terminal, which previously had no bound of their own and relied solely
// on the caller's ambient ctx (pg2-ljyaj).
const gitCallTimeout = 10 * time.Second

// gitLocatorCleaner is the composite role this file needs from x/gitclient
// at a given anchor directory: Locator.Toplevel backs safeToReset's
// worktree-root backstop, and Cleaner.ResetHard/CleanUntracked back
// terminal's guarded hard-stop reset (design §4.5's consumer mapping: "pr-
// pool watchdog -> Cleaner + Locator (Toplevel inside safeToReset)").
type gitLocatorCleaner interface {
	gitclient.Locator
	gitclient.Cleaner
}

// gitOpener anchors an x/gitclient client at dir, sized to the widest
// role(s) this file needs (design §4.6's app-local opener seam for multi-
// directory consumers -- this watchdog probes arbitrary session paths, one
// client per hard-stop rather than a cached long-lived one).
type gitOpener func(ctx context.Context, dir string) (gitLocatorCleaner, error)

// openGit is a package-level var, not a plain function, so tests can
// substitute a client anchored via gitclient.WithGit at a script that
// deliberately blocks -- proving ctx cancellation/timeout is honored end to
// end -- without threading a new testing seam through Watchdog itself.
var openGit gitOpener = func(ctx context.Context, dir string) (gitLocatorCleaner, error) {
	return gitclient.New(ctx, dir)
}

// terminal runs the 100% hard-stop sequence: 2nd cancel, guarded worktree reset,
// session close, budget note, unclaim, eventlog. Each step is best-effort.
// The session MUST be closed here, BEFORE the unclaim: the pass-level
// teardownAll does not reach a stable-named session that a duplicate-absorb
// re-attached to, so an open session let one dead session be re-absorbed 28
// times (pg2-uwnjp, pg2-vwb4c). purge=false keeps the transcript.
func (w *Watchdog) terminal(ctx context.Context, sessionName, beadID string, be *BudgetError) {
	_ = w.CC.Cancel(ctx, sessionName) // 2nd cancel (idempotent/safe)

	wt := w.sessionCWD(ctx, sessionName)
	didReset := false
	if safeToReset(ctx, wt, w.RepoRoot, w.WorktreeDir) {
		octx, ocancel := context.WithTimeout(ctx, gitCallTimeout)
		defer ocancel()
		if client, err := openGit(octx, wt); err == nil {
			rctx, rcancel := context.WithTimeout(ctx, gitCallTimeout)
			defer rcancel()
			if err := client.ResetHard(rctx); err == nil {
				cctx, ccancel := context.WithTimeout(ctx, gitCallTimeout)
				defer ccancel()
				_ = client.CleanUntracked(cctx)
				didReset = true
			}
		}
	}

	// No outcome mark precedes the close: the stopped row is settled, and a
	// settled row is never absorbed (INV-CCH-30), so the next dispatch runs in a
	// fresh session without one (the incomplete mark of bead pg2-tc9c3 is gone,
	// ADR 0094's Decision item 6). A row someone already closed is not closed
	// again: a second non-purge close re-stamps it and appends another close
	// event (bead pg2-92rfu).
	_, _ = ccpool.CloseIfOpen(ctx, w.CC, sessionName, false)

	_ = beads.Comment(ctx, w.BD, beadID, "interrupted — budget")
	iss, stops, counted := w.recordBudgetStop(ctx, sessionName, beadID)
	outcome, closed := "", false
	if counted {
		outcome, closed = w.escalate(ctx, sessionName, beadID, be, iss, stops)
	}
	// Never unclaim (= reopen) a bead that is already closed (bead pg2-sx246).
	// escalate only learns that on the AT-threshold path; below the threshold
	// (or with counting disabled / the record failed) it reports closed=false, so
	// the status is read fresh here, right before the release. A failed read
	// falls back to the plain unclaim (the pre-existing safe direction).
	if !closed {
		if st, err := beads.Status(ctx, w.BD, beadID); err == nil && st == "closed" {
			closed = true
		}
	}
	unclaimSkipped := closed
	if unclaimSkipped {
		w.emit("info", "budget_unclaim_skipped", "bead already closed; release skipped (no reopen)", map[string]any{
			"session": sessionName, "bead": beadID, "role": be.Role,
		})
	} else {
		_ = beads.Unclaim(ctx, w.BD, beadID)
	}
	fields := map[string]any{
		"session": sessionName, "bead": beadID, "worktree_reset": didReset, "worktree": wt,
		"role": be.Role, "pool": be.Pool, "limit": string(be.Limit),
		"failure_signature": "budget",
		"used":              be.Used, "cap": be.Cap, "elapsed": be.Elapsed.Seconds(),
	}
	if unclaimSkipped {
		fields["unclaim_skipped"] = true
	}
	if counted {
		fields["budget_stops"] = stops
		if outcome != "" {
			fields["escalation"] = outcome
		}
	}
	w.emit("error", "hard_stop", "budget hard stop reached", fields)
}

// recordBudgetStop writes this session's budget-stop record on the bead and
// returns the resulting distinct-session count (bead pg2-6akgz). It MUST run
// BEFORE the unclaim, and is reached only from terminal, i.e. only by the
// watchdog that OWNS the terminal outcome (ClaimTerminal), so the watchdog
// losing the race to waitDone never counts. Disabled (BudgetStopEscalateAfter
// <= 0) it does nothing. On any bd failure it logs and reports counted=false,
// so the caller falls back to today's plain unclaim (the safe direction); the
// Budget is never touched.
func (w *Watchdog) recordBudgetStop(ctx context.Context, sessionName, beadID string) (iss beads.Issue, stops int, counted bool) {
	if w.BudgetStopEscalateAfter <= 0 {
		return beads.Issue{}, 0, false
	}
	if err := beads.RecordBudgetStop(ctx, w.BD, beadID, sessionName); err != nil {
		w.emit("warn", "budget_stop_record_failed", "budget-stop record failed; plain unclaim", map[string]any{
			"session": sessionName, "bead": beadID, "err": err.Error(),
		})
		return beads.Issue{}, 0, false
	}
	iss, err := beads.ShowObj(ctx, w.BD, beadID)
	n := beads.BudgetStopCount(iss)
	if err != nil {
		w.emit("warn", "budget_stop_record_failed", "budget-stop count failed; plain unclaim", map[string]any{
			"session": sessionName, "bead": beadID, "err": err.Error(),
		})
		return beads.Issue{}, 0, false
	}
	_ = beads.Comment(ctx, w.BD, beadID,
		fmt.Sprintf("budget stop %d of %d (session %s)", n, w.BudgetStopEscalateAfter, sessionName))
	return iss, n, true
}

// Escalation outcomes (beads pg2-mab1w, pg2-cty80) and the labels they act through.
const (
	// OutcomeEscalated is a bead put on the escalated path: it carries the
	// escalated label the deployment's *-escalated-work query selects, so the
	// escalation triager role decides what happens next (a split review, a
	// human, or another disposition).
	OutcomeEscalated = "escalated"
	// OutcomeHuman is the triage-role exception: the triager must not escalate
	// its own bead back to itself, so a triage session's stop parks the bead
	// for a human.
	OutcomeHuman = "human"

	LabelEscalated        = complete.EscalatedLabel
	LabelNeedsSplitReview = beads.LabelNeedsSplitReview
	LabelWasSplit         = "was-split"
	LabelSplitFromPrefix  = "split-from:"
	labelHuman            = "human"
)

// isTriageRole reports whether role is a triage role: its name contains
// "triage" (covers pg2-/zr-escalation-triager and the split-triage role). A
// triage role's own budget stop goes to a human, never back onto the escalated
// path it works, so a bead cannot loop through the triager forever.
func isTriageRole(role string) bool {
	return strings.Contains(role, "triage")
}

// splitIneligibleReason reports why a bead at the escalation threshold MUST NOT
// be sent on to split review by the triager ("" = splitting is allowed). Review
// beads are PR reviews (splitting makes no sense); an already-split bead or a
// split child gets no second round (the split subcommand refuses both). The
// reason travels in the escalation comment so the triager need not rediscover
// it. It does not change the outcome: every non-triage role goes to escalated.
func splitIneligibleReason(role string, iss beads.Issue) string {
	switch {
	case role == "review":
		return "review role"
	case iss.HasLabel(LabelWasSplit):
		return "already split"
	}
	for _, l := range iss.Labels {
		if strings.HasPrefix(l, LabelSplitFromPrefix) {
			return "split child"
		}
	}
	return ""
}

// escalate acts on the stop count once it reaches the threshold (beads
// pg2-mab1w, pg2-cty80): the bead is labelled escalated and handed to the
// escalation triager, except that a triage role's own stop goes to human. It
// runs BEFORE the unclaim so the label is in place when the bead returns to the
// pool (the deployment's discovery queries exclude escalated, so it does not
// come back). The count is never reset, so a manually removed escalated/human
// label re-escalates on the next stop. closed=true means the bead was closed
// mid-flight: nothing is written (and the caller must not unclaim, which would
// reopen it). On a bd write failure it logs and returns outcome "" so the
// caller does today's plain unclaim.
func (w *Watchdog) escalate(ctx context.Context, sessionName, beadID string, be *BudgetError, iss beads.Issue, stops int) (outcome string, closed bool) {
	if stops < w.BudgetStopEscalateAfter {
		return "", false
	}
	if iss.Status == "closed" {
		w.emit("info", "budget_escalation_skipped", "bead closed mid-flight; no escalation", map[string]any{
			"session": sessionName, "bead": beadID, "role": be.Role,
		})
		return "", true
	}
	triage := isTriageRole(be.Role)
	reason := splitIneligibleReason(be.Role, iss)
	outcome, how := OutcomeEscalated, "labelled escalated for the escalation triager"
	if triage {
		reason = "triage role"
		outcome, how = OutcomeHuman, "escalated to human (triage role; not escalated again)"
	} else if reason != "" {
		how += " (" + reason + "; not eligible for split review)"
	}
	comment := fmt.Sprintf("budget stops reached threshold %d; %s. stop sessions: %s. last stop: session=%s limit=%s used=%s cap=%s",
		w.BudgetStopEscalateAfter, how, strings.Join(beads.BudgetStopSessions(iss), ", "), sessionName, be.Limit,
		strconv.FormatFloat(be.Used, 'f', -1, 64), strconv.FormatFloat(be.Cap, 'f', -1, 64))
	fields := map[string]any{
		"session": sessionName, "bead": beadID, "role": be.Role, "pool": be.Pool,
		"outcome": outcome, "reason": reason, "budget_stops": stops, "threshold": w.BudgetStopEscalateAfter,
	}
	// A triage role's own stop is a hand-off to the operator: the strict pair
	// human + attention, written through the one-call helper (bead pg2-ikkpk;
	// docs/guides/bead-label-vocabulary.md). Every other role's stop only
	// labels the bead escalated for the triager.
	var labelErr error
	if triage {
		labelErr = beads.HandToHuman(ctx, w.BD, beadID)
	} else {
		labelErr = beads.AddLabel(ctx, w.BD, beadID, LabelEscalated)
	}
	if err := labelErr; err != nil {
		fields["err"] = err.Error()
		w.emit("warn", "budget_escalation_failed", "budget escalation failed; plain unclaim", fields)
		return "", false
	}
	_ = beads.Comment(ctx, w.BD, beadID, comment)
	if triage {
		// pg2-47rsh: the split-triage session hit its OWN budget. human is on;
		// drop needs-split-review so the bead is not left half-parked and the
		// split-triage completion sees a decided outcome. Never re-enters split.
		_ = beads.RemoveLabel(ctx, w.BD, beadID, LabelNeedsSplitReview)
	}
	w.emit("warn", "budget_escalation", "budget stops reached threshold; escalated to "+outcome, fields)
	return outcome, false
}

func (w *Watchdog) sessionCWD(ctx context.Context, externalID string) string {
	sessions, err := w.CC.List(ctx)
	if err != nil {
		return ""
	}
	for _, s := range sessions {
		if s.ExternalID == externalID {
			return s.CWD
		}
	}
	return ""
}

// safeToReset returns true only when path is a real git worktree root, distinct
// from repoRoot, inside worktreeDir. Symlink-resolved, boundary-checked (never a
// prefix-string match). On ANY uncertainty it returns false (no-op = safe).
func safeToReset(ctx context.Context, path, repoRoot, worktreeDir string) bool {
	if path == "" {
		return false
	}
	rp, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false // path doesn't exist -> safe no-op
	}
	rr, err := filepath.EvalSymlinks(repoRoot)
	if err == nil && rp == rr {
		return false // never the monorepo
	}
	wd, err := filepath.EvalSymlinks(worktreeDir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(wd, rp)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false // outside worktreeDir
	}
	// backstop: must be a worktree ROOT (toplevel == path), not REPO_ROOT.
	// x/gitclient's Locator role (Toplevel = `rev-parse --show-toplevel`)
	// backs this probe -- it replaces the retired execGit/gitToplevel
	// helpers (pg2-ljyaj) -- bounded by gitCallTimeout so a wedged git
	// can't hang this guard (pg2-yy42).
	cctx, cancel := context.WithTimeout(ctx, gitCallTimeout)
	defer cancel()
	client, err := openGit(cctx, rp)
	if err != nil {
		return false
	}
	tl, err := client.Toplevel(cctx)
	if err != nil {
		return false
	}
	if resolved, evalErr := filepath.EvalSymlinks(tl); evalErr == nil {
		tl = resolved
	}
	return tl == rp
}

// OSGit is the production GitRunner — runs `git -C <dir> <args...>`. This
// file's own hard-stop sequence (terminal/safeToReset above) no longer uses
// it: they migrated onto x/gitclient's Cleaner+Locator roles (bead pg2-
// ljyaj). OSGit remains here because internal/executor still injects it as
// the shared worktree-CREATION git seam (executor.Deps.git's nil fallback,
// which worktreeIsolation passes to internal/worktree.Ensure) — a separate,
// not-yet-landed migration (pg2-mj9n0, x/gitclient's WorktreeManager role).
// Retire this type only once that bead lands.
type OSGit struct{}

// Run runs `git -C <dir> <args...>` with a hermetic child environment (see
// this package's gitenv import) — retained for internal/executor's
// worktree-creation seam; see the OSGit doc comment above for why this
// file still defines it despite no longer calling it itself.
func (OSGit) Run(ctx context.Context, dir string, args ...string) error {
	return gitenv.Command(ctx, dir, args...).Run()
}

// HardStop runs the 100% hard-stop sequence (terminal) for a session whose
// dispatching handler is gone, so no Run loop is metering it: the dispatch-time
// orphan reconcile calls it for a starting/ready/working session whose
// supervision lease expired and whose time budget is exhausted (bead
// pg2-g2u9m, INV-CCH-18). The caller owns the "should this stop fire" decision
// and the single-terminal guarantee (it holds the per-session lock and
// re-checked the lease); w needs BD, CC, Log, RepoRoot, WorktreeDir (the
// session's own worktree, which safeToReset requires to be a worktree root) and
// BudgetStopEscalateAfter. w.ClaimTerminal is not consulted -- nothing races.
// The worktree is NOT removed here; the caller removes it afterwards under its
// own guards, as the dispatch-time cleanup does.
func HardStop(ctx context.Context, w *Watchdog, sessionName, beadID string, be *BudgetError) {
	w.terminal(ctx, sessionName, beadID, be)
}

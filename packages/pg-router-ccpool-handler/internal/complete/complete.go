// Package complete holds pg-router's completion semantics: when a dispatched bead
// counts as done, and what to do when it fails. The polling loop lives in the
// orchestrator; this package is the pure decision + the failure side effects.
package complete

import (
	"context"
	"slices"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// StatusDeferred is bd's status for a bead parked until a date; the drain
// protocol's stamp-refusal release leaves a bead in it, unassigned.
const StatusDeferred = "deferred"

// DoneSignal reports whether a bead has completed for the given completion mode.
//   - close-only:        done iff status == "closed".
//   - close-or-handback: done iff "closed", OR (seenClaimed && "open") — the
//     seenClaimed guard prevents a freshly-dispatched, not-yet-claimed "open" bead
//     from being mistaken for a hand-back (the startup race).
//   - close-or-release:  done iff "closed", OR the session has ENDED (sessionEnded:
//     not merely idle, INV-CCH-28) and the bead is unassigned and either
//     "deferred" or ("open" with the claim latched). assignee is the bead's
//     current assignee; a bead anyone holds is never a hand-back. The marks that
//     let an "open" bead count without the latch need labels and dates, so they
//     live in Tracker.Done.
func DoneSignal(c roles.Completion, status string, seenClaimed bool, assignee string, sessionEnded bool) bool {
	if status == "closed" {
		return true
	}
	if (c == roles.CloseOrHandback || c == roles.CloseOrTriage) && seenClaimed && status == "open" {
		return true
	}
	if c == roles.CloseOrRelease && sessionEnded && assignee == "" {
		return status == StatusDeferred || (seenClaimed && status == "open")
	}
	return false
}

// EscalatedLabel is the label whose removal counts as de-escalation for the
// close-or-triage completion.
const EscalatedLabel = "escalated"

// Observation is one read of the dispatched bead.
type Observation struct {
	Status   string
	Labels   []string
	Comments int
	// Assignee is the bead's assignee ("" when unassigned). close-or-release
	// reads it (claim latch, hand-back test); the other modes ignore it.
	Assignee string
	// FutureDefer is true when the bead's defer_until is later than now (the
	// caller owns the clock). Read by close-or-release only.
	FutureDefer bool
	// Blocked is true when the bead has an open blocks dependency, read from a
	// single-bead read. Read by close-or-release only.
	Blocked bool
}

// Tracker accumulates what close-or-triage needs across polls: the startup-race
// seenClaimed latch and the comment-count baseline (first successful read).
type Tracker struct {
	SeenClaimed bool
	haveBase    bool
	baseComment int
}

// Done reports whether the bead has completed under mode c, given the latest
// observation. ok=false means the read failed (transient bd hiccup): never done.
// For close-only / close-or-handback this is exactly DoneSignal. For
// close-or-triage (pg2-2grpj) the triager never claims its bead, so besides
// closed / hand-back it is also done when the `escalated` label is gone
// (Escalate: human added, escalated removed) or the comment count grew since the
// first read (Triage: comment appended, bead left escalated). For
// close-or-split-triage (pg2-47rsh) the bead is done on close or once
// needs-split-review is gone (split or not-splittable both remove it); a
// hand-back is not an outcome there. sessionEnded is read by close-or-release
// only (INV-CCH-28): there the session must have ended, and an unassigned open
// bead counts as handed back even without the latch (a restarted handler lost
// it) when it carries human, a future defer_until or an open blocker.
func (t *Tracker) Done(c roles.Completion, obs Observation, ok bool, sessionEnded bool) bool {
	if !ok {
		return DoneSignal(c, "", t.SeenClaimed, "", sessionEnded)
	}
	if DoneSignal(c, obs.Status, t.SeenClaimed, obs.Assignee, sessionEnded) {
		return true
	}
	if c == roles.CloseOrRelease {
		return sessionEnded && obs.Assignee == "" && obs.Status == "open" && releaseMark(obs)
	}
	if c == roles.CloseOrSplitTriage {
		return !slices.Contains(obs.Labels, beads.LabelNeedsSplitReview)
	}
	if c != roles.CloseOrTriage {
		return false
	}
	if !t.haveBase {
		t.haveBase, t.baseComment = true, obs.Comments
	}
	return !slices.Contains(obs.Labels, EscalatedLabel) || obs.Comments > t.baseComment
}

// releaseMark reports whether an open, unassigned bead carries a mark that
// only a worker's own release leaves: human (park), a future defer_until
// (DEFER-ON-EVENT) or an open blocker (CONVERT).
func releaseMark(obs Observation) bool {
	return slices.Contains(obs.Labels, beads.LabelHuman) || obs.FutureDefer || obs.Blocked
}

// Observe records the claim latch for a read (call after Done returned false).
// For close-or-release the latch is set only when the bead is held by actor, the
// role's own actor: a peer's claim must not make the handler treat the peer's
// bead as its worker's (INV-CCH-28). Close-or-handback and close-or-triage latch
// on any in_progress read, as before.
func (t *Tracker) Observe(c roles.Completion, obs Observation, actor string) {
	if obs.Status != "in_progress" {
		return
	}
	switch c {
	case roles.CloseOrHandback, roles.CloseOrTriage:
		t.SeenClaimed = true
	case roles.CloseOrRelease:
		if actor != "" && obs.Assignee == actor {
			t.SeenClaimed = true
		}
	}
}

// End is how a close-or-release session that has ended and was not Done is
// classified (INV-CCH-28's matrix).
type End int

const (
	// EndFailed is an end the role's on_failure handles, as in every other mode
	// (the session died holding its claim, or the bead is in a status the mode
	// does not recognise, or could not be read).
	EndFailed End = iota
	// EndPeer is a bead held by an actor other than the role's own: no bead
	// write, no strike.
	EndPeer
	// EndUnclaimed is a session that ended without claiming its bead and
	// without any mark that it gave the bead back: two-strike escalation.
	EndUnclaimed
)

// EndKind classifies the end of a close-or-release session that Done did not
// accept (the caller has already checked Done with sessionEnded true). Any other
// mode, or an unreadable bead, is EndFailed.
func (t *Tracker) EndKind(c roles.Completion, obs Observation, ok bool, actor string) End {
	if c != roles.CloseOrRelease || !ok {
		return EndFailed
	}
	switch obs.Status {
	case "in_progress", "open", StatusDeferred:
	default:
		return EndFailed
	}
	if obs.Assignee != "" && obs.Assignee == actor {
		return EndFailed
	}
	if obs.Assignee != "" || obs.Status == "in_progress" {
		return EndPeer
	}
	// open or deferred, unassigned, not Done: an open bead with no latch and no
	// release mark (a deferred one is always Done).
	if obs.Status == "open" && !t.SeenClaimed && !releaseMark(obs) {
		return EndUnclaimed
	}
	return EndFailed
}

// OnFailure applies the configured failure action:
//   - add-human: add the `human` label, never unclaim (a dead worker may hold a
//     half-built worktree; blind retry is unsafe).
//   - unclaim:   status=open, assignee cleared, so the next pass retries.
//
// This is the completion-policy write-back half of this module's own
// INTF-CCH-BEADS boundary crossing (docs/behavior/interfaces.md) — a handler
// session's completion policy writing a result back to bd.
func OnFailure(ctx context.Context, br beads.Runner, action roles.FailureAction, beadID string) error {
	if action == roles.AddHuman {
		return beads.AddHuman(ctx, br, beadID)
	}
	return beads.Unclaim(ctx, br, beadID)
}

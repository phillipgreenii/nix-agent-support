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

// DoneSignal reports whether a bead has completed for the given completion mode.
//   - close-only:        done iff status == "closed".
//   - close-or-handback: done iff "closed", OR (seenClaimed && "open") — the
//     seenClaimed guard prevents a freshly-dispatched, not-yet-claimed "open" bead
//     from being mistaken for a hand-back (the startup race).
func DoneSignal(c roles.Completion, status string, seenClaimed bool) bool {
	if status == "closed" {
		return true
	}
	if (c == roles.CloseOrHandback || c == roles.CloseOrTriage) && seenClaimed && status == "open" {
		return true
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
// first read (Triage: comment appended, bead left escalated).
func (t *Tracker) Done(c roles.Completion, obs Observation, ok bool) bool {
	if !ok {
		return DoneSignal(c, "", t.SeenClaimed)
	}
	if DoneSignal(c, obs.Status, t.SeenClaimed) {
		return true
	}
	if c != roles.CloseOrTriage {
		return false
	}
	if !t.haveBase {
		t.haveBase, t.baseComment = true, obs.Comments
	}
	return !slices.Contains(obs.Labels, EscalatedLabel) || obs.Comments > t.baseComment
}

// Observe records the claim latch for a read (call after Done returned false).
func (t *Tracker) Observe(c roles.Completion, status string) {
	if (c == roles.CloseOrHandback || c == roles.CloseOrTriage) && status == "in_progress" {
		t.SeenClaimed = true
	}
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

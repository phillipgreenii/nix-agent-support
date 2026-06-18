// Package complete holds pr-pool's completion semantics: when a dispatched bead
// counts as done, and what to do when it fails. The polling loop lives in the
// orchestrator; this package is the pure decision + the failure side effects.
package complete

import (
	"context"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/roles"
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
	if c == roles.CloseOrHandback && seenClaimed && status == "open" {
		return true
	}
	return false
}

// OnFailure applies the configured failure action:
//   - add-human: add the `human` label, never unclaim (a dead worker may hold a
//     half-built worktree; blind retry is unsafe).
//   - unclaim:   status=open, assignee cleared, so the next pass retries.
func OnFailure(ctx context.Context, br beads.Runner, action roles.FailureAction, beadID string) error {
	if action == roles.AddHuman {
		return beads.AddHuman(ctx, br, beadID)
	}
	return beads.Unclaim(ctx, br, beadID)
}

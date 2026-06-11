// Package complete holds pr-pool's completion semantics: when a dispatched bead
// counts as done, and what to do when it fails. The polling loop lives in the
// orchestrator; this package is the pure decision + the failure side effects.
package complete

import (
	"context"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// DoneSignal reports whether a bead has completed for the given role.
//   - feedback: done iff status == "closed".
//   - worker:   done iff status == "closed", OR (seenClaimed && status == "open")
//     — the seenClaimed guard prevents a freshly-dispatched, not-yet-claimed
//     "open" bead from being mistaken for a hand-back (the startup race).
func DoneSignal(kind roles.RoleKind, status string, seenClaimed bool) bool {
	if status == "closed" {
		return true
	}
	if kind == roles.Worker && seenClaimed && status == "open" {
		return true
	}
	return false
}

// OnFailure applies the role-specific failure action:
//   - worker:   add the `human` label, NEVER unclaim (a dead worker may hold a
//     half-built worktree; blind retry is unsafe).
//   - feedback: unclaim (status=open, assignee cleared) so the next pass retries.
func OnFailure(ctx context.Context, br beads.Runner, role roles.Role, beadID string) error {
	if role.Kind == roles.Worker {
		return beads.AddHuman(ctx, br, beadID)
	}
	return beads.Unclaim(ctx, br, beadID)
}

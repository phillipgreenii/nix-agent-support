package sync

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// ApplyWIP implements the WIP toggle's immediate provider-facing effect
// (pg2-4dz88.4.4: the ready<->draft conversions the WIP flag drives).
//
// Toggling WIP ON: when the PR is currently READY (open, not draft), this
// converts it to draft by calling SetDraft(true) against the provider
// exactly once, so the next observation reads it as draft. This is the
// "ready -> draft" direction, and it is deliberately the ONLY call site
// permitted to take it — a ready PR must never regress to draft for any
// other reason (not a CI failure, not a bot verdict flip, not a merge
// conflict; operator ruling, pg2-4dz88.4).
//
// Toggling WIP OFF never calls anything upstream: the eventual return to
// ready is the rebuilt draft-promotion predicate's job on its next
// evaluation (sibling leaf pg2-4dz88.4.5), not an immediate effect of the
// toggle. The store-only WIP flag itself is set/cleared by the caller via
// store.DB.SetWIP; ApplyWIP only drives the provider-facing side effect.
//
// The caller (the `pg-pr pr wip on/off` CLI command) is responsible for
// persisting the flag via store.DB.SetWIP; ApplyWIP has no store dependency
// and never reads or writes it.
//
// Returns whether it actually called SetDraft(true), so callers can report
// accurately. No upstream call is made, and (false, nil) is returned, when:
//   - wip is false (the OFF direction, described above).
//   - the PR is already draft — nothing to convert.
//   - the PR is not currently open (merged or closed) — a merged/closed
//     PR's provider state is never written.
func ApplyWIP(ctx context.Context, provider DraftToggler, repo string, pr api.PR, wip bool) (bool, error) {
	if !wip || stateForPR(pr) != "open" {
		return false, nil
	}
	if err := provider.SetDraft(ctx, repo, pr.Number, true); err != nil {
		return false, fmt.Errorf("set-draft=true (wip-on): %w", err)
	}
	return true, nil
}

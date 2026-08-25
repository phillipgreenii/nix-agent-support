package sync

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// draftPromoteBlockedByConflict reports whether maybePromoteDraft's
// merge-conflict gate blocks promotion of this draft PR.
//
// Merge state (Mergeable / MergeStateStatus) is populated ONLY by the GraphQL
// enrichment path (overlayMergeState, refresh.go; enricher.PR at the source).
// This function reads it straight from enriched.PR rather than trusting the
// observed api.PR the caller overlaid it onto — both are equal by the time
// maybePromoteDraft runs (both call sites apply overlayMergeState first), but
// reading enriched.PR directly makes this gate self-contained and testable
// without depending on that overlay step having already run.
//
// Fail-closed per an explicit, binding operator ruling (pg2-4dz88.4.5): when
// merge state is UNKNOWN — enrichment never ran (enriched == nil) OR GitHub
// itself has not yet resolved mergeability (enriched.PR.Mergeable ==
// "UNKNOWN") — promotion is blocked, treating unknown as "conflict present",
// never as "no conflict". This is the OPPOSITE default from
// api.PR.HasConflict()'s own doc comment, which deliberately treats UNKNOWN
// as "not a conflict" for the unrelated dashboard/attention surface
// (internal/snapshot). The two functions are allowed to disagree on UNKNOWN
// because they answer different questions: HasConflict asks "should the
// dashboard flag this PR for attention", while this gate asks "is it safe to
// make an upstream write on the author's behalf" — and an upstream write is
// the one place this leaf's ruling requires the conservative answer.
func draftPromoteBlockedByConflict(enriched *vcs.EnrichedPR) bool {
	if enriched == nil || enriched.PR.Mergeable == "UNKNOWN" {
		return true
	}
	return enriched.PR.HasConflict()
}

// draftPromoteBlockedByWIPOrBotVerdict reports whether the store-backed WIP
// suppression flag, or a current (non-stale) bot-verdict-withheld approval
// from an allowlisted login, blocks promotion of this draft PR.
//
// Both signals are store-only and store-OPTIONAL, and both degrade to "not
// blocked" (permissive) on any of: a nil Store, a PR row the store has never
// observed, or a store read error. This mirrors the established
// store-optional convention elsewhere in this package (see
// cmd/pg-pr/review.go's selfDraftWIP doc: "a PR pg-pr has never observed ...
// reads as WIP=false, never an error") rather than the merge-conflict gate's
// deliberate fail-closed rule above, which is scoped narrowly to unknown
// MERGE STATE by its own operator ruling and does not generalize to every
// store read in this function.
//
// WIP is read from the authoritative pull_request row (store.PullRequest.WIP,
// set via store.DB.SetWIP — see pkg's doc on the pg2-4dz88.4 "WIP semantics").
//
// The bot-verdict check reuses the SAME per-approver source and allowlist
// gate as internal/sync/approver.go's botVerdictApprovals/approverApprovalState
// (cfg.ApproverAllowlist) and internal/snapshot/builder.go's classifyApprovals
// (State + Approval.IsStale against the PR's CURRENT head) — it does not
// reimplement verdict classification, it only reads the rows that mechanism
// already wrote via store.DB.SetApproval. A row counts as a bot disapproval
// iff its Approver is allowlisted, its State is "changes-requested" (the
// store's mapping for Authority Withheld — approverApprovalState's doc), and
// it is NOT stale for the PR's current HeadSHA.
func (e *Engine) draftPromoteBlockedByWIPOrBotVerdict(ctx context.Context, repo string, pr api.PR) bool {
	if e.deps.Store == nil {
		return false
	}
	row, err := e.deps.Store.GetPR(ctx, repo, pr.Number)
	if err != nil || row == nil {
		return false
	}
	if row.WIP {
		return true
	}
	allowlist := approverAllowlistSet(e.cfg().ApproverAllowlist)
	if len(allowlist) == 0 {
		return false
	}
	approvals, err := e.deps.Store.ListApprovals(ctx, row.ID)
	if err != nil {
		return false
	}
	for _, a := range approvals {
		if allowlist[a.Approver] && a.State == "changes-requested" && !a.IsStale(pr.HeadSHA) {
			return true
		}
	}
	return false
}

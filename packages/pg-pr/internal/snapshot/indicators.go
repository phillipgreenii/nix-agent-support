package snapshot

import "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"

// This file holds the shared computations behind the per-row indicator set
// the operator requested (2026-08-24, via pg2-4dz88.7.1, carried as
// pg2-4dz88.7.5): build state, bot-verdict tri-state, and the self-review
// (approval/comment) facts. Each is a pure function of already-available
// inputs — CIStatus, the per-approver store.Approval rows, an allowlist —
// mirroring attention.go's NeedsAttention / ordering.go's CompareTeamRows
// convention of ONE exported/shared computation per concern so no consumer
// can re-derive its own diverging copy.

// Build-state indicator values: the operator's own three-valued vocabulary
// ("broken/pending/passing build").
const (
	BuildStateBroken  = "broken"
	BuildStatePending = "pending"
	BuildStatePassing = "passing"
)

// buildStateFor maps a row's already-computed CIStatus (cirollup.Compute's
// Rollup.State, with policy-bot already excluded upstream via the per-repo
// cirollup.Excluder threaded through BuilderInput.CheckInterpretersByRepo —
// this function must not add a second exclusion mechanism) onto the
// operator's three-valued vocabulary.
//
// # The "none" decision (pg2-4dz88.7.5 acceptance criterion)
//
// cirollup.Compute reports "none" when a PR carries no countable CI run at
// all — no CI has run, which is neither "CI ran and failed" nor "CI ran and
// passed". Mapped to BuildStatePending, deliberately NOT BuildStatePassing:
// panels.go's ActNow predicate already treats anything other than
// CIStatus=="success" as not yet "ready for review" ("a build that has not
// definitively passed is not yet ready for review"), so folding "none" into
// "passing" here would contradict that established meaning the moment the
// two are ever compared side by side. "No build has run yet" is closer in
// spirit to "still pending" than to a false "broken" reading, so pending is
// the honest three-valued home for it. Any other/unrecognised CIStatus value
// falls into the same default, rather than panicking or picking an
// arbitrary state.
func buildStateFor(ciStatus string) string {
	switch ciStatus {
	case "failure":
		return BuildStateBroken
	case "success":
		return BuildStatePassing
	default: // "pending", "none", or any unrecognised value
		return BuildStatePending
	}
}

// Bot-verdict tri-state values: a genuine three-state read, replacing the
// risk this bead's own description flags — a shipped-but-still-boolean
// relabeling of AgentApproved/BotDisapproved.
const (
	BotVerdictApproved    = "approved"
	BotVerdictDisapproved = "disapproved"
	BotVerdictNoDecision  = "no-decision"
)

// botVerdictFor is the ONE shared computation behind TeamRow/MineRow's
// BotVerdict field and behind the pre-existing TeamRow.BotDisapproved
// (builder.go's botDisapproved, retained as a derived, backward-compatible
// projection of this function so pg2-4dz88.7.3's ActNow/panels.go call site
// needs no change). pg2-4dz88.7.3/.7.4 read this SAME function rather than
// each deriving their own allowlist-filtered scan of p.Approvals — exactly
// the "reuse one computation, do not build three times" coordination this
// bead's description calls for.
//
// Filters approvals to ALLOWLISTED approvers ONLY (config.Config.
// ApproverAllowlist via BuilderInput.ApproverAllowlist — see
// TeamRow.BotDisapproved's doc for why this is a set DELIBERATELY SEPARATE
// from agentregistry.Registry.IsAgent) and to rows currently STANDING
// (!store.Approval.IsStale(headSHA) — INV-APPROVAL-3: a disapproval or
// approval of a superseded/dismissed head is WITHDRAWN, never counted).
//
// A standing "changes-requested" from ANY allowlisted approver reads as
// BotVerdictDisapproved outright — mirroring the pre-existing botDisapproved
// short-circuit-on-first-match behavior and ActNow's "a blocking bot verdict
// wins" posture, so a mix of one allowlisted approval and one allowlisted
// disapproval still reads as disapproved rather than picking a side by scan
// order. Absent any disapproval, a standing "approved" row from an
// allowlisted approver reads as BotVerdictApproved. Absent either — no
// allowlisted approver has a standing approved/disapproved row at all, which
// also covers a standing "commented" row, a bot comment that is neither an
// approval nor a disapproval and says nothing decisive — the PR carries no
// resolved bot verdict: BotVerdictNoDecision.
func botVerdictFor(approvals []store.Approval, allowlist map[string]struct{}, headSHA string) string {
	sawApproved := false
	for _, a := range approvals {
		if _, ok := allowlist[a.Approver]; !ok {
			continue
		}
		if a.IsStale(headSHA) {
			continue
		}
		switch a.State {
		case "changes-requested":
			return BotVerdictDisapproved
		case "approved":
			sawApproved = true
		}
	}
	if sawApproved {
		return BotVerdictApproved
	}
	return BotVerdictNoDecision
}

// Self-approval-state values: "have I approved, and is it stale".
const (
	SelfApprovalNotApproved = "not-approved"
	SelfApprovalStanding    = "approved"
	SelfApprovalStale       = "stale"
)

// selfApprovalStateFor reads approvals for the ONE row belonging to self
// (store.ListApprovals is UNIQUE(pr_id, approver), so at most one exists)
// and reports whether it is an approval and, if so, whether it still stands
// for headSHA.
//
// This is genuinely new wiring, not a re-export: NeedsAttention (attention.go)
// computes staleness INTERNALLY but conflates "I approved and it still
// stands", "a teammate approved", and "a conflict dampened it" — all three
// read as the same NeedsAttention==false, so NeedsAttention/AttentionReason
// cannot answer "is MY approval standing" on their own. Reading approvals
// directly for self's own row keeps this indicator answering exactly that
// one question, independent of what anyone else did or whether the PR has a
// conflict.
//
// self == "" cannot identify any row (mirroring NeedsAttention's own self
// handling) and always reads SelfApprovalNotApproved. A self row whose State
// is "changes-requested" or "commented" (never "approved") also reads
// SelfApprovalNotApproved — this indicator answers only the approval
// question; selfCommentedFor below answers the sibling "have I commented"
// question over the SAME row.
func selfApprovalStateFor(approvals []store.Approval, self, headSHA string) string {
	if self == "" {
		return SelfApprovalNotApproved
	}
	for _, a := range approvals {
		if a.Approver != self {
			continue
		}
		if a.State != "approved" {
			return SelfApprovalNotApproved
		}
		if a.IsStale(headSHA) {
			return SelfApprovalStale
		}
		return SelfApprovalStanding
	}
	return SelfApprovalNotApproved
}

// selfCommentedFor answers "have I commented", scoped DELIBERATELY to option
// (b) from this bead's own inventory of three choices: the PRE-EXISTING
// store.Approval{Approver: self, State: "commented"} fact that
// internal/sync/revision.go's mySubmittedReviews already writes whenever
// self's latest submitted GitHub review carries the COMMENTED disposition —
// not option (a), any top-level or inline api.Comment authored by self
// (broader, and a raw comment count, not a review disposition), nor (c),
// both.
//
// Rationale for (b): the operator listed "have I commented" immediately
// alongside "have I approved" — read together, these are peer REVIEW
// DISPOSITION facts about the SAME underlying self-review row, not a comment
// tally. pr_approval already carries exactly that disposition for self with
// no new ingest work, and (a) would risk exactly the "duplicate field under
// a different name" this bead's acceptance criteria forbid, since a self
// review submitted with a COMMENTED disposition is, definitionally, also a
// top-level comment (a) would independently rediscover.
//
// Because pr_approval is UNIQUE(pr_id, approver), self's row can read
// State=="commented" ONLY when self's LATEST submitted review is COMMENTED
// and NOT a later APPROVED/CHANGES_REQUESTED (a later review of either state
// overwrites the row in place, per store.SetApproval's doc) — so this can
// never simultaneously read true alongside SelfApprovalStanding/
// SelfApprovalStale: the two indicators read the same row but can never
// double-count the same positive signal (TestSelfCommentedDoesNotDuplicateSelfApprovalState
// pins this).
func selfCommentedFor(approvals []store.Approval, self string) bool {
	if self == "" {
		return false
	}
	for _, a := range approvals {
		if a.Approver == self {
			return a.State == "commented"
		}
	}
	return false
}

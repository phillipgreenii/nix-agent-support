package interpret

// This file is the ONE decision of who owes the next step on a PR. Ownership
// (ownership.go) decides mine, co-owned or team once; decideWaitingOn then
// names, once, who is waiting on whom and why, as a (WaitingOn, reason code)
// pair. The panel is a lookup over (ownership, waiting-on) (PanelFor) and the
// attention rules read the stored pair; neither recomputes a predicate. The
// reason-code tables are specified in docs/behavior/pg-desk/interpret.md,
// "Waiting-on: the single decision".

// WaitingOn names who owes the next step on a PR.
type WaitingOn string

const (
	// WaitingOnMe: the operator owes the next step.
	WaitingOnMe WaitingOn = "me"
	// WaitingOnAuthor: the PR author owes it (team PRs only; on an own PR the
	// author is the operator, so it collapses into WaitingOnMe).
	WaitingOnAuthor WaitingOn = "author"
	// WaitingOnOthers: other reviewers or the team owe it.
	WaitingOnOthers WaitingOn = "others"
	// WaitingOnNone: nobody owes a step pg-desk tracks (not open, a draft team
	// PR, or a team PR with no match reason).
	WaitingOnNone WaitingOn = "none"
)

// Reason codes. Evaluation stops at the first predicate that matches, so one
// code names the PR's one cause.
const (
	ReasonCIFailing         = "ci-failing"
	ReasonBotDisapproved    = "bot-disapproved"
	ReasonChangesRequested  = "changes-requested"
	ReasonConflict          = "conflict"
	ReasonOpenThread        = "open-thread"
	ReasonApprovedReady     = "approved-ready"
	ReasonOwnDraft          = "own-draft"
	ReasonAwaitingReview    = "awaiting-review"
	ReasonStaleReview       = "stale-review"
	ReasonReviewRequested   = "review-requested"
	ReasonActingReviewer    = "acting-reviewer"
	ReasonUnansweredThread  = "unanswered-thread"
	ReasonApprovedMergeBlk  = "approved-merge-blocked"
	ReasonApprovedCIPending = "approved-ci-unsettled"
	ReasonApprovedRequested = "approved-awaiting-requested"
	ReasonApprovedToMerge   = "approved-ready-to-merge"
)

// ReasonRow pairs a reason code with the WaitingOn it yields.
type ReasonRow struct {
	Reason    string
	WaitingOn WaitingOn
}

// OwnReasons is every reason code an own (mine or co-owned) open PR can carry.
var OwnReasons = []ReasonRow{
	{ReasonCIFailing, WaitingOnMe},
	{ReasonBotDisapproved, WaitingOnMe},
	{ReasonChangesRequested, WaitingOnMe},
	{ReasonConflict, WaitingOnMe},
	{ReasonOpenThread, WaitingOnMe},
	{ReasonApprovedReady, WaitingOnMe},
	{ReasonOwnDraft, WaitingOnMe},
	{ReasonAwaitingReview, WaitingOnOthers},
}

// TeamReasons is every reason code an admitted (open, non-draft, matched) team
// PR can carry.
var TeamReasons = []ReasonRow{
	{ReasonCIFailing, WaitingOnAuthor},
	{ReasonConflict, WaitingOnAuthor},
	{ReasonChangesRequested, WaitingOnAuthor},
	{ReasonStaleReview, WaitingOnMe},
	{ReasonReviewRequested, WaitingOnMe},
	{ReasonActingReviewer, WaitingOnMe},
	{ReasonBotDisapproved, WaitingOnAuthor},
	{ReasonApprovedMergeBlk, WaitingOnOthers},
	{ReasonApprovedCIPending, WaitingOnAuthor},
	{ReasonApprovedRequested, WaitingOnOthers},
	{ReasonApprovedToMerge, WaitingOnAuthor},
	{ReasonUnansweredThread, WaitingOnAuthor},
	{ReasonAwaitingReview, WaitingOnOthers},
}

// PanelFor is the panel lookup over (ownership, waiting-on). An own PR cannot
// be waiting on its author (the author is the operator), so that cell maps to
// the operator's own panel defensively.
func PanelFor(own Ownership, w WaitingOn) string {
	if own.ActsAsMine() {
		switch w {
		case WaitingOnMe, WaitingOnAuthor:
			return PanelMineAwaitingMe
		case WaitingOnOthers:
			return PanelMineAwaitingTeam
		}
		return PanelNone
	}
	switch w {
	case WaitingOnMe:
		return PanelTeamAwaitingMe
	case WaitingOnAuthor:
		return PanelTeamAwaitingOwner
	case WaitingOnOthers:
		return PanelTeamAwaitingTeam
	}
	return PanelNone
}

// decideWaitingOn is the single waiting-on decision for one entity: the first
// matching predicate of the table for its ownership names (waiting-on, reason).
// A PR that is not open, a draft team PR and a team PR with no match reason
// yield (WaitingOnNone, "").
func decideWaitingOn(own Ownership, pr prShow, ci ciRollupResult, appr Approvals, matchReasons []string) (WaitingOn, string) {
	if pr.State != "open" {
		return WaitingOnNone, ""
	}
	if own.ActsAsMine() {
		return decideOwn(pr, ci, appr)
	}
	if pr.Draft || len(matchReasons) == 0 {
		return WaitingOnNone, ""
	}
	return decideTeam(pr, ci, appr, matchReasons)
}

func decideOwn(pr prShow, ci ciRollupResult, appr Approvals) (WaitingOn, string) {
	// CI blocks on failure and on "none" (no countable run at all); a pending
	// rollup does not. ReviewState carries only the review-exempt softening.
	if s := ci.ReviewState(); s != "success" && s != "pending" {
		return WaitingOnMe, ReasonCIFailing
	}
	switch {
	case appr.BotVerdict == BotVerdictDisapproved:
		return WaitingOnMe, ReasonBotDisapproved
	case appr.HumanChangesRequested:
		return WaitingOnMe, ReasonChangesRequested
	case pr.hasConflict():
		return WaitingOnMe, ReasonConflict
	case hasUnresolvedThread(pr.allComments()):
		return WaitingOnMe, ReasonOpenThread
	case appr.HumanApproved:
		return WaitingOnMe, ReasonApprovedReady
	case pr.Draft:
		// Nobody else will flip a draft out of draft.
		return WaitingOnMe, ReasonOwnDraft
	}
	return WaitingOnOthers, ReasonAwaitingReview
}

func decideTeam(pr prShow, ci ciRollupResult, appr Approvals, matchReasons []string) (WaitingOn, string) {
	// Hard blockers: things only the PR owner can fix. ReviewerState sets
	// aside a harmless cancelled run and reads "no CI data" as pending.
	if s := ci.ReviewerState(); s != "success" && s != "pending" {
		return WaitingOnAuthor, ReasonCIFailing
	}
	if pr.hasConflict() || pr.hasStackParentConflict() {
		return WaitingOnAuthor, ReasonConflict
	}
	if blockingChangesRequested(appr) {
		return WaitingOnAuthor, ReasonChangesRequested
	}
	// A review thread the operator took part in that the author has not
	// answered is the author's debt, whatever else stands: there is nothing new
	// to re-review until the author answers (operator ruling 2026-11-06). It
	// precedes the stale-review, review-requested, acting-reviewer, bot and
	// approved rows and is not gated on SelfReviewStale.
	if appr.SelfUnansweredThread {
		return WaitingOnAuthor, ReasonUnansweredThread
	}
	// The PR is approvable and the operator has no unanswered thread of their
	// own. A review of the operator's own that a push made stale is a
	// re-review the operator owes, whatever else stands.
	if appr.SelfReviewStale {
		return WaitingOnMe, ReasonStaleReview
	}
	if containsString(matchReasons, MatchReasonReviewRequested) {
		return WaitingOnMe, ReasonReviewRequested
	}
	if appr.SelfReviewInProgress {
		return WaitingOnMe, ReasonActingReviewer
	}
	if appr.BotVerdict == BotVerdictDisapproved {
		return WaitingOnAuthor, ReasonBotDisapproved
	}
	if appr.HumanApproved {
		return teamApprovedDecision(pr, ci)
	}
	if hasUnansweredReviewThread(pr) {
		return WaitingOnAuthor, ReasonUnansweredThread
	}
	return WaitingOnOthers, ReasonAwaitingReview
}

// blockingChangesRequested reports whether a CHANGES_REQUESTED review is a
// hard blocker on a team PR: any non-bot reviewer's, except the operator's own
// when it is stale (made against an older head), which routes to the operator
// for re-review instead (operator ruling 2026-11-03).
func blockingChangesRequested(appr Approvals) bool {
	if !appr.HumanChangesRequested {
		return false
	}
	return appr.OtherChangesRequested || !appr.SelfChangesRequestedStale
}

// teamApprovedDecision decides an approved, unblocked team PR the operator is
// not asked to review, by GitHub's merge state (see the behavior doc, team
// predicate 9).
func teamApprovedDecision(pr prShow, ci ciRollupResult) (WaitingOn, string) {
	switch pr.MergeStateStatus {
	case "BLOCKED":
		if ci.RunsInFlight() || ci.State == "unknown" || ci.State == "failure" {
			return WaitingOnAuthor, ReasonApprovedCIPending
		}
		return WaitingOnOthers, ReasonApprovedMergeBlk
	case "", "UNKNOWN":
		if len(pr.ReviewRequests) > 0 {
			return WaitingOnOthers, ReasonApprovedRequested
		}
		return WaitingOnAuthor, ReasonApprovedToMerge
	}
	return WaitingOnAuthor, ReasonApprovedToMerge
}

func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

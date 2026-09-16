package interpret

import "encoding/json"

// Bot-verdict tri-state values. Ported from
// packages/pg-pr/internal/snapshot/indicators.go's BotVerdictApproved/
// BotVerdictDisapproved/BotVerdictNoDecision.
const (
	BotVerdictApproved    = "approved"
	BotVerdictDisapproved = "disapproved"
	BotVerdictNoDecision  = "no-decision"
)

// computeApprovals derives Approvals from the PR's current review set
// (pr.Reviews). HumanApprovers/HumanApproved count every DISTINCT login with
// a currently APPROVED review — every reviewer counts as human, since this
// packet has no agent registry input (see interpret.go's package doc);
// BotVerdict is a SEPARATE, overlapping read restricted to
// approver_allowlist logins, mirroring
// packages/pg-pr/internal/snapshot/indicators.go's botVerdictFor: a standing
// CHANGES_REQUESTED from any allowlisted login wins outright (disapproved);
// otherwise an allowlisted APPROVED reads BotVerdictApproved; otherwise
// BotVerdictNoDecision. WaitingOnMe is populated by interpret.go's caller
// (computeWaitingOnMe), not here.
//
// No staleness axis: schema.PRReview carries no head-SHA-at-review field, so
// unlike pg-pr's store.Approval.IsStale (INV-APPROVAL-3), every review is
// treated as standing for the PR's CURRENT state — a documented deviation
// (see interpret.go's package doc).
func computeApprovals(pr prShow, approverAllowlist []string) Approvals {
	approvers := map[string]struct{}{}
	for _, r := range pr.Reviews {
		if r.State == "APPROVED" {
			approvers[r.Author] = struct{}{}
		}
	}

	allow := toSet(approverAllowlist)
	disapproved := false
	approvedByAllowlisted := false
	for _, r := range pr.Reviews {
		if _, ok := allow[r.Author]; !ok {
			continue
		}
		switch r.State {
		case "CHANGES_REQUESTED":
			disapproved = true
		case "APPROVED":
			approvedByAllowlisted = true
		}
	}
	verdict := BotVerdictNoDecision
	switch {
	case disapproved:
		verdict = BotVerdictDisapproved
	case approvedByAllowlisted:
		verdict = BotVerdictApproved
	}

	return Approvals{
		HumanApprovers: len(approvers),
		HumanApproved:  len(approvers) > 0,
		BotVerdict:     verdict,
	}
}

// depsEntity is the minimal subset of an `issue deps --full` entity this
// package decodes: State and Labels, to evaluate the same "human"-label
// predicate pkg/beads.AllNonClosedHumanLabeled applies.
type depsEntity struct {
	State  string   `json:"state"`
	Labels []string `json:"labels"`
}

type depsResult struct {
	Entities []depsEntity `json:"entities"`
}

// computeWaitingOnMe ports pkg/beads.AllNonClosedHumanLabeled: true iff
// there is at least one non-closed dependency AND every non-closed
// dependency carries the `human` label. An empty/malformed payload (no
// matched work bead at all — gather.Facts.Deps' own doc: "Empty when no
// work bead matched this PR at all") degrades to false, matching
// AllNonClosedHumanLabeled's own "an empty non-closed set returns false"
// rule.
func computeWaitingOnMe(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var d depsResult
	if err := json.Unmarshal(raw, &d); err != nil {
		return false
	}
	anyOpen := false
	for _, e := range d.Entities {
		if e.State == "closed" {
			continue
		}
		anyOpen = true
		if !hasLabel(e.Labels, "human") {
			return false
		}
	}
	return anyOpen
}

// --- match reasons -----------------------------------------------------
// Recomputed from v3 facts per the design's Interpret bullet, verbatim list:
// author in team_members; review_requests contains self; a review by self
// exists; labels intersects watch_labels.

const (
	MatchReasonTeamAuthored    = "team-authored"
	MatchReasonReviewRequested = "review-requested"
	MatchReasonReviewedByMe    = "reviewed-by-me"
	MatchReasonLabelPrefix     = "label:"
)

// selfSubmittedReviewStates mirrors
// packages/pg-pr/internal/snapshot/builder.go's selfSubmittedReviewStates
// (DISMISSED/PENDING deliberately absent — see that file's doc; not
// representable here anyway, since schema.PRReview carries no dismissal
// state).
var selfSubmittedReviewStates = map[string]bool{
	"APPROVED": true, "CHANGES_REQUESTED": true, "COMMENTED": true,
}

func computeMatchReasons(pr prShow, teamMembers, watchLabels []string, self string) []string {
	var reasons []string

	team := toSet(teamMembers)
	if _, ok := team[pr.Author]; ok {
		reasons = append(reasons, MatchReasonTeamAuthored)
	}

	if self != "" {
		for _, rr := range pr.ReviewRequests {
			if rr == self {
				reasons = append(reasons, MatchReasonReviewRequested)
				break
			}
		}
		for _, r := range pr.Reviews {
			if r.Author == self && selfSubmittedReviewStates[r.State] {
				reasons = append(reasons, MatchReasonReviewedByMe)
				break
			}
		}
	}

	if len(watchLabels) > 0 {
		watch := toSet(watchLabels)
		for _, l := range pr.Labels {
			if _, ok := watch[l]; ok {
				reasons = append(reasons, MatchReasonLabelPrefix+l)
			}
		}
	}

	return reasons
}

// --- panel placement -----------------------------------------------------
// Adapted from packages/pg-pr/internal/snapshot's mine_panels.go/panels.go
// (ClassifyMine/ActNow), minus the clauses that need data Facts cannot
// carry: WIPReadyForPromotion (WIP is a packet-8 read-time join) and any
// staleness-dependent clause. See interpret.go's package doc for the full
// list of documented deviations.

// openConversationOnly ports mine_panels.go's OpenConversationOnly exactly
// (module-level free function there; a private helper here, over prComment
// instead of api.Comment).
func openConversationOnly(humanApproved bool, mergeStateStatus, ciState string, hasConflict bool, comments []prComment) bool {
	if !humanApproved {
		return false
	}
	if mergeStateStatus == "" || mergeStateStatus == "CLEAN" {
		return false
	}
	if ciState != "success" {
		return false
	}
	if hasConflict {
		return false
	}
	for _, c := range comments {
		if c.ThreadID != "" && !c.Resolved {
			return true
		}
	}
	return false
}

// classifyPanel places one entity into exactly one of the five named panels,
// or PanelNone when it is not currently admitted to any (a merged PR of
// mine, or a draft/reasonless team PR — mirroring pg-pr's silently-dropped
// DroppedCount branch, internal/snapshot/builder.go's Build).
func classifyPanel(own Ownership, pr prShow, ci ciRollupResult, appr Approvals, matchReasons []string) string {
	if pr.Merged {
		// A merged PR is retained in the human view's "Mine" list, but is no
		// longer "in flight" in any of the three mine-panel senses
		// (builder.go: three-view membership is "computed for every ACTIVE
		// (non-merged) mine/co-owned row, never for a retained merged row").
		return PanelNone
	}

	hasConflict := pr.hasConflict()
	botDisapproved := appr.BotVerdict == BotVerdictDisapproved

	if own.ActsAsMine() {
		ciRed := ci.State == "failure"
		openConvo := openConversationOnly(appr.HumanApproved, pr.MergeStateStatus, ci.State, hasConflict, pr.allComments())
		actNow := hasConflict || botDisapproved || ciRed || openConvo
		switch {
		case actNow:
			return PanelMineActNow
		case appr.HumanApproved && pr.MergeStateStatus == "CLEAN" && ci.State == "pending":
			return PanelMineAwaitingOtherThings
		default:
			return PanelMineAwaitingOthers
		}
	}

	// Team: admitted only when non-draft and carrying at least one live
	// match reason (builder.go's admission switch: "!p.PR.Draft &&
	// len(reasons) > 0"). Otherwise it is not in the review set at all.
	if pr.Draft || len(matchReasons) == 0 {
		return PanelNone
	}
	if ci.State == "success" && !botDisapproved && !hasConflict {
		return PanelTeamActNow
	}
	return PanelTeamBlocked
}

// --- ready-to-promote (D15) ------------------------------------------------

// computeReadyToPromote evaluates D15's promotion predicate (own PR, not
// co-owned, draft, checks green, no bot disapproval, no merge conflict) —
// section 7.5's "Recorded losses" bullet, cited verbatim in this packet's
// Binding decisions. The predicate's "not WIP" clause is OMITTED: WIP is an
// annotation-table, packet-8 read-time join this package cannot see (design:
// "Hidden and WIP are NOT interpreted"); packet 8's `open --promotable` is
// expected to combine THIS flag with the WIP annotation at read time.
func computeReadyToPromote(own Ownership, pr prShow, ci ciRollupResult, appr Approvals) bool {
	if own != OwnershipMine {
		return false
	}
	if !pr.Draft {
		return false
	}
	if pr.hasConflict() {
		return false
	}
	if ci.State != "success" {
		return false
	}
	if appr.BotVerdict == BotVerdictDisapproved {
		return false
	}
	return true
}

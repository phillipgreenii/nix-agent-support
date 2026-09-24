package interpret

import (
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/verdict"
)

// Bot-verdict tri-state values. Ported from
// packages/pg-pr/internal/snapshot/indicators.go's BotVerdictApproved/
// BotVerdictDisapproved/BotVerdictNoDecision.
const (
	BotVerdictApproved    = "approved"
	BotVerdictDisapproved = "disapproved"
	BotVerdictNoDecision  = "no-decision"
)

// computeApprovals derives Approvals from the PR's current review set
// (pr.Reviews) and, when verdictClassifier is non-nil, its comments. It
// merges two independent bot-verdict signals — see this function's own
// "two independent signals" note below for why a Reviews-only read is
// insufficient for a real bot observed in the wild (zr-review-bot, pg2-2j5ac):
//
//   - Review-state signal (approver_allowlist logins' pr.Reviews[].State):
//     a standing CHANGES_REQUESTED from any allowlisted login wins outright
//     (disapproved); otherwise an allowlisted APPROVED reads
//     BotVerdictApproved. Mirrors packages/pg-pr/internal/snapshot/
//     indicators.go's botVerdictFor.
//   - Comment-verdict-grammar signal (verdictClassifier over pr.Comments +
//     every review-thread comment, packages/pg-desk/internal/verdict): some
//     bots report their real verdict in a structured comment body rather
//     than (or in addition to) a matching review State — observed for
//     zr-review-bot, which posts a COMMENTED review (not CHANGES_REQUESTED)
//     alongside a separate marker-carrying issue comment when it finds
//     problems, and only uses a formal APPROVED review when clean. Authority
//     Approved reads BotVerdictApproved; Authority Withheld (findings
//     Problems, or a Clean-but-not-approved reading) reads
//     BotVerdictDisapproved; Pending/Absent contribute nothing.
//
// Two independent signals, since neither one alone is a complete account of
// every bot's real behavior: a bot that only ever emits Review objects needs
// nothing from the comment grammar (verdictClassifier may be nil), and a bot
// whose disapproval never surfaces as a review State needs the comment
// grammar to be seen at all. Across BOTH signals, disapproved wins outright
// over approved (never silently overridden by an approval elsewhere), exactly
// mirroring the single-signal precedence the Review-state-only read already
// had. HumanApprovers/HumanApproved count every DISTINCT login with a
// currently APPROVED review — every reviewer counts as human, since this
// packet has no agent registry input (see interpret.go's package doc).
// WaitingOnMe is populated by interpret.go's caller (computeWaitingOnMe), not
// here.
//
// No staleness axis: schema.PRReview carries no head-SHA-at-review field, so
// unlike pg-pr's store.Approval.IsStale (INV-APPROVAL-3), every review is
// treated as standing for the PR's CURRENT state — a documented deviation
// (see interpret.go's package doc).
func computeApprovals(pr prShow, approverAllowlist []string, verdictClassifier *verdict.Classifier) Approvals {
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

	if commentAuthority := classifyCommentVerdict(pr, verdictClassifier); commentAuthority == verdict.Withheld {
		disapproved = true
	} else if commentAuthority == verdict.Approved {
		approvedByAllowlisted = true
	}

	botVerdict := BotVerdictNoDecision
	switch {
	case disapproved:
		botVerdict = BotVerdictDisapproved
	case approvedByAllowlisted:
		botVerdict = BotVerdictApproved
	}

	return Approvals{
		HumanApprovers: len(approvers),
		HumanApproved:  len(approvers) > 0,
		BotVerdict:     botVerdict,
	}
}

// classifyCommentVerdict returns the comment-verdict-grammar's Authority
// reading across every comment on pr (top-level and review-thread), or
// verdict.Absent when verdictClassifier is nil (no generations configured —
// the comment-grammar signal is opt-in via config.VerdictGenerations) or no
// comment carries a configured BodyMarker at all.
//
// When more than one comment carries a matching marker (a bot re-posting its
// overview comment across pushes), the LAST one wins — pr.allComments()
// preserves the host's chronological order, so this reads as "the bot's most
// recent verdict comment," mirroring Classify's own "highest declared
// generation wins" precedent applied across comments instead of generations.
// Only a DEFINITE reading (Findings Clean or Problems) can win; a comment
// that only reaches Pending/Absent never overrides an earlier definite one.
func classifyCommentVerdict(pr prShow, verdictClassifier *verdict.Classifier) verdict.Authority {
	if verdictClassifier == nil {
		return verdict.Absent
	}
	best := verdict.Absent
	for _, c := range pr.allComments() {
		if c.Body == "" {
			continue
		}
		result := verdictClassifier.Classify(c.Body)
		if result.Findings == verdict.FindingsUnknown {
			continue
		}
		best = result.Authority
	}
	return best
}

// buildVerdictClassifier converts config's flat []config.VerdictGeneration
// into []verdict.Generation and compiles it, mirroring urgency.go's
// compileExcluder precedent: "a mis-configured pattern must not break
// interpretation." An empty gens slice, or a compile failure (a bad regex in
// one of the configured generations), degrades to verdict.New(nil) — a valid
// classifier that reads every body Absent (verdict.New's own documented
// "zero configured generations" behavior) — rather than propagating an
// error out of Interpret for what is, today, an entirely optional, opt-in
// signal.
func buildVerdictClassifier(gens []config.VerdictGeneration) *verdict.Classifier {
	converted := make([]verdict.Generation, 0, len(gens))
	for _, g := range gens {
		converted = append(converted, verdict.Generation{
			ID:                g.ID,
			BodyMarker:        g.BodyMarker,
			FindingsPatterns:  g.FindingsPatterns,
			AuthorityPatterns: g.AuthorityPatterns,
		})
	}
	c, err := verdict.New(converted)
	if err != nil {
		c, _ = verdict.New(nil)
	}
	return c
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

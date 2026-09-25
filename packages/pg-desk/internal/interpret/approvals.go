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

const (
	PanelTeamAwaitingOwner = "team_awaiting_owner"
	PanelTeamAwaitingTeam  = "team_awaiting_team"
	PanelTeamAwaitingMe    = "team_awaiting_me"
	PanelMineAwaitingMe    = "mine_awaiting_me"
	PanelMineAwaitingTeam  = "mine_awaiting_team"
	PanelNone              = ""
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
// No PER-COMMIT staleness axis: schema.PRReview carries no head-SHA-at-review
// field, so unlike pg-pr's store.Approval.IsStale (INV-APPROVAL-3), a review
// is never invalidated by a LATER COMMIT landing with no new review from
// anyone — a documented deviation (see interpret.go's package doc). This
// function DOES collapse pr.Reviews to each author's latest decisive verdict
// (see latestDecision inside computeApprovals), which fixes the OTHER
// staleness axis — a review superseded by a LATER REVIEW FROM THE SAME
// AUTHOR (e.g. CHANGES_REQUESTED followed by that same author's own
// APPROVED) no longer reads as standing. The two axes are independent; only
// the per-commit one remains unaddressed.
//
// SelfApproved and HumanChangesRequested are computed in the SAME pass as
// HumanApprovers/HumanApproved (one loop over pr.Reviews) rather than as
// separate helper functions, since all four read the identical
// State/Author fields — splitting them would mean re-walking pr.Reviews
// for no benefit. HumanChangesRequested excludes approverAllowlist logins
// deliberately: a bot's disapproval is already BotVerdict's concern, and
// double-carrying it here would let one bot rejection present as two
// independent signals to classifyPanel.
func computeApprovals(pr prShow, self string, approverAllowlist []string, verdictClassifier *verdict.Classifier) Approvals {
	allow := toSet(approverAllowlist)

	// latestDecision collapses pr.Reviews (a full chronological history,
	// per pg-connector-pr-github's ListReviews) to each author's most
	// recent APPROVED/CHANGES_REQUESTED verdict. A COMMENTED (or any other)
	// review never overwrites an earlier decisive one, mirroring
	// pg-connector-pr-github's own needsAttentionForPR collapse
	// (internal/provider.go) and GitHub's own reviewDecision semantics.
	// Without this, a reviewer who requested changes and was later
	// satisfied — an ordinary review cycle — would read as "still
	// requesting changes" forever, since pr.Reviews carries every
	// historical event, not just the live one.
	latestDecision := map[string]string{}
	for _, r := range pr.Reviews {
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED":
			latestDecision[r.Author] = r.State
		}
	}

	approvers := map[string]struct{}{}
	selfApproved := false
	humanChangesRequested := false
	disapproved := false
	approvedByAllowlisted := false
	for author, state := range latestDecision {
		_, isBot := allow[author]
		switch state {
		case "APPROVED":
			approvers[author] = struct{}{}
			if self != "" && author == self {
				selfApproved = true
			}
			if isBot {
				approvedByAllowlisted = true
			}
		case "CHANGES_REQUESTED":
			if isBot {
				disapproved = true
			} else {
				humanChangesRequested = true
			}
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
		HumanApprovers:        len(approvers),
		HumanApproved:         len(approvers) > 0,
		SelfApproved:          selfApproved,
		HumanChangesRequested: humanChangesRequested,
		BotVerdict:            botVerdict,
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

// classifyPanel places one entity into exactly one of the five named
// panels, or PanelNone when it is not currently in flight at all (not
// open, a draft/reasonless team PR).
//
// Operator ruling, 2026-09-25 (superseding the earlier act-now/blocked
// taxonomy this function carried through Phase 9-13): "Act Now" conflated
// "nothing is stopping you from looking at this" with "this needs YOUR
// action," which was misleading — a team PR already carrying two human
// approvals and a bot approval showed as Act Now solely because it matched
// a watch label, with nothing left for the operator to actually do.
//
//   - blocked (ci not green, bot disapproved, a real human
//     CHANGES_REQUESTED, or a merge conflict) always resolves to
//     team_awaiting_owner / mine_awaiting_me FIRST, before any
//     assignment/approval check — see this file's TestClassifyPanel
//     "blocked wins even if I'm assigned and already approved".
//   - Team, once not blocked: if I'm a requested reviewer
//     (MatchReasonReviewRequested) and haven't approved yet ->
//     team_awaiting_me; if I have -> team_awaiting_owner (the ball is back
//     with the PR's owner/other reviewers). If I'm not requested: any
//     existing human approval -> team_awaiting_owner, otherwise ->
//     team_awaiting_team.
//   - Mine, once not blocked: any unresolved review-thread comment ->
//     mine_awaiting_me (no author qualifier — any open thread is on me).
//     Otherwise, already having a human approval -> mine_awaiting_me
//     (nothing left to do but merge). Otherwise -> mine_awaiting_team.
//
// No staleness axis (package doc): "approved" here means "a currently
// APPROVED review exists," not "a non-stale one" — pg-desk has no
// per-review head-SHA history to tell the two apart yet.
func classifyPanel(own Ownership, pr prShow, ci ciRollupResult, appr Approvals, matchReasons []string) string {
	if pr.State != "open" {
		return PanelNone
	}

	blocked := ci.State != "success" ||
		appr.BotVerdict == BotVerdictDisapproved ||
		appr.HumanChangesRequested ||
		pr.hasConflict()

	if own.ActsAsMine() {
		switch {
		case blocked:
			return PanelMineAwaitingMe
		case hasUnresolvedThread(pr.allComments()):
			return PanelMineAwaitingMe
		case appr.HumanApproved:
			return PanelMineAwaitingMe
		default:
			return PanelMineAwaitingTeam
		}
	}

	// Team: admitted only when non-draft and carrying at least one live
	// match reason (unchanged from the prior taxonomy).
	if pr.Draft || len(matchReasons) == 0 {
		return PanelNone
	}
	if blocked {
		return PanelTeamAwaitingOwner
	}

	_, requested := toSet(matchReasons)[MatchReasonReviewRequested]
	if requested {
		if appr.SelfApproved {
			return PanelTeamAwaitingOwner
		}
		return PanelTeamAwaitingMe
	}
	if appr.HumanApproved {
		return PanelTeamAwaitingOwner
	}
	return PanelTeamAwaitingTeam
}

// hasUnresolvedThread reports whether any review-thread comment is still
// open — ported from the old openConversationOnly's inner loop, but no
// longer gated on approval/CI/conflict state: the 2026-09-25 ruling makes
// an open thread on a mine PR actionable on its own, regardless of
// anything else about the PR.
func hasUnresolvedThread(comments []prComment) bool {
	for _, c := range comments {
		if c.ThreadID != "" && !c.Resolved {
			return true
		}
	}
	return false
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

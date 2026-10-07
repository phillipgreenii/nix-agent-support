package interpret

import (
	"encoding/json"
	"strings"
	"time"

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
//     problems, and only uses a formal APPROVED review when clean. A comment
//     is a bot DISAPPROVAL only when its Findings are Problems (a real review
//     finding). Findings Clean + Authority Approved reads BotVerdictApproved.
//     Findings Clean + Authority Withheld ("no issues found, but
//     auto-approval blocked" — an app not opted in to auto-approval, a
//     not_approvable classification) is a POLICY limit on the bot's ability
//     to approve, not a finding against the PR, so it contributes nothing
//     (no-decision): it neither disapproves nor counts as approved.
//     Pending/Absent contribute nothing. See classifyCommentVerdict for the
//     last-definite-comment-wins ordering.
//
// Two independent signals, since neither one alone is a complete account of
// every bot's real behavior: a bot that only ever emits Review objects needs
// nothing from the comment grammar (verdictClassifier may be nil), and a bot
// whose disapproval never surfaces as a review State needs the comment
// grammar to be seen at all. Across BOTH signals, disapproved wins outright
// over approved (never silently overridden by an approval elsewhere), exactly
// mirroring the single-signal precedence the Review-state-only read already
// had. HumanApprovers/HumanApproved count every DISTINCT login with a
// currently APPROVED review that is NOT a bot (operator ruling 2026-10-02,
// pg2-k8lri: "HumanApproved does not include any bot"). Bot rule — the stored
// review data (schema.PRReview) carries only the author login, no GitHub
// account type, so detection is login-based: a login is a bot iff it is in
// approverAllowlist (the configured bot logins, which need not look like bots)
// OR isBotLogin recognizes it ("[bot]" suffix or a knownBotLogins member).
// Bots are excluded from the human count (they feed BotVerdict) so a
// bot-only approval leaves HumanApproved false, and with a bot AND a person
// HumanApprovers lists only the person. SelfApproved is unaffected by the
// bot exclusion.
// WaitingOnMe is populated by interpret.go's caller (computeWaitingOnMe), not
// here.
//
// The per-commit staleness axis (pg-pr's store.Approval.IsStale,
// INV-APPROVAL-3) is reported SEPARATELY, as Approvals.SelfReviewStale and
// Approvals.HumanApprovalStanding (computeReviewStaleness, review_staleness.go),
// because schema.PRReview now carries the commit each review was submitted
// against. It does NOT change HumanApproved, HumanApprovers, SelfApproved or
// the panel: those still mean "a currently APPROVED review exists," so a
// review is not invalidated by a LATER COMMIT for panel placement — only the
// pr.review-stale-after-push attention rule reads the new fields. This
// function DOES collapse pr.Reviews to each author's latest decisive verdict
// (see latestDecision inside computeApprovals), which fixes the OTHER
// staleness axis — a review superseded by a LATER REVIEW FROM THE SAME
// AUTHOR (e.g. CHANGES_REQUESTED followed by that same author's own
// APPROVED) no longer reads as standing. The two axes are independent; only
// the per-commit one remains unaddressed.
//
// SelfApproved and HumanChangesRequested are computed in the SAME pass as
// HumanApprovers/HumanApproved: computeApprovals runs two loops, one over
// pr.Reviews to build latestDecision (each author's single most recent
// decisive verdict), then one over that collapsed latestDecision map, where
// all four are derived together rather than via separate helper functions,
// since all four read the identical author/state pairs latestDecision
// already produced — splitting them would mean re-deriving that same
// collapse for no benefit. HumanChangesRequested excludes approverAllowlist
// logins deliberately: a bot's disapproval is already BotVerdict's concern,
// and double-carrying it here would let one bot rejection present as two
// independent signals to classifyPanel. It also excludes isBotLogin accounts
// (non-allowlisted bots such as github-actions), which are not humans and
// have no BotVerdict channel either; their CHANGES_REQUESTED is ignored.
func computeApprovals(pr prShow, self string, approverAllowlist []string, verdictClassifier *verdict.Classifier) Approvals {
	allow := toSet(approverAllowlist)

	// latestDecision collapses pr.Reviews (a full review history; see
	// latestDecisions for why array order is not trusted) to each author's most
	// recent APPROVED/CHANGES_REQUESTED verdict. A COMMENTED (or any other)
	// review never overwrites an earlier decisive one, mirroring
	// pg-connector-pr-github's own needsAttentionForPR collapse
	// (internal/provider.go) and GitHub's own reviewDecision semantics.
	// Without this, a reviewer who requested changes and was later
	// satisfied — an ordinary review cycle — would read as "still
	// requesting changes" forever, since pr.Reviews carries every
	// historical event, not just the live one.
	latestDecision := latestDecisions(pr.Reviews)

	approvers := map[string]struct{}{}
	selfApproved := false
	humanChangesRequested := false
	disapproved := false
	approvedByAllowlisted := false
	for author, state := range latestDecision {
		_, isBot := allow[author]
		isNonHuman := isBot || isBotLogin(author)
		switch state {
		case "APPROVED":
			if !isNonHuman {
				approvers[author] = struct{}{}
			}
			if self != "" && author == self {
				selfApproved = true
			}
			if isBot {
				approvedByAllowlisted = true
			}
		case "CHANGES_REQUESTED":
			if isBot {
				disapproved = true
			} else if !isNonHuman {
				humanChangesRequested = true
			}
		}
	}

	switch r := classifyCommentVerdict(pr, verdictClassifier); {
	case r.Findings == verdict.Problems:
		disapproved = true
	case r.Findings == verdict.Clean && r.Authority == verdict.Approved:
		approvedByAllowlisted = true
	}
	// Clean + Withheld (policy-blocked auto-approval) and everything else
	// deliberately fall through: no-decision.

	botVerdict := BotVerdictNoDecision
	switch {
	case disapproved:
		botVerdict = BotVerdictDisapproved
	case approvedByAllowlisted:
		botVerdict = BotVerdictApproved
	}

	selfStale, teammateStanding := computeReviewStaleness(pr, self, approverAllowlist)

	return Approvals{
		HumanApprovers:        len(approvers),
		HumanApproved:         len(approvers) > 0,
		SelfApproved:          selfApproved,
		HumanChangesRequested: humanChangesRequested,
		BotVerdict:            botVerdict,
		SelfReviewStale:       selfStale,
		HumanApprovalStanding: teammateStanding,
	}
}

// latestDecisions collapses reviews to each author's most recent decisive
// (APPROVED / CHANGES_REQUESTED) review state. It is independent of the order
// the reviews arrive in: "most recent" is decided by SubmittedAt, never by
// array position (pg-connector's pr show emitted reviews oldest-first before
// commit 8245dbc9 and newest-first since, bead pg2-4jmw2).
//
// Only when two competing reviews of one author lack a comparable timestamp
// (a pre-schema-9 connector, which does not send submitted_at) does position
// decide, and then by the connector's newest-first contract: the first review
// seen wins. Equal timestamps likewise keep the first seen.
func latestDecisions(reviews []prReview) map[string]string {
	best := map[string]prReview{}
	for _, r := range reviews {
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED":
		default:
			continue
		}
		cur, seen := best[r.Author]
		if !seen || submittedAfter(r.SubmittedAt, cur.SubmittedAt) {
			best[r.Author] = r
		}
	}
	out := make(map[string]string, len(best))
	for author, r := range best {
		out[author] = r.State
	}
	return out
}

// submittedAfter reports whether timestamp a is strictly later than b; false
// when either is missing or unparseable (the caller then keeps its incumbent).
func submittedAfter(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339, a)
	tb, errB := time.Parse(time.RFC3339, b)
	if errA != nil || errB != nil {
		return false
	}
	return ta.After(tb)
}

// knownBotLogins are bot accounts whose login pg-connector reports WITHOUT
// the "[bot]" suffix on REVIEW authors (observed 2026-10-01 on a live PR:
// review author "github-actions" while the same bot's issue-comment author
// is "github-actions[bot]"; the payload
// carries no author-type field). The "[bot]" suffix rule alone misses them.
var knownBotLogins = map[string]struct{}{
	"github-actions":                {},
	"dependabot":                    {},
	"copilot-pull-request-reviewer": {},
}

// isBotLogin reports whether login is a bot account: a GitHub "[bot]" suffix
// or a member of knownBotLogins (suffix-stripped review authors).
func isBotLogin(login string) bool {
	if strings.HasSuffix(login, "[bot]") {
		return true
	}
	_, ok := knownBotLogins[login]
	return ok
}

// classifyCommentVerdict returns the verdict Result of the most recent
// DEFINITE verdict comment across pr (top-level and review-thread), or the
// zero Result (which matches none of computeApprovals' cases) when
// verdictClassifier is nil (no generations configured — the comment-grammar
// signal is opt-in via config.VerdictGenerations) or no comment carries a
// definite reading.
//
// When more than one comment carries a matching marker (a bot re-posting its
// overview comment across pushes), the LAST definite one wins —
// pr.allComments() preserves the host's chronological order. "Definite"
// means Findings is Clean or Problems; a comment that only reaches
// Pending/Absent never overrides an earlier definite one. A later
// Clean + Withheld comment IS definite, so it supersedes an earlier Problems
// comment and the net verdict resets to no-decision (the bot re-reviewed and
// found nothing wrong, even though it cannot auto-approve).
func classifyCommentVerdict(pr prShow, verdictClassifier *verdict.Classifier) verdict.Result {
	var best verdict.Result
	if verdictClassifier == nil {
		return best
	}
	for _, c := range pr.allComments() {
		if c.Body == "" {
			continue
		}
		result := verdictClassifier.Classify(c.Body)
		if result.Findings == verdict.FindingsUnknown {
			continue
		}
		best = result
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
//   - Mine: blocked (ci failing or absent -- pending CI is NOT blocked,
//     operator ruling 2026-10-01 -- bot disapproved, a real human
//     CHANGES_REQUESTED, or a merge conflict) resolves to mine_awaiting_me
//     FIRST. CI is judged by ReviewState (review-exempt softening only).
//     Once not blocked: any unresolved review-thread comment ->
//     mine_awaiting_me (no author qualifier — any open thread is on me).
//     Otherwise, already having a human approval -> mine_awaiting_me
//     (nothing left to do but merge). Otherwise -> mine_awaiting_team.
//   - Team, in this order (operator rulings 2026-09-25, 2026-10-02 bead
//     pg2-4ajtt, and 2026-10-05):
//     1. HARD-blocked (CI failing, a real human CHANGES_REQUESTED, or a merge
//     conflict) -> team_awaiting_owner, even if I'm a requested reviewer:
//     fixing those is the PR owner's job. CI is judged by ReviewerState,
//     which also sets aside a harmless cancelled run and reads "no CI
//     data" as pending (neither is a verdict about the code).
//     2. I'm a requested reviewer (MatchReasonReviewRequested) ->
//     team_awaiting_me, whether or not I already approved (GitHub drops a
//     reviewer from review_requests once they submit any review, so
//     requested && SelfApproved is the RE-REQUEST case -- a live
//     re-request wins over the prior approval, pg2-4ajtt). This also wins
//     over a bot disapproval: the review bot's own footer says it does NOT
//     satisfy CODEOWNERS and human review is still required, so its
//     verdict is advice to a human reviewer, not a gate.
//     3. Bot disapproved (and I'm not requested) -> team_awaiting_owner.
//     4. A human approval exists -> teamApprovedPanel (owner or team,
//     decided by GitHub's merge state).
//     5. Otherwise -> team_awaiting_team.
//
// No staleness axis (package doc): "approved" here means "a currently
// APPROVED review exists," not "a non-stale one". The stored approvals do
// carry the per-commit staleness answer now (Approvals.SelfReviewStale), but
// the panels deliberately do not consult it: only the
// pr.review-stale-after-push attention rule does.
func classifyPanel(own Ownership, pr prShow, ci ciRollupResult, appr Approvals, matchReasons []string) string {
	if pr.State != "open" {
		return PanelNone
	}

	conflict := pr.hasConflict()
	botBlocked := appr.BotVerdict == BotVerdictDisapproved

	if own.ActsAsMine() {
		// CI blocks on failure (and on "none" -- no countable run at all --
		// which the 2026-10-01 ruling left as it was for own PRs). A "pending"
		// rollup does NOT block. ReviewState (not State) is consulted so a
		// failure caused solely by review-exempt jobs (config
		// review_exempt_checks) does not block either.
		reviewCI := ci.ReviewState()
		blocked := reviewCI != "success" && reviewCI != "pending" ||
			botBlocked ||
			appr.HumanChangesRequested ||
			conflict
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

	// Hard blockers: things only the PR owner can fix. The bot verdict is NOT
	// one (see the doc comment). ReviewerState, not ReviewState: a harmless
	// cancelled run and "no CI data" do not make a PR unreviewable. A human's
	// own CHANGES_REQUESTED also lands here even if the author re-requested
	// review (pre-existing behavior, pinned by a test).
	reviewerCI := ci.ReviewerState()
	if reviewerCI != "success" && reviewerCI != "pending" ||
		appr.HumanChangesRequested ||
		conflict {
		return PanelTeamAwaitingOwner
	}

	_, requested := toSet(matchReasons)[MatchReasonReviewRequested]
	if requested {
		// A live review request wins over a prior self-approval: GitHub
		// clears the request on any submitted review, so requested &&
		// SelfApproved means the author re-requested (pg2-4ajtt). It also wins
		// over a bot disapproval (operator ruling 2026-10-05).
		return PanelTeamAwaitingMe
	}
	if botBlocked {
		return PanelTeamAwaitingOwner
	}
	if appr.HumanApproved {
		return teamApprovedPanel(pr, ci)
	}
	return PanelTeamAwaitingTeam
}

// teamApprovedPanel places an approved, unblocked, not-requested-of-me team PR.
// "At least one approval" is not "ready": the stored approvals carry no
// required-approver count, so GitHub's own merge state is the readiness
// signal (operator ruling 2026-10-05):
//
//   - BLOCKED -> team_awaiting_team: GitHub says merge requirements are still
//     unmet, normally outstanding required reviews. EXCEPT while CI runs are
//     still in flight: BLOCKED is then plausibly just the pending required
//     check, and an approved PR waiting on CI stays with the owner (the
//     2026-10-01 pending ruling).
//   - UNKNOWN or absent (GitHub computes it lazily; the stored snapshot can
//     also lag, since merge state is not a change signal) -> fall back to the
//     weaker proxy "any review request is still outstanding" -> team, else
//     owner.
//   - Anything else (CLEAN, HAS_HOOKS, UNSTABLE, BEHIND; DIRTY is already
//     handled as a conflict and DRAFT never reaches here) -> team_awaiting_owner:
//     nothing is left but the owner merging or updating the branch.
//
// Known gaps: BLOCKED can also come from required conversation resolution,
// which is the author's job; it is not distinguished here.
func teamApprovedPanel(pr prShow, ci ciRollupResult) string {
	switch pr.MergeStateStatus {
	case "BLOCKED":
		if ci.RunsInFlight() {
			return PanelTeamAwaitingOwner
		}
		return PanelTeamAwaitingTeam
	case "", "UNKNOWN":
		if len(pr.ReviewRequests) > 0 {
			return PanelTeamAwaitingTeam
		}
		return PanelTeamAwaitingOwner
	default:
		return PanelTeamAwaitingOwner
	}
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
	if ci.ReviewState() != "success" {
		return false
	}
	if appr.BotVerdict == BotVerdictDisapproved {
		return false
	}
	return true
}

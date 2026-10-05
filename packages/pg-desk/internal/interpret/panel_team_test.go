package interpret

import "testing"

// Team-panel placement matrix for the operator rulings of 2026-10-05:
//
//	A. an approval is not "ready" -- GitHub's merge state decides owner vs team;
//	B. a bot-only disapproval does not hide a PR I was asked to review;
//	D. a harmless cancelled run and "no CI data" do not block a reviewer.
//
// The scenarios are the real PRs that motivated the rulings (numbers only,
// no repo identifiers). The MINE side must NOT move: its canaries are at the
// bottom and are the regression guard against the reviewer-only softenings
// leaking into ReviewState (which mine and ready-to-promote consult).

func TestClassifyPanel_TeamApprovalAndMergeState(t *testing.T) {
	const (
		reqOwner = "someone-else"
	)
	reasons := []string{MatchReasonTeamAuthored}
	approved := Approvals{HumanApproved: true, HumanApprovers: 1}
	green := ciRollupResult{State: "success"}
	// inFlightFailure: a failure coexisting with a run still running.
	inFlightFailure := ciRollupResult{State: "failure", reviewerState: "pending", inFlight: true}

	tests := []struct {
		name         string
		pr           prShow
		ci           ciRollupResult
		appr         Approvals
		matchReasons []string
		want         string
	}{
		// --- A: approved + merge state ---
		{"approved, CLEAN -> owner (119535/120601)", prShow{State: "open", MergeStateStatus: "CLEAN"}, green, approved, reasons, PanelTeamAwaitingOwner},
		{"approved, CLEAN, other requesters still listed -> owner (120192)", prShow{State: "open", MergeStateStatus: "CLEAN", ReviewRequests: []string{reqOwner}}, green, approved, reasons, PanelTeamAwaitingOwner},
		{"approved, BLOCKED -> team (119820/120115/120425)", prShow{State: "open", MergeStateStatus: "BLOCKED", ReviewRequests: []string{reqOwner}}, green, approved, reasons, PanelTeamAwaitingTeam},
		{"approved, BLOCKED, no review_requests -> team (merge state wins over requests)", prShow{State: "open", MergeStateStatus: "BLOCKED"}, green, approved, reasons, PanelTeamAwaitingTeam},
		{"approved, UNKNOWN, requesters pending -> team (112341)", prShow{State: "open", MergeStateStatus: "UNKNOWN", ReviewRequests: []string{reqOwner, "another"}}, green, approved, reasons, PanelTeamAwaitingTeam},
		{"approved, UNKNOWN, nobody pending -> owner", prShow{State: "open", MergeStateStatus: "UNKNOWN"}, green, approved, reasons, PanelTeamAwaitingOwner},
		{"approved, merge state absent, requesters pending -> team", prShow{State: "open", ReviewRequests: []string{reqOwner}}, green, approved, reasons, PanelTeamAwaitingTeam},
		{"approved, merge state absent, nobody pending -> owner (pins the pre-ruling shape)", prShow{State: "open"}, green, approved, reasons, PanelTeamAwaitingOwner},
		{"approved, HAS_HOOKS -> owner", prShow{State: "open", MergeStateStatus: "HAS_HOOKS"}, green, approved, reasons, PanelTeamAwaitingOwner},
		{"approved, UNSTABLE -> owner (only non-required checks failing)", prShow{State: "open", MergeStateStatus: "UNSTABLE"}, green, approved, reasons, PanelTeamAwaitingOwner},
		{"approved, BEHIND -> owner (author must update the branch)", prShow{State: "open", MergeStateStatus: "BEHIND"}, green, approved, reasons, PanelTeamAwaitingOwner},
		{"approved, DIRTY -> owner (merge conflict is a hard blocker)", prShow{State: "open", MergeStateStatus: "DIRTY"}, green, approved, reasons, PanelTeamAwaitingOwner},
		{"approved, BLOCKED, CI still in flight -> owner (BLOCKED is plausibly the pending check)", prShow{State: "open", MergeStateStatus: "BLOCKED"}, ciRollupResult{State: "pending"}, approved, reasons, PanelTeamAwaitingOwner},
		{"approved, BLOCKED, failure coexisting with in-flight runs -> owner", prShow{State: "open", MergeStateStatus: "BLOCKED"}, inFlightFailure, approved, reasons, PanelTeamAwaitingOwner},
		{"approved, BLOCKED, no CI data -> team (120536: 'none' is not in flight)", prShow{State: "open", MergeStateStatus: "BLOCKED"}, ciRollupResult{State: "none"}, approved, reasons, PanelTeamAwaitingTeam},
		{"not approved -> team, whatever the merge state", prShow{State: "open", MergeStateStatus: "CLEAN"}, green, Approvals{}, reasons, PanelTeamAwaitingTeam},
		{"self-approved only (not requested), BLOCKED -> team", prShow{State: "open", MergeStateStatus: "BLOCKED"}, green, Approvals{SelfApproved: true}, reasons, PanelTeamAwaitingTeam},
		{"requested of me, approved, BLOCKED -> me (a live request wins over merge state)", prShow{State: "open", MergeStateStatus: "BLOCKED"}, green, approved, []string{MatchReasonReviewRequested}, PanelTeamAwaitingMe},
		{"requested of me, CLEAN -> me", prShow{State: "open", MergeStateStatus: "CLEAN"}, green, Approvals{}, []string{MatchReasonReviewRequested}, PanelTeamAwaitingMe},
		{"approved, BLOCKED but bot disapproved, not requested -> owner (bot is judged before merge state)", prShow{State: "open", MergeStateStatus: "BLOCKED"}, green, Approvals{HumanApproved: true, BotVerdict: BotVerdictDisapproved}, reasons, PanelTeamAwaitingOwner},
		{"approved, BLOCKED, human changes requested -> owner", prShow{State: "open", MergeStateStatus: "BLOCKED"}, green, Approvals{HumanApproved: true, HumanChangesRequested: true}, reasons, PanelTeamAwaitingOwner},
		{"draft team PR with BLOCKED -> none", prShow{State: "open", Draft: true, MergeStateStatus: "BLOCKED"}, green, approved, reasons, PanelNone},
		{"no match reasons -> none", prShow{State: "open", MergeStateStatus: "BLOCKED"}, green, approved, nil, PanelNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyPanel(OwnershipTeam, tt.pr, tt.ci, tt.appr, tt.matchReasons); got != tt.want {
				t.Errorf("classifyPanel = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestClassifyPanel_TeamBotDisapprovalIsAdvice(t *testing.T) {
	requested := []string{MatchReasonTeamAuthored, MatchReasonReviewRequested}
	notRequested := []string{MatchReasonTeamAuthored}
	botNo := Approvals{BotVerdict: BotVerdictDisapproved}
	green := ciRollupResult{State: "success"}
	realFailure := ciRollupResult{State: "failure"}
	open := prShow{State: "open"}

	tests := []struct {
		name         string
		pr           prShow
		ci           ciRollupResult
		appr         Approvals
		matchReasons []string
		want         string
	}{
		{"bot disapproved only + requested of me -> me (115159/117403/117911/119847)", open, green, botNo, requested, PanelTeamAwaitingMe},
		{"bot disapproved + CI pending + requested -> me (replaces the 2026-10-01 pinned row)", open, ciRollupResult{State: "pending"}, botNo, requested, PanelTeamAwaitingMe},
		{"bot disapproved + I already approved + requested (re-request) -> me", open, green, Approvals{BotVerdict: BotVerdictDisapproved, SelfApproved: true}, requested, PanelTeamAwaitingMe},
		{"bot disapproved, not requested, unapproved -> owner", open, green, botNo, notRequested, PanelTeamAwaitingOwner},
		{"bot disapproved, not requested, approved -> owner", open, green, Approvals{BotVerdict: BotVerdictDisapproved, HumanApproved: true}, notRequested, PanelTeamAwaitingOwner},
		{"bot disapproved + real CI failure + requested -> owner (120572)", open, realFailure, botNo, requested, PanelTeamAwaitingOwner},
		{"CI failure alone + requested -> owner (a real failure still hides it)", open, realFailure, Approvals{}, requested, PanelTeamAwaitingOwner},
		{"bot disapproved + human changes-requested + requested -> owner", open, green, Approvals{BotVerdict: BotVerdictDisapproved, HumanChangesRequested: true}, requested, PanelTeamAwaitingOwner},
		{"bot disapproved + conflict + requested -> owner", prShow{State: "open", Mergeable: "CONFLICTING"}, green, botNo, requested, PanelTeamAwaitingOwner},
		{"conflict alone + requested -> owner", prShow{State: "open", MergeStateStatus: "DIRTY"}, green, Approvals{}, requested, PanelTeamAwaitingOwner},
		// Pre-existing behavior, pinned (out of scope for the 2026-10-05 rulings):
		// the reviewer's OWN CHANGES_REQUESTED beats a later re-request.
		{"my/any human changes-requested + re-requested -> owner (pre-existing)", open, green, Approvals{HumanChangesRequested: true, SelfApproved: false}, requested, PanelTeamAwaitingOwner},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyPanel(OwnershipTeam, tt.pr, tt.ci, tt.appr, tt.matchReasons); got != tt.want {
				t.Errorf("classifyPanel = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestClassifyPanel_TeamReviewerCIState(t *testing.T) {
	reasons := []string{MatchReasonTeamAuthored}
	requested := []string{MatchReasonReviewRequested}
	open := prShow{State: "open"}
	// cancelledOnly is what computeCIRollup yields for a lone cancelled run.
	cancelledOnly := ciRollupResult{State: "failure", reviewerState: "success"}

	tests := []struct {
		name         string
		ci           ciRollupResult
		matchReasons []string
		want         string
	}{
		{"cancelled-only + requested of me -> me (120650)", cancelledOnly, requested, PanelTeamAwaitingMe},
		{"cancelled-only, not requested, unapproved -> team", cancelledOnly, reasons, PanelTeamAwaitingTeam},
		{"no CI data + not requested, unapproved -> team (120536)", ciRollupResult{State: "none"}, reasons, PanelTeamAwaitingTeam},
		{"no CI data + requested of me -> me", ciRollupResult{State: "none"}, requested, PanelTeamAwaitingMe},
		{"real failure -> owner", ciRollupResult{State: "failure"}, reasons, PanelTeamAwaitingOwner},
		{"exempt-only failure -> reviewable (2026-10-02 ruling unchanged)", ciRollupResult{State: "failure", reviewState: "success"}, reasons, PanelTeamAwaitingTeam},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyPanel(OwnershipTeam, open, tt.ci, Approvals{}, tt.matchReasons); got != tt.want {
				t.Errorf("classifyPanel = %q; want %q", got, tt.want)
			}
		})
	}
}

// The MINE side must not move. Each row here would flip if ReviewerState's
// softenings (or the bot-advice rule) were wired into the shared ReviewState or
// the mine branch.
func TestClassifyPanel_MineUnchangedByTeamRulings(t *testing.T) {
	open := prShow{State: "open"}
	tests := []struct {
		name string
		pr   prShow
		ci   ciRollupResult
		appr Approvals
		want string
	}{
		{"no CI data is still not green -> awaiting me", open, ciRollupResult{State: "none"}, Approvals{}, PanelMineAwaitingMe},
		{"cancelled-only is still 'fix your CI' -> awaiting me", open, ciRollupResult{State: "failure", reviewerState: "success"}, Approvals{}, PanelMineAwaitingMe},
		{"bot disapproved is still a blocker -> awaiting me", open, ciRollupResult{State: "success"}, Approvals{BotVerdict: BotVerdictDisapproved}, PanelMineAwaitingMe},
		{"approved + BLOCKED merge state is still awaiting me (merge state is team-only)", prShow{State: "open", MergeStateStatus: "BLOCKED"}, ciRollupResult{State: "success"}, Approvals{HumanApproved: true}, PanelMineAwaitingMe},
		{"unapproved + BLOCKED merge state is still awaiting team", prShow{State: "open", MergeStateStatus: "BLOCKED"}, ciRollupResult{State: "success"}, Approvals{}, PanelMineAwaitingTeam},
		{"exempt-only failure stays reviewable (2026-10-02)", open, ciRollupResult{State: "failure", reviewState: "success", reviewerState: "success"}, Approvals{}, PanelMineAwaitingTeam},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyPanel(OwnershipMine, tt.pr, tt.ci, tt.appr, nil); got != tt.want {
				t.Errorf("classifyPanel = %q; want %q", got, tt.want)
			}
		})
	}
}

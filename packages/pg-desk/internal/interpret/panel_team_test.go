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

// Operator ruling 2026-10-12 (pg2-tgq8o): an operator with a PENDING or
// COMMENTED review of their own is the acting reviewer and is treated like a
// requested reviewer. Motivating PRs: 105206 and 113985 (bot disapproved, only
// a pending review of mine, not requested -> was owner).
func TestClassifyPanel_TeamActingReviewer(t *testing.T) {
	reasons := []string{MatchReasonTeamAuthored}
	green := ciRollupResult{State: "success"}
	realFailure := ciRollupResult{State: "failure"}
	open := prShow{State: "open"}
	acting := Approvals{SelfReviewInProgress: true}
	botNo := Approvals{SelfReviewInProgress: true, BotVerdict: BotVerdictDisapproved}
	otherApproved := Approvals{SelfReviewInProgress: true, HumanApproved: true, HumanApprovers: 1}

	tests := []struct {
		name         string
		pr           prShow
		ci           ciRollupResult
		appr         Approvals
		matchReasons []string
		want         string
	}{
		{"review in progress, not requested -> me", open, green, acting, reasons, PanelTeamAwaitingMe},
		{"review in progress + bot disapproved -> me (105206/113985)", open, green, botNo, reasons, PanelTeamAwaitingMe},
		{"review in progress + another human approved, BLOCKED -> me (their approval does not end it)", prShow{State: "open", MergeStateStatus: "BLOCKED"}, green, otherApproved, reasons, PanelTeamAwaitingMe},
		{"review in progress + another human approved, CLEAN -> me", prShow{State: "open", MergeStateStatus: "CLEAN"}, green, otherApproved, reasons, PanelTeamAwaitingMe},
		{"review in progress, only a pending review admits it -> me", open, green, acting, []string{MatchReasonReviewInProgress}, PanelTeamAwaitingMe},
		{"review in progress + real CI failure -> owner (hard blocker first)", open, realFailure, acting, reasons, PanelTeamAwaitingOwner},
		{"review in progress + conflict -> owner", prShow{State: "open", MergeStateStatus: "DIRTY"}, green, acting, reasons, PanelTeamAwaitingOwner},
		{"review in progress + human changes-requested -> owner", open, green, Approvals{SelfReviewInProgress: true, HumanChangesRequested: true}, reasons, PanelTeamAwaitingOwner},
		{"no review in progress, bot disapproved -> owner (unchanged)", open, green, Approvals{BotVerdict: BotVerdictDisapproved}, reasons, PanelTeamAwaitingOwner},
		{"standing own approval (not in progress), BLOCKED -> team (unchanged)", prShow{State: "open", MergeStateStatus: "BLOCKED"}, green, Approvals{SelfApproved: true, HumanApproved: true}, reasons, PanelTeamAwaitingTeam},
		{"review in progress on a draft -> none", prShow{State: "open", Draft: true}, green, acting, reasons, PanelNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyPanel(OwnershipTeam, tt.pr, tt.ci, tt.appr, tt.matchReasons); got != tt.want {
				t.Errorf("classifyPanel = %q; want %q", got, tt.want)
			}
		})
	}
}

// computeApprovals derives SelfReviewInProgress from the operator's own review
// states.
func TestComputeApprovals_SelfReviewInProgress(t *testing.T) {
	rev := func(state, at string) prReview {
		return prReview{Author: "me", State: state, SubmittedAt: at}
	}
	tests := []struct {
		name    string
		reviews []prReview
		want    bool
	}{
		{"none", nil, false},
		{"pending only", []prReview{rev("PENDING", "")}, true},
		{"submitted comment", []prReview{rev("COMMENTED", "2026-10-01T00:00:00Z")}, true},
		{"dismissed only", []prReview{rev("DISMISSED", "2026-10-01T00:00:00Z")}, false},
		{"dismissed + pending (113985)", []prReview{rev("DISMISSED", "2026-09-24T20:22:09Z"), rev("PENDING", "")}, true},
		{"approved", []prReview{rev("APPROVED", "2026-10-01T00:00:00Z")}, false},
		{"approved then pending follow-up", []prReview{rev("APPROVED", "2026-10-01T00:00:00Z"), rev("PENDING", "")}, false},
		{"changes requested only", []prReview{rev("CHANGES_REQUESTED", "2026-10-01T00:00:00Z")}, false},
		{"someone else's pending", []prReview{{Author: "other", State: "PENDING"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeApprovals(prShow{Reviews: tt.reviews}, "me", nil, nil).SelfReviewInProgress
			if got != tt.want {
				t.Errorf("SelfReviewInProgress = %v; want %v", got, tt.want)
			}
		})
	}
	if computeApprovals(prShow{Reviews: []prReview{{Author: "", State: "PENDING"}}}, "", nil, nil).SelfReviewInProgress {
		t.Error("an empty self login must never match")
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
		// Own drafts are never awaiting team (2026-10-09). These pin that the
		// draft rule composes with each other own-PR rule: a draft that is
		// also exempt-failing, approved, threaded or conflicting is awaiting me.
		{"own draft with exempt-only CI failure is awaiting me", prShow{State: "open", Draft: true}, ciRollupResult{State: "failure", reviewState: "success", reviewerState: "success"}, Approvals{}, PanelMineAwaitingMe},
		{"own draft with a human approval is awaiting me", prShow{State: "open", Draft: true}, ciRollupResult{State: "success"}, Approvals{HumanApproved: true}, PanelMineAwaitingMe},
		{"own draft with an unresolved thread is awaiting me", prShow{State: "open", Draft: true, Comments: []prComment{{ID: "c1", ThreadID: "t1", Resolved: false}}}, ciRollupResult{State: "success"}, Approvals{}, PanelMineAwaitingMe},
		{"own draft with a merge conflict is awaiting me", prShow{State: "open", Draft: true, Mergeable: "CONFLICTING"}, ciRollupResult{State: "success"}, Approvals{}, PanelMineAwaitingMe},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyPanel(OwnershipMine, tt.pr, tt.ci, tt.appr, nil); got != tt.want {
				t.Errorf("classifyPanel = %q; want %q", got, tt.want)
			}
		})
	}
}

// Operator ruling 2026-10-12 (Phillip), bead pg2-407y2: team_awaiting_team is
// reached only when the team positively owes something. These rows pin the two
// owner-owed cases that used to fall into it.

// Case A (120050): an approved PR whose merge state is BLOCKED while CI has a
// failure that only the review-exempt / cancelled-run softening sets aside is
// the owner's to fix, not "reviews outstanding".
func TestClassifyPanel_TeamApprovedBlockedByCIFailure(t *testing.T) {
	reasons := []string{MatchReasonTeamAuthored}
	approved := Approvals{HumanApproved: true, HumanApprovers: 1}
	blocked := prShow{State: "open", MergeStateStatus: "BLOCKED"}
	// exemptOnly is what computeCIRollup yields when the only failing job is
	// review-exempt: State stays "failure", both softened states read success.
	exemptOnly := ciRollupResult{State: "failure", reviewState: "success", reviewerState: "success"}
	cancelledOnly := ciRollupResult{State: "failure", reviewerState: "success"}

	tests := []struct {
		name string
		pr   prShow
		ci   ciRollupResult
		want string
	}{
		{"approved, BLOCKED, only an exempt check failing, nothing in flight -> owner (120050)", blocked, exemptOnly, PanelTeamAwaitingOwner},
		{"approved, BLOCKED, only a cancelled run -> owner (the failure is still the owner's)", blocked, cancelledOnly, PanelTeamAwaitingOwner},
		{"approved, BLOCKED, green CI -> team (reviews outstanding, unchanged)", blocked, ciRollupResult{State: "success"}, PanelTeamAwaitingTeam},
		{"approved, BLOCKED, no CI data -> team (unchanged)", blocked, ciRollupResult{State: "none"}, PanelTeamAwaitingTeam},
		{"approved, BLOCKED, exempt failure, requesters still listed -> owner", prShow{State: "open", MergeStateStatus: "BLOCKED", ReviewRequests: []string{"someone-else"}}, exemptOnly, PanelTeamAwaitingOwner},
		{"approved, UNKNOWN, exempt failure, requesters pending -> team (UNKNOWN fallback unchanged)", prShow{State: "open", MergeStateStatus: "UNKNOWN", ReviewRequests: []string{"someone-else"}}, exemptOnly, PanelTeamAwaitingTeam},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyPanel(OwnershipTeam, tt.pr, tt.ci, approved, reasons); got != tt.want {
				t.Errorf("classifyPanel = %q; want %q", got, tt.want)
			}
		})
	}
}

// Case B (120879): a reviewer already reviewed (COMMENTED, no approval) and
// left review threads the author has not answered; the author owes a reply.
func TestClassifyPanel_TeamUnansweredReviewThreads(t *testing.T) {
	const author = "the-author"
	reasons := []string{MatchReasonTeamAuthored}
	green := ciRollupResult{State: "success"}
	unapproved := Approvals{}

	// thread builds one inline review comment of a thread.
	thread := func(id, threadID, by string, resolved, outdated bool) prComment {
		return prComment{ID: id, Author: by, ThreadID: id, ReviewThreadID: threadID, Resolved: resolved, ThreadOutdated: outdated}
	}
	withReview := func(cs ...prComment) prShow {
		return prShow{State: "open", Author: author, Reviews: []prReview{{ID: "r1", Author: "reviewer", State: "COMMENTED", Comments: cs}}}
	}

	tests := []struct {
		name string
		pr   prShow
		want string
	}{
		{
			"two unresolved reviewer threads, no author reply -> owner (120879)",
			withReview(thread("c1", "PRRT_1", "reviewer", false, false), thread("c2", "PRRT_2", "reviewer", false, false)), PanelTeamAwaitingOwner,
		},
		{
			"one unresolved unanswered thread among a resolved one -> owner",
			withReview(thread("c1", "PRRT_1", "reviewer", true, false), thread("c2", "PRRT_2", "reviewer", false, false)), PanelTeamAwaitingOwner,
		},
		{
			"author replied in the thread -> team",
			withReview(thread("c1", "PRRT_1", "reviewer", false, false), thread("c2", "PRRT_1", author, false, false)), PanelTeamAwaitingTeam,
		},
		{
			"author replied in one thread, a second is still unanswered -> owner",
			withReview(thread("c1", "PRRT_1", "reviewer", false, false), thread("c2", "PRRT_1", author, false, false), thread("c3", "PRRT_2", "reviewer", false, false)), PanelTeamAwaitingOwner,
		},
		{
			"all threads resolved -> team",
			withReview(thread("c1", "PRRT_1", "reviewer", true, false)), PanelTeamAwaitingTeam,
		},
		{
			"unresolved thread is outdated (code since changed) -> team",
			withReview(thread("c1", "PRRT_1", "reviewer", false, true)), PanelTeamAwaitingTeam,
		},
		{
			"thread opened by the author alone -> team (nothing owed to a reviewer)",
			withReview(thread("c1", "PRRT_1", author, false, false)), PanelTeamAwaitingTeam,
		},
		{
			"no thread at all (reviewed with a bare comment) -> team",
			prShow{State: "open", Author: author, Reviews: []prReview{{ID: "r1", Author: "reviewer", State: "COMMENTED"}}},
			PanelTeamAwaitingTeam,
		},
		{
			"top-level (non-thread) comment from a reviewer -> team",
			prShow{State: "open", Author: author, Comments: []prComment{{ID: "i1", Author: "reviewer", Body: "ping"}}},
			PanelTeamAwaitingTeam,
		},
		{
			"payload without review_thread_id: each comment is its own thread -> owner for the reviewer's",
			withReview(prComment{ID: "c1", Author: "reviewer", ThreadID: "c1"}, prComment{ID: "c2", Author: author, ThreadID: "c2"}), PanelTeamAwaitingOwner,
		},
		{
			"unknown PR author cannot tell who owes a reply -> team",
			prShow{State: "open", Reviews: []prReview{{ID: "r1", Author: "reviewer", State: "COMMENTED", Comments: []prComment{thread("c1", "PRRT_1", "reviewer", false, false)}}}},
			PanelTeamAwaitingTeam,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyPanel(OwnershipTeam, tt.pr, green, unapproved, reasons); got != tt.want {
				t.Errorf("classifyPanel = %q; want %q", got, tt.want)
			}
		})
	}

	t.Run("unanswered thread + requested of me -> me (a live request wins)", func(t *testing.T) {
		pr := withReview(thread("c1", "PRRT_1", "reviewer", false, false))
		if got := classifyPanel(OwnershipTeam, pr, green, unapproved, []string{MatchReasonReviewRequested}); got != PanelTeamAwaitingMe {
			t.Errorf("classifyPanel = %q; want %q", got, PanelTeamAwaitingMe)
		}
	})
	t.Run("unanswered thread + failing CI -> owner (hard blocker, unchanged)", func(t *testing.T) {
		pr := withReview(thread("c1", "PRRT_1", "reviewer", false, false))
		if got := classifyPanel(OwnershipTeam, pr, ciRollupResult{State: "failure"}, unapproved, reasons); got != PanelTeamAwaitingOwner {
			t.Errorf("classifyPanel = %q; want %q", got, PanelTeamAwaitingOwner)
		}
	})
	t.Run("no review at all -> team (unchanged)", func(t *testing.T) {
		if got := classifyPanel(OwnershipTeam, prShow{State: "open", Author: author}, green, unapproved, reasons); got != PanelTeamAwaitingTeam {
			t.Errorf("classifyPanel = %q; want %q", got, PanelTeamAwaitingTeam)
		}
	})
}

// The thread grouping and outdated fields come from the stored pr show payload.
func TestDecodePRShow_ReviewThreadFields(t *testing.T) {
	raw := []byte(`{"author":"a","reviews":[{"id":"r","author":"x","state":"COMMENTED","comments":[` +
		`{"id":"c1","author":"x","thread_id":"c1","review_thread_id":"PRRT_1","resolved":false,"thread_outdated":true}]}]}`)
	pr, err := decodePRShow(raw)
	if err != nil {
		t.Fatal(err)
	}
	c := pr.Reviews[0].Comments[0]
	if c.ReviewThreadID != "PRRT_1" || !c.ThreadOutdated {
		t.Errorf("decoded comment = %+v; want ReviewThreadID PRRT_1 and ThreadOutdated", c)
	}
}

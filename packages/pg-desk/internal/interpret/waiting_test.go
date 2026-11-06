package interpret

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
)

// decideFromReviews runs the real approvals computation over pr's reviews for
// the operator "me" and then the single waiting-on decision, so a case states
// review history and facts and reads back (waiting-on, reason, panel).
func decideFromReviews(own Ownership, pr prShow, ci ciRollupResult) (WaitingOn, string, string) {
	appr := computeApprovals(pr, "me", nil, nil)
	reasons := computeMatchReasons(pr, []string{"author"}, nil, "me")
	w, r := decideWaitingOn(own, pr, ci, appr, reasons)
	return w, r, classifyPanel(own, pr, ci, appr, reasons)
}

func TestDecideWaitingOn_Table(t *testing.T) {
	green := ciRollupResult{State: "success"}
	team := func(mod func(*prShow)) prShow {
		p := prShow{State: "open", Author: "author", HeadSHA: "h2", MergeStateStatus: "CLEAN"}
		if mod != nil {
			mod(&p)
		}
		return p
	}
	tests := []struct {
		name       string
		own        Ownership
		pr         prShow
		wantWait   WaitingOn
		wantReason string
		wantPanel  string
	}{
		{
			"stale review with a teammate approval of the current head is waiting on me",
			OwnershipTeam,
			team(func(p *prShow) {
				p.Reviews = []prReview{rev("me", "APPROVED", oidp("h1")), rev("mate", "APPROVED", oidp("h2"))}
			}),
			WaitingOnMe, ReasonStaleReview, PanelTeamAwaitingMe,
		},
		{
			"the operator's own stale CHANGES_REQUESTED routes to the operator",
			OwnershipTeam,
			team(func(p *prShow) { p.Reviews = []prReview{rev("me", "CHANGES_REQUESTED", oidp("h1"))} }),
			WaitingOnMe, ReasonStaleReview, PanelTeamAwaitingMe,
		},
		{
			"the operator's own fresh CHANGES_REQUESTED stays with the author",
			OwnershipTeam,
			team(func(p *prShow) { p.Reviews = []prReview{rev("me", "CHANGES_REQUESTED", oidp("h2"))} }),
			WaitingOnAuthor, ReasonChangesRequested, PanelTeamAwaitingOwner,
		},
		{
			"an unreported commit on the operator's CHANGES_REQUESTED is unknown, so it stays a blocker",
			OwnershipTeam,
			team(func(p *prShow) { p.Reviews = []prReview{rev("me", "CHANGES_REQUESTED", nil)} }),
			WaitingOnAuthor, ReasonChangesRequested, PanelTeamAwaitingOwner,
		},
		{
			"another reviewer's CHANGES_REQUESTED still blocks even beside my stale one",
			OwnershipTeam,
			team(func(p *prShow) {
				p.Reviews = []prReview{rev("me", "CHANGES_REQUESTED", oidp("h1")), rev("mate", "CHANGES_REQUESTED", oidp("h2"))}
			}),
			WaitingOnAuthor, ReasonChangesRequested, PanelTeamAwaitingOwner,
		},
		{
			"a stale review is not approvable while the PR conflicts",
			OwnershipTeam,
			team(func(p *prShow) {
				p.Mergeable = "CONFLICTING"
				p.Reviews = []prReview{rev("me", "APPROVED", oidp("h1"))}
			}),
			WaitingOnAuthor, ReasonConflict, PanelTeamAwaitingOwner,
		},
		{
			"an empty pending draft does not route the PR to the operator",
			OwnershipTeam,
			team(func(p *prShow) { p.Reviews = []prReview{rev("me", "PENDING", nil)} }),
			WaitingOnOthers, ReasonAwaitingReview, PanelTeamAwaitingTeam,
		},
		{
			"a stacked child with a conflicted parent waits on the author",
			OwnershipTeam,
			team(func(p *prShow) {
				p.Branch, p.Base = "c", "b"
				p.stack = []gather.StackParent{{ID: "r#2", Branch: "b", Base: "main", State: "open", MergeStateStatus: "DIRTY"}}
				p.Reviews = []prReview{rev("me", "APPROVED", oidp("h1"))}
			}),
			WaitingOnAuthor, ReasonConflict, PanelTeamAwaitingOwner,
		},
		{
			"a review request on a draft team PR routes nothing",
			OwnershipTeam,
			team(func(p *prShow) { p.Draft = true; p.ReviewRequests = []string{"me"} }),
			WaitingOnNone, "", PanelNone,
		},
		{
			"a closed PR is waiting on nobody",
			OwnershipMine,
			prShow{State: "closed"},
			WaitingOnNone, "", PanelNone,
		},
		{
			"an own PR with no review yet waits on others",
			OwnershipMine,
			prShow{State: "open", Author: "me"},
			WaitingOnOthers, ReasonAwaitingReview, PanelMineAwaitingTeam,
		},
		{
			"an own draft is the operator's",
			OwnershipMine,
			prShow{State: "open", Author: "me", Draft: true},
			WaitingOnMe, ReasonOwnDraft, PanelMineAwaitingMe,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, r, panel := decideFromReviews(tt.own, tt.pr, green)
			if w != tt.wantWait || r != tt.wantReason || panel != tt.wantPanel {
				t.Errorf("got (%q, %q, panel %q), want (%q, %q, panel %q)", w, r, panel, tt.wantWait, tt.wantReason, tt.wantPanel)
			}
		})
	}
}

// Every reason code of both tables is reachable from some input, yields the
// waiting-on its table row names, and has a panel that is exactly the lookup
// over (ownership, waiting-on).
func TestDecideWaitingOn_EveryReasonCodeIsReachable(t *testing.T) {
	green := ciRollupResult{State: "success"}
	failing := ciRollupResult{State: "failure"}
	pending := ciRollupResult{State: "pending", inFlight: true}
	requested := []string{MatchReasonTeamAuthored, MatchReasonReviewRequested}
	matched := []string{MatchReasonTeamAuthored}
	open := prShow{State: "open", Author: "author", MergeStateStatus: "CLEAN"}
	with := func(mod func(*prShow)) prShow {
		p := open
		mod(&p)
		return p
	}
	type in struct {
		own     Ownership
		pr      prShow
		ci      ciRollupResult
		appr    Approvals
		reasons []string
	}
	own := func(pr prShow, ci ciRollupResult, appr Approvals) in { return in{OwnershipMine, pr, ci, appr, nil} }
	tm := func(pr prShow, ci ciRollupResult, appr Approvals, reasons []string) in {
		return in{OwnershipTeam, pr, ci, appr, reasons}
	}
	thread := with(func(p *prShow) {
		p.Comments = []prComment{{ID: "c1", Author: "mate", ThreadID: "c1", ReviewThreadID: "t1"}}
	})
	inputs := map[Ownership]map[string]in{
		OwnershipMine: {
			ReasonCIFailing:        own(open, failing, Approvals{}),
			ReasonBotDisapproved:   own(open, green, Approvals{BotVerdict: BotVerdictDisapproved}),
			ReasonChangesRequested: own(open, green, Approvals{HumanChangesRequested: true}),
			ReasonConflict:         own(with(func(p *prShow) { p.Mergeable = "CONFLICTING" }), green, Approvals{}),
			ReasonOpenThread: own(with(func(p *prShow) {
				p.Comments = []prComment{{ID: "c1", ThreadID: "c1", Resolved: false}}
			}), green, Approvals{}),
			ReasonApprovedReady:  own(open, green, Approvals{HumanApproved: true}),
			ReasonOwnDraft:       own(with(func(p *prShow) { p.Draft = true }), green, Approvals{}),
			ReasonAwaitingReview: own(open, green, Approvals{}),
		},
		OwnershipTeam: {
			ReasonCIFailing:        tm(open, failing, Approvals{}, matched),
			ReasonConflict:         tm(with(func(p *prShow) { p.Mergeable = "CONFLICTING" }), green, Approvals{}, matched),
			ReasonChangesRequested: tm(open, green, Approvals{HumanChangesRequested: true, OtherChangesRequested: true}, matched),
			ReasonStaleReview:      tm(open, green, Approvals{SelfReviewStale: true}, matched),
			ReasonReviewRequested:  tm(open, green, Approvals{}, requested),
			ReasonActingReviewer:   tm(open, green, Approvals{SelfReviewInProgress: true}, matched),
			ReasonBotDisapproved:   tm(open, green, Approvals{BotVerdict: BotVerdictDisapproved}, matched),
			ReasonApprovedMergeBlk: tm(with(func(p *prShow) { p.MergeStateStatus = "BLOCKED" }), green, Approvals{HumanApproved: true}, matched),
			ReasonApprovedCIPending: tm(with(func(p *prShow) { p.MergeStateStatus = "BLOCKED" }), pending,
				Approvals{HumanApproved: true}, matched),
			ReasonApprovedRequested: tm(with(func(p *prShow) { p.MergeStateStatus = "UNKNOWN"; p.ReviewRequests = []string{"x"} }), green,
				Approvals{HumanApproved: true}, matched),
			ReasonApprovedToMerge:  tm(open, green, Approvals{HumanApproved: true}, matched),
			ReasonUnansweredThread: tm(thread, green, Approvals{}, matched),
			ReasonAwaitingReview:   tm(open, green, Approvals{}, matched),
		},
	}
	tables := map[Ownership][]ReasonRow{OwnershipMine: OwnReasons, OwnershipTeam: TeamReasons}
	for o, rows := range tables {
		if len(inputs[o]) != len(rows) {
			t.Errorf("%s: %d inputs for %d reason codes", o, len(inputs[o]), len(rows))
		}
		for _, row := range rows {
			t.Run(string(o)+"/"+row.Reason, func(t *testing.T) {
				i, ok := inputs[o][row.Reason]
				if !ok {
					t.Fatalf("no input for reason code %q", row.Reason)
				}
				w, r := decideWaitingOn(i.own, i.pr, i.ci, i.appr, i.reasons)
				if w != row.WaitingOn || r != row.Reason {
					t.Fatalf("decision = (%q, %q), want (%q, %q)", w, r, row.WaitingOn, row.Reason)
				}
				if got, want := classifyPanel(i.own, i.pr, i.ci, i.appr, i.reasons), PanelFor(o, row.WaitingOn); got != want {
					t.Errorf("panel = %q, want the lookup %q", got, want)
				}
			})
		}
	}
}

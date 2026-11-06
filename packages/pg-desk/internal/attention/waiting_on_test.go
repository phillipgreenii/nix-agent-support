package attention

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Every reason code, against both outputs: a row stores one (waiting-on,
// reason) pair, its panel is the lookup over (ownership, waiting-on), and the
// attention rules raise from the stored pair alone, so the panel and the
// attention feed cannot disagree about who owes the next step.
func TestEveryReasonCodeFollowsIntoPanelAndAttention(t *testing.T) {
	type tc struct {
		ownership string
		own       interpret.Ownership
		rows      []interpret.ReasonRow
	}
	for _, c := range []tc{
		{"mine", interpret.OwnershipMine, interpret.OwnReasons},
		{"co-owned", interpret.OwnershipCoOwned, interpret.OwnReasons},
		{"team", interpret.OwnershipTeam, interpret.TeamReasons},
	} {
		for _, row := range c.rows {
			t.Run(c.ownership+"/"+row.Reason, func(t *testing.T) {
				st := store.OpenNewSchemaForTest(t)
				panel := interpret.PanelFor(c.own, row.WaitingOn)
				spec := prSpec{
					number: 1, ownership: c.ownership, panel: panel, ci: "success",
					waitingOn: row.WaitingOn, reason: row.Reason,
				}
				put(t, st, spec)
				res := evaluate(t, st, baseConfig())
				tr := res.Traces[Ref("pr", spec.id())]

				// The panel never disagrees with the stored waiting-on.
				if panel == "" && row.WaitingOn != interpret.WaitingOnNone {
					t.Fatalf("waiting on %q has no panel", row.WaitingOn)
				}
				if got := interpret.PanelFor(c.own, row.WaitingOn) == interpret.PanelMineAwaitingMe ||
					interpret.PanelFor(c.own, row.WaitingOn) == interpret.PanelTeamAwaitingMe; got != (row.WaitingOn == interpret.WaitingOnMe) {
					t.Fatalf("panel %q disagrees with waiting-on %q", panel, row.WaitingOn)
				}

				var kinds []string
				for _, cand := range tr.Survived {
					kinds = append(kinds, cand.Kind)
				}
				var want string
				switch {
				case row.WaitingOn != interpret.WaitingOnMe:
				case c.own == interpret.OwnershipTeam:
					want = KindReviewRequested
				case row.Reason == interpret.ReasonCIFailing || row.Reason == interpret.ReasonOwnDraft:
					// CI is pr.own-ci-failing's, from the raw rollup; an own
					// draft is not an item.
				default:
					want = KindOwnNeedsAction
				}
				if want == "" {
					if len(kinds) != 0 {
						t.Errorf("waiting on %q (%s) raised %v, want nothing", row.WaitingOn, row.Reason, kinds)
					}
					return
				}
				if len(kinds) != 1 || kinds[0] != want {
					t.Errorf("waiting on me (%s) raised %v, want exactly %s", row.Reason, kinds, want)
				}
			})
		}
	}
}

// The three summaries of pr.review-requested are variants chosen by the stored
// reason code; the folded-in stale-after-push kind no longer exists.
func TestReviewRequestedSummaryFollowsTheReasonCode(t *testing.T) {
	for reason, want := range map[string]string{
		interpret.ReasonReviewRequested: "review requested of me",
		interpret.ReasonActingReviewer:  "my review is in progress",
		interpret.ReasonStaleReview:     "new commits since my review",
	} {
		st := store.OpenNewSchemaForTest(t)
		put(t, st, prSpec{number: 1, ownership: "team", panel: interpret.PanelTeamAwaitingMe, waitingOn: interpret.WaitingOnMe, reason: reason})
		it := mustItem(t, evaluate(t, st, baseConfig()), 1)
		if it.Rule != KindReviewRequested || !strings.Contains(it.Summary, want) {
			t.Errorf("reason %s: item = %+v, want %s saying %q", reason, it, KindReviewRequested, want)
		}
	}
	for _, kind := range RuleKinds() {
		if kind == "pr.review-stale-after-push" {
			t.Errorf("rule kind %s must be removed", kind)
		}
	}
}

// A row stored before the decision was stored raises no PR item until the
// entity is interpreted again.
func TestRowWithoutStoredDecisionRaisesNothing(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	put(t, st, prSpec{number: 1, ownership: "team", panel: interpret.PanelTeamAwaitingMe, noDecision: true})
	put(t, st, prSpec{number: 2, ownership: "mine", panel: interpret.PanelMineAwaitingMe, ci: "success", noDecision: true})
	if res := evaluate(t, st, baseConfig()); len(res.Items) != 0 {
		t.Fatalf("items = %+v, want none", res.Items)
	}
}

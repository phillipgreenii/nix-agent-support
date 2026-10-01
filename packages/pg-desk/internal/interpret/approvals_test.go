package interpret

import (
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/verdict"
)

func policyClassifier(t *testing.T) *verdict.Classifier {
	t.Helper()
	c, err := verdict.New([]verdict.Generation{{
		ID:                "v1",
		BodyMarker:        "X-TEST-MARKER",
		FindingsPatterns:  []string{`(?im)^DECISION: CLEAN$`, `(?im)^DECISION: ISSUES-FOUND$`},
		AuthorityPatterns: []string{`(?im)^AUTHORITY: AUTO-APPROVED$`, `(?im)^AUTHORITY: BLOCKED`},
	}})
	if err != nil {
		t.Fatalf("verdict.New: %v", err)
	}
	return c
}

// A Clean finding with auto-approval withheld is a policy limit, not a
// disapproval; the last DEFINITE comment wins, and Clean+Withheld is definite.
func TestComputeApprovals_PolicyWithheldIsNotDisapproval(t *testing.T) {
	c := policyClassifier(t)
	body := func(lines ...string) string { return "X-TEST-MARKER\n" + strings.Join(lines, "\n") }
	cases := []struct {
		name     string
		comments []string
		want     string
	}{
		{"clean + withheld -> no-decision", []string{body("DECISION: CLEAN", "AUTHORITY: BLOCKED see below")}, BotVerdictNoDecision},
		{"problems -> disapproved", []string{body("DECISION: ISSUES-FOUND")}, BotVerdictDisapproved},
		{"clean + approved -> approved", []string{body("DECISION: CLEAN", "AUTHORITY: AUTO-APPROVED")}, BotVerdictApproved},
		{"later clean+withheld resets earlier problems (last definite wins)", []string{
			body("DECISION: ISSUES-FOUND"), body("DECISION: CLEAN", "AUTHORITY: BLOCKED"),
		}, BotVerdictNoDecision},
		{"later problems overrides earlier clean+approved", []string{
			body("DECISION: CLEAN", "AUTHORITY: AUTO-APPROVED"), body("DECISION: ISSUES-FOUND"),
		}, BotVerdictDisapproved},
		{"later non-verdict comment does not override", []string{
			body("DECISION: ISSUES-FOUND"), "just chatting",
		}, BotVerdictDisapproved},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var pr prShow
			for i, b := range tc.comments {
				pr.Comments = append(pr.Comments, prComment{ID: fmt.Sprint(i), Author: "review-bot", Body: b})
			}
			if got := computeApprovals(pr, "", []string{"review-bot"}, c).BotVerdict; got != tc.want {
				t.Fatalf("BotVerdict=%q; want %q", got, tc.want)
			}
		})
	}
}

func TestComputeApprovals_BotsAreNotHumanApprovers(t *testing.T) {
	allow := []string{"review-bot"}
	t.Run("allowlisted bot approval: bot approved, zero humans", func(t *testing.T) {
		got := computeApprovals(prShow{Reviews: []prReview{{Author: "review-bot", State: "APPROVED"}}}, "", allow, nil)
		if got.BotVerdict != BotVerdictApproved || got.HumanApprovers != 0 || got.HumanApproved {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("github-actions (suffix stripped) is not human", func(t *testing.T) {
		got := computeApprovals(prShow{Reviews: []prReview{{Author: "github-actions", State: "APPROVED"}}}, "", allow, nil)
		if got.HumanApprovers != 0 || got.HumanApproved {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("[bot] suffix is not human", func(t *testing.T) {
		got := computeApprovals(prShow{Reviews: []prReview{{Author: "some-app[bot]", State: "APPROVED"}}}, "", nil, nil)
		if got.HumanApprovers != 0 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("human + bots counts 1", func(t *testing.T) {
		got := computeApprovals(prShow{Reviews: []prReview{
			{Author: "alice", State: "APPROVED"},
			{Author: "review-bot", State: "APPROVED"},
			{Author: "github-actions", State: "APPROVED"},
		}}, "", allow, nil)
		if got.HumanApprovers != 1 || !got.HumanApproved {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("self approval still reported", func(t *testing.T) {
		got := computeApprovals(prShow{Reviews: []prReview{{Author: "me", State: "APPROVED"}}}, "me", allow, nil)
		if !got.SelfApproved || got.HumanApprovers != 1 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("non-allowlisted bot CHANGES_REQUESTED is not human", func(t *testing.T) {
		got := computeApprovals(prShow{Reviews: []prReview{{Author: "github-actions", State: "CHANGES_REQUESTED"}}}, "", allow, nil)
		if got.HumanChangesRequested {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestClassifyPanel_PolicyBlockedBotOnlyApprovalTeamPR(t *testing.T) {
	c := policyClassifier(t)
	pr := prShow{
		State:    "open",
		Reviews:  []prReview{{Author: "review-bot", State: "APPROVED"}},
		Comments: []prComment{{ID: "1", Author: "review-bot", Body: "X-TEST-MARKER\nDECISION: CLEAN\nAUTHORITY: BLOCKED \"not_approvable\""}},
	}
	appr := computeApprovals(pr, "me", []string{"review-bot"}, c)
	got := classifyPanel(OwnershipTeam, pr, ciRollupResult{State: "success"}, appr, []string{MatchReasonTeamAuthored})
	if got != PanelTeamAwaitingTeam {
		t.Fatalf("classifyPanel = %q (appr %+v); want %q", got, appr, PanelTeamAwaitingTeam)
	}
}

func TestIsBotLogin(t *testing.T) {
	cases := []struct {
		login string
		want  bool
	}{
		{"dependabot[bot]", true},
		{"some-app[bot]", true},
		{"github-actions", true},
		{"dependabot", true},
		{"copilot-pull-request-reviewer", true},
		{"alice", false},
		{"bot", false},
		{"[bot]x", false},
		{"github-actions-fan", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isBotLogin(tc.login); got != tc.want {
			t.Errorf("isBotLogin(%q) = %v; want %v", tc.login, got, tc.want)
		}
	}
}

func TestComputeApprovals_AllowlistedBotChangesRequested(t *testing.T) {
	pr := prShow{Reviews: []prReview{
		{Author: "review-bot", State: "CHANGES_REQUESTED"},
		{Author: "alice", State: "APPROVED"},
	}}
	got := computeApprovals(pr, "", []string{"review-bot"}, nil)
	if got.BotVerdict != BotVerdictDisapproved {
		t.Fatalf("BotVerdict = %q; want disapproved (%+v)", got.BotVerdict, got)
	}
	if got.HumanChangesRequested {
		t.Fatalf("allowlisted bot CHANGES_REQUESTED leaked into HumanChangesRequested: %+v", got)
	}
	if got.HumanApprovers != 1 {
		t.Fatalf("HumanApprovers = %d; want 1", got.HumanApprovers)
	}
}

func TestComputeApprovals_HumanChangesRequestedAlongsideBots(t *testing.T) {
	pr := prShow{Reviews: []prReview{
		{Author: "github-actions", State: "CHANGES_REQUESTED"},
		{Author: "bob", State: "CHANGES_REQUESTED"},
	}}
	if got := computeApprovals(pr, "", nil, nil); !got.HumanChangesRequested {
		t.Fatalf("real human CHANGES_REQUESTED lost: %+v", got)
	}
}

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

// TestHumanApproved_ExcludesEveryBot pins the pg2-k8lri ruling ("HumanApproved
// does not include any bot") end to end: computeApprovals feeding
// classifyPanel, for each bot-detection signal (allowlisted login that is NOT a
// Bot-looking account, "[bot]" suffix, known suffix-less bot), on both the
// team and mine branches. A bot-only approval must not read as "nothing left
// but merge" / "owner must act".
func TestHumanApproved_ExcludesEveryBot(t *testing.T) {
	allow := []string{"policy-reviewer"} // plain login: only the allowlist marks it a bot
	green := ciRollupResult{State: "success"}
	teamReasons := []string{MatchReasonTeamAuthored}

	for _, bot := range []string{"policy-reviewer", "some-app[bot]", "github-actions"} {
		t.Run("bot-only approval by "+bot, func(t *testing.T) {
			pr := prShow{State: "open", Reviews: []prReview{{Author: bot, State: "APPROVED"}}}
			appr := computeApprovals(pr, "me", allow, nil)
			if appr.HumanApproved || appr.HumanApprovers != 0 {
				t.Fatalf("HumanApproved=%v HumanApprovers=%d; want false/0", appr.HumanApproved, appr.HumanApprovers)
			}
			if got := classifyPanel(OwnershipMine, pr, green, appr, nil); got != PanelMineAwaitingTeam {
				t.Errorf("mine panel = %q; want %q (not routed to mine_awaiting_me on a bot approval)", got, PanelMineAwaitingTeam)
			}
			if got := classifyPanel(OwnershipTeam, pr, green, appr, teamReasons); got != PanelTeamAwaitingTeam {
				t.Errorf("team panel = %q; want %q (not routed to team_awaiting_owner on a bot approval)", got, PanelTeamAwaitingTeam)
			}
		})
	}

	t.Run("bots and a person: only the person is listed", func(t *testing.T) {
		pr := prShow{State: "open", Reviews: []prReview{
			{Author: "policy-reviewer", State: "APPROVED"},
			{Author: "some-app[bot]", State: "APPROVED"},
			{Author: "github-actions", State: "APPROVED"},
			{Author: "alice", State: "APPROVED"},
		}}
		appr := computeApprovals(pr, "", allow, nil)
		if !appr.HumanApproved || appr.HumanApprovers != 1 {
			t.Fatalf("HumanApproved=%v HumanApprovers=%d; want true/1 (alice only)", appr.HumanApproved, appr.HumanApprovers)
		}
		if got := classifyPanel(OwnershipMine, pr, green, appr, nil); got != PanelMineAwaitingMe {
			t.Errorf("mine panel = %q; want %q", got, PanelMineAwaitingMe)
		}
		if got := classifyPanel(OwnershipTeam, pr, green, appr, teamReasons); got != PanelTeamAwaitingOwner {
			t.Errorf("team panel = %q; want %q", got, PanelTeamAwaitingOwner)
		}
	})
}

// latestDecisions must give the same per-author verdict whichever order the
// connector emits reviews in (oldest-first before pg-connector 8245dbc9,
// newest-first since; bead pg2-4jmw2). Each case is run forwards and reversed.
func TestLatestDecisions_IsOrderIndependent(t *testing.T) {
	rv := func(author, state, at string) prReview {
		return prReview{ID: author + state + at, Author: author, State: state, SubmittedAt: at}
	}
	cases := []struct {
		name    string
		reviews []prReview // oldest-first
		want    map[string]string
	}{
		{
			name: "changes requested then approved by same author",
			reviews: []prReview{
				rv("alice", "CHANGES_REQUESTED", "2026-10-01T09:00:00Z"),
				rv("alice", "APPROVED", "2026-10-02T09:00:00Z"),
			},
			want: map[string]string{"alice": "APPROVED"},
		},
		{
			name: "approved then changes requested by same author",
			reviews: []prReview{
				rv("alice", "APPROVED", "2026-10-01T09:00:00Z"),
				rv("alice", "CHANGES_REQUESTED", "2026-10-02T09:00:00Z"),
			},
			want: map[string]string{"alice": "CHANGES_REQUESTED"},
		},
		{
			name: "later COMMENTED never overwrites a decisive review",
			reviews: []prReview{
				rv("alice", "APPROVED", "2026-10-01T09:00:00Z"),
				rv("alice", "COMMENTED", "2026-10-03T09:00:00Z"),
			},
			want: map[string]string{"alice": "APPROVED"},
		},
		{
			name: "authors are collapsed independently, offsets compared as instants",
			reviews: []prReview{
				rv("alice", "CHANGES_REQUESTED", "2026-10-01T09:00:00Z"),
				rv("bob", "APPROVED", "2026-10-01T09:30:00Z"),
				rv("alice", "APPROVED", "2026-10-01T05:30:00-04:00"),        // 09:30Z, after 09:00Z
				rv("bob", "CHANGES_REQUESTED", "2026-10-01T08:00:00-04:00"), // 12:00Z, after bob's approval
			},
			want: map[string]string{"alice": "APPROVED", "bob": "CHANGES_REQUESTED"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rev := make([]prReview, len(tc.reviews))
			for i, r := range tc.reviews {
				rev[len(rev)-1-i] = r
			}
			for name, in := range map[string][]prReview{"oldest-first": tc.reviews, "newest-first": rev} {
				got := latestDecisions(in)
				if len(got) != len(tc.want) {
					t.Fatalf("%s: got %v, want %v", name, got, tc.want)
				}
				for a, st := range tc.want {
					if got[a] != st {
						t.Fatalf("%s: %s = %q, want %q (all: %v)", name, a, got[a], st, got)
					}
				}
			}
		})
	}
}

// Without timestamps (a pre-schema-9 connector) position decides, by the
// newest-first contract: the first decisive review seen wins.
func TestLatestDecisions_NoTimestampsFallsBackToNewestFirst(t *testing.T) {
	got := latestDecisions([]prReview{
		{Author: "alice", State: "APPROVED"},          // newest
		{Author: "alice", State: "CHANGES_REQUESTED"}, // older
	})
	if got["alice"] != "APPROVED" {
		t.Fatalf("alice = %q, want APPROVED", got["alice"])
	}
}

// End to end through computeApprovals: the same reviews in either order give
// the same Approvals.
func TestComputeApprovals_ReviewOrderDoesNotChangeResult(t *testing.T) {
	oldest := []prReview{
		{Author: "me", State: "CHANGES_REQUESTED", SubmittedAt: "2026-10-01T09:00:00Z"},
		{Author: "me", State: "APPROVED", SubmittedAt: "2026-10-02T09:00:00Z"},
		{Author: "bob", State: "APPROVED", SubmittedAt: "2026-10-01T10:00:00Z"},
		{Author: "bob", State: "CHANGES_REQUESTED", SubmittedAt: "2026-10-02T10:00:00Z"},
	}
	newest := []prReview{oldest[3], oldest[2], oldest[1], oldest[0]}
	a := computeApprovals(prShow{Reviews: oldest}, "me", nil, nil)
	b := computeApprovals(prShow{Reviews: newest}, "me", nil, nil)
	if a != b {
		t.Fatalf("order changed result: oldest-first %+v vs newest-first %+v", a, b)
	}
	if !a.SelfApproved || !a.HumanChangesRequested || a.HumanApprovers != 1 {
		t.Fatalf("unexpected approvals %+v (want self approved, bob requesting changes, 1 approver)", a)
	}
}

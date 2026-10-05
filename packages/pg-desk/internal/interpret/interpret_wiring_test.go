package interpret

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
)

// teamPRFacts builds Facts for an open, team-authored PR matched by a watch
// label, with the given extra pr-show fields and CI runs.
func teamPRFacts(t *testing.T, extra map[string]any, runs []map[string]any) gather.Facts {
	t.Helper()
	pr := map[string]any{
		"author": "teammate",
		"title":  "feat: thing",
		"state":  "open",
		"labels": []string{"watch-me"},
	}
	for k, v := range extra {
		pr[k] = v
	}
	facts := factsFor(t, pr)
	facts.CI, _ = json.Marshal(map[string]any{"runs": runs})
	return facts
}

func ciRun(name, conclusion, sha, id string, attempt int) map[string]any {
	r := map[string]any{"name": name, "status": "completed", "conclusion": conclusion, "id": id, "attempt": attempt}
	if sha != "" {
		r["head_sha"] = sha
	}
	return r
}

func wiringConfig() *config.Config {
	return &config.Config{
		SelfLogin:         "me",
		TeamMembers:       []string{"teammate"},
		WatchLabels:       []string{"watch-me"},
		ApproverAllowlist: []string{"example-review-bot"},
		VerdictGenerations: []config.VerdictGeneration{{
			ID:                "example-review-bot-v1",
			BodyMarker:        "<!-- example-review-bot -->",
			FindingsPatterns:  []string{`(?i)Result:\s*no findings`, `(?i)Result:\s*findings`},
			AuthorityPatterns: []string{`(?i)Auto-approve:\s*submitting`},
		}},
	}
}

var wiringClock = FixedClock(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))

// Interpret must pass the PR's head_sha through to computeCIRollup: stale
// cancelled runs on an old SHA must not turn a green head into a blocked panel.
func TestInterpret_CIScopedToPRHeadSHA(t *testing.T) {
	oldCancelled := []map[string]any{
		ciRun("A", "cancelled", "old", "5", 1), ciRun("B", "cancelled", "old", "6", 1),
	}
	with := func(head ...map[string]any) []map[string]any {
		return append(append([]map[string]any{}, head...), oldCancelled...)
	}
	// Without a head SHA the rollup falls back to counting EVERY run. The
	// fallback cases need a REAL failure on the old SHA to prove that: a
	// cancelled run no longer blocks a reviewer (see
	// TestInterpret_CancelledAndNoCIReviewerRules), so it cannot stand in.
	oldFailed := []map[string]any{
		ciRun("A", "failure", "old", "5", 1), ciRun("B", "failure", "old", "6", 1),
	}
	withFailed := func(head ...map[string]any) []map[string]any {
		return append(append([]map[string]any{}, head...), oldFailed...)
	}
	cases := []struct {
		name  string
		extra map[string]any
		runs  []map[string]any
		want  string
	}{
		{
			"head green, old cancelled ignored -> awaiting_team",
			map[string]any{"head_sha": "new"},
			with(ciRun("A", "success", "new", "10", 1), ciRun("B", "success", "new", "11", 1)),
			PanelTeamAwaitingTeam,
		},
		{
			"head failing -> awaiting_owner",
			map[string]any{"head_sha": "new"},
			with(ciRun("A", "failure", "new", "10", 1), ciRun("B", "success", "new", "11", 1)),
			PanelTeamAwaitingOwner,
		},
		{
			"no head_sha in pr show falls back to all runs -> awaiting_owner",
			nil,
			withFailed(ciRun("A", "success", "new", "10", 1), ciRun("B", "success", "new", "11", 1)),
			PanelTeamAwaitingOwner,
		},
		{
			"empty head_sha string falls back to all runs -> awaiting_owner",
			map[string]any{"head_sha": ""},
			withFailed(ciRun("A", "success", "new", "10", 1)),
			PanelTeamAwaitingOwner,
		},
		{
			"explicit null head_sha falls back -> awaiting_owner",
			map[string]any{"head_sha": nil},
			withFailed(ciRun("A", "success", "new", "10", 1)),
			PanelTeamAwaitingOwner,
		},
		{
			"no head_sha, only cancelled runs counted -> awaiting_team (a cancellation is not a verdict)",
			nil,
			with(ciRun("A", "success", "new", "10", 1)),
			PanelTeamAwaitingTeam,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			interp, err := Interpret(teamPRFacts(t, tc.extra, tc.runs), wiringClock, wiringConfig())
			if err != nil {
				t.Fatalf("Interpret: %v", err)
			}
			if interp.Panel != tc.want {
				t.Fatalf("Panel = %q; want %q", interp.Panel, tc.want)
			}
		})
	}
}

func TestInterpret_MalformedHeadSHAType_Errors(t *testing.T) {
	facts := teamPRFacts(t, map[string]any{"head_sha": 12345}, nil)
	if _, err := Interpret(facts, wiringClock, wiringConfig()); err == nil {
		t.Fatal("expected a decode error for a non-string head_sha")
	}
}

// A review bot's "no findings, but auto-approval blocked by policy" comment
// plus an APPROVED review from the allowlisted bot is no human approval and
// no bot disapproval.
func TestInterpret_PolicyBlockedBotComment_AwaitsTeam(t *testing.T) {
	const blockedBody = "<!-- example-review-bot -->\nResult: no findings\nAuto-approve: blocked (app not opted in)"
	const problemsBody = "<!-- example-review-bot -->\nResult: findings\n"
	const approvedBody = "<!-- example-review-bot -->\nResult: no findings\nAuto-approve: submitting"
	green := []map[string]any{ciRun("A", "success", "new", "1", 1)}
	cases := []struct {
		name       string
		comment    string
		withReview bool
		wantBot    string
		wantPanel  string
	}{
		{"clean + blocked, no review -> no-decision", blockedBody, false, BotVerdictNoDecision, PanelTeamAwaitingTeam},
		{"clean + blocked, bot APPROVED review -> approved via review signal", blockedBody, true, BotVerdictApproved, PanelTeamAwaitingTeam},
		{"clean + will be submitted, no review -> approved", approvedBody, false, BotVerdictApproved, PanelTeamAwaitingTeam},
		{"issues found + bot APPROVED review -> disapproved wins, blocked", problemsBody, true, BotVerdictDisapproved, PanelTeamAwaitingOwner},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			extra := map[string]any{
				"head_sha": "new",
				"comments": []map[string]any{{"id": "c1", "author": "example-review-bot", "body": tc.comment}},
			}
			if tc.withReview {
				extra["reviews"] = []map[string]any{{"id": "r1", "author": "example-review-bot", "state": "APPROVED"}}
			}
			facts := teamPRFacts(t, extra, green)
			interp, err := Interpret(facts, wiringClock, wiringConfig())
			if err != nil {
				t.Fatalf("Interpret: %v", err)
			}
			a := interp.Approvals
			if a.HumanApprovers != 0 || a.HumanApproved {
				t.Fatalf("bot counted as human: %+v", a)
			}
			if a.BotVerdict != tc.wantBot {
				t.Fatalf("BotVerdict = %q; want %q", a.BotVerdict, tc.wantBot)
			}
			if interp.Panel != tc.wantPanel {
				t.Fatalf("Panel = %q; want %q", interp.Panel, tc.wantPanel)
			}
		})
	}
}

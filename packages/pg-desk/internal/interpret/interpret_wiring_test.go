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
		ApproverAllowlist: []string{"zr-review-bot"},
		VerdictGenerations: []config.VerdictGeneration{{
			ID:                "zr-review-bot-v1",
			BodyMarker:        "<!-- review-bot -->",
			FindingsPatterns:  []string{`(?i)\*\*Decision:\*\*\s*No issues found`, `(?i)\*\*Decision:\*\*\s*Issues found`},
			AuthorityPatterns: []string{`(?i)\*\*Auto-approval:\*\*\s*will be submitted`},
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
			with(ciRun("A", "success", "new", "10", 1), ciRun("B", "success", "new", "11", 1)),
			PanelTeamAwaitingOwner,
		},
		{
			"empty head_sha string falls back to all runs -> awaiting_owner",
			map[string]any{"head_sha": ""},
			with(ciRun("A", "success", "new", "10", 1)),
			PanelTeamAwaitingOwner,
		},
		{
			"explicit null head_sha falls back -> awaiting_owner",
			map[string]any{"head_sha": nil},
			with(ciRun("A", "success", "new", "10", 1)),
			PanelTeamAwaitingOwner,
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

// Live zr-review-bot shape: a "No issues found / Auto-approval: blocked"
// comment plus an APPROVED review from the allowlisted bot is no human
// approval and no bot disapproval.
func TestInterpret_PolicyBlockedBotComment_AwaitsTeam(t *testing.T) {
	const blockedBody = "<!-- review-bot -->\n## Review Bot\n\n:white_check_mark: **Decision:** No issues found\n:no_entry: **Auto-approval:** blocked — see below"
	const problemsBody = "<!-- review-bot -->\n## Review Bot\n\n**Decision:** Issues found\n"
	const approvedBody = "<!-- review-bot -->\n## Review Bot\n\n**Decision:** No issues found\n**Auto-approval:** will be submitted"
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
				"comments": []map[string]any{{"id": "c1", "author": "zr-review-bot", "body": tc.comment}},
			}
			if tc.withReview {
				extra["reviews"] = []map[string]any{{"id": "r1", "author": "zr-review-bot", "state": "APPROVED"}}
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

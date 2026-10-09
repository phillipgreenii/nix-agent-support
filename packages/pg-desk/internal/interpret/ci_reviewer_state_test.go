package interpret

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

// Reviewer-facing CI state (operator ruling 2026-10-05): the display State and
// the owner-side ReviewState stay as they were; ReviewerState additionally
// sets aside a cancelled run that masks no real job failure, and reads "no CI
// data" as pending. These tests pin all four views of the same payload so the
// layers cannot drift apart.

// cancelledRunWithJobs is a cancelled run on the head commit carrying fetched jobs.
func cancelledRunWithJobs(name, id string, jobs ...map[string]any) map[string]any {
	r := ciRun(name, "cancelled", "new", id, 1)
	r["jobs"] = jobs
	return r
}

func inFlightRun(name, id string) map[string]any {
	return map[string]any{"name": name, "status": "in_progress", "id": id, "attempt": 1, "head_sha": "new"}
}

func TestComputeCIRollup_ReviewerState(t *testing.T) {
	exempt := []string{exemptJob}
	cases := []struct {
		name         string
		runs         []map[string]any
		exempt       []string
		wantState    string
		wantReview   string
		wantReviewer string
		wantInFlight bool
	}{
		{"cancelled-only newest run, jobs not fetched (120650)", []map[string]any{
			ciRun("EAS PR Preview", "cancelled", "new", "1", 1),
		}, nil, "failure", "failure", "success", false},
		{"cancelled run alongside passing runs", []map[string]any{
			ciRun("EAS PR Preview", "cancelled", "new", "1", 1), ciRun("Other", "success", "new", "2", 1),
		}, nil, "failure", "failure", "success", false},
		{"cancelled run with a run still in flight stays pending", []map[string]any{
			ciRun("EAS PR Preview", "cancelled", "new", "1", 1), inFlightRun("Slow", "2"),
		}, nil, "failure", "failure", "pending", true},
		{"cancelled run + a real failure in another run is still a failure", []map[string]any{
			ciRun("EAS PR Preview", "cancelled", "new", "1", 1), ciRun("Unit", "failure", "new", "2", 1),
		}, nil, "failure", "failure", "failure", false},
		{"timed_out is not a cancellation", []map[string]any{
			ciRun("Unit", "timed_out", "new", "1", 1),
		}, nil, "failure", "failure", "failure", false},
		{"cancelled run whose job really failed first is a real failure (S2 guard)", []map[string]any{
			cancelledRunWithJobs("PR Checks", "1", job("lint", "failure"), job("unit", "cancelled")),
		}, nil, "failure", "failure", "failure", false},
		{"cancelled run whose jobs are all cancelled is soft", []map[string]any{
			cancelledRunWithJobs("PR Checks", "1", job("lint", "cancelled"), job("unit", "cancelled"), job("docs", "success")),
		}, nil, "failure", "failure", "success", false},
		{"cancelled run whose only real failure is an exempt job is soft for the reviewer only", []map[string]any{
			cancelledRunWithJobs("PR Checks", "1", job(exemptJob, "failure"), job("unit", "cancelled")),
		}, exempt, "failure", "failure", "success", false},
		{"cancelled run + exempt-provable failed run: reviewer sees through both", []map[string]any{
			cancelledRunWithJobs("Cancelled", "1", job("unit", "cancelled")),
			failedRunWithJobs("PR Checks", "2", job(exemptJob, "failure")),
		}, exempt, "failure", "failure", "success", false},
		{"newest cancelled run supersedes an older real failure of the same workflow (accepted edge)", []map[string]any{
			ciRun("PR Checks", "failure", "new", "1", 1), ciRun("PR Checks", "cancelled", "new", "2", 1),
		}, nil, "failure", "failure", "success", false},
		{"cancelled run on an OLD sha is ignored", []map[string]any{
			ciRun("A", "success", "new", "10", 1), ciRun("A", "cancelled", "old", "5", 1),
		}, nil, "success", "success", "success", false},
		{"exempt-only failure still softens both views (2026-10-02 unchanged)", []map[string]any{
			failedRunWithJobs("PR Checks", "1", job(exemptJob, "failure")),
		}, exempt, "failure", "success", "success", false},
		{"exempt failure without fetched jobs is not provable, so it still blocks both", []map[string]any{
			ciRun("PR Checks", "failure", "new", "1", 1),
		}, exempt, "failure", "failure", "failure", false},
		{"real failure alongside in-flight run reports in flight", []map[string]any{
			ciRun("Unit", "failure", "new", "1", 1), inFlightRun("Slow", "2"),
		}, nil, "failure", "failure", "failure", true},
		{"no runs at all: display none, owner side none, reviewer pending", nil, nil, "none", "none", "pending", false},
		{"only in-flight runs", []map[string]any{inFlightRun("Slow", "1")}, nil, "pending", "pending", "pending", true},
		{"all green", []map[string]any{ciRun("A", "success", "new", "1", 1)}, nil, "success", "success", "success", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rollupOf(t, tc.runs, tc.exempt)
			if got.State != tc.wantState {
				t.Errorf("State = %q; want %q", got.State, tc.wantState)
			}
			if got.ReviewState() != tc.wantReview {
				t.Errorf("ReviewState = %q; want %q", got.ReviewState(), tc.wantReview)
			}
			if got.ReviewerState() != tc.wantReviewer {
				t.Errorf("ReviewerState = %q; want %q", got.ReviewerState(), tc.wantReviewer)
			}
			if got.RunsInFlight() != tc.wantInFlight {
				t.Errorf("RunsInFlight = %v; want %v", got.RunsInFlight(), tc.wantInFlight)
			}
		})
	}
}

// Runs excluded by check_interpreters leave the rollup empty, which is "none".
func TestComputeCIRollup_AllRunsExcludedIsNone(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"runs": []map[string]any{ciRun("gate: approval", "failure", "new", "1", 1)}})
	got := computeCIRollup(raw, []config.CheckInterpreterConfig{{Patterns: []string{"^gate"}, Type: "approval-gate"}}, "new", nil)
	if got.State != "none" || got.ReviewState() != "none" || got.ReviewerState() != "pending" {
		t.Fatalf("got State=%q ReviewState=%q ReviewerState=%q; want none/none/pending", got.State, got.ReviewState(), got.ReviewerState())
	}
}

// Hand-built rollups (used throughout the classifier tests) must agree with
// what computeCIRollup would produce for the same State.
func TestCIRollupResult_DirectConstructionDefaults(t *testing.T) {
	for _, tc := range []struct {
		r            ciRollupResult
		wantReviewer string
		wantInFlight bool
	}{
		{ciRollupResult{State: "none"}, "pending", false},
		{ciRollupResult{State: "pending"}, "pending", true},
		{ciRollupResult{State: "success"}, "success", false},
		{ciRollupResult{State: "failure"}, "failure", false},
		{ciRollupResult{State: "failure", reviewState: "success"}, "success", false},
	} {
		if got := tc.r.ReviewerState(); got != tc.wantReviewer {
			t.Errorf("%+v ReviewerState = %q; want %q", tc.r, got, tc.wantReviewer)
		}
		if got := tc.r.RunsInFlight(); got != tc.wantInFlight {
			t.Errorf("%+v RunsInFlight = %v; want %v", tc.r, got, tc.wantInFlight)
		}
	}
}

// End to end through Interpret: the real-PR scenarios behind the rulings.
func TestInterpret_ReviewerRulings_EndToEnd(t *testing.T) {
	const problemsBody = "<!-- example-review-bot -->\nResult: findings\n"
	green := []map[string]any{ciRun("A", "success", "new", "1", 1)}
	humanApproval := []map[string]any{{"id": "r1", "author": "alice", "state": "APPROVED"}}
	botComment := []map[string]any{{"id": "c1", "author": "example-review-bot", "body": problemsBody}}

	cases := []struct {
		name  string
		extra map[string]any
		runs  []map[string]any
		want  string
	}{
		{
			"cancelled-only run, requested of me -> awaiting me (120650)",
			map[string]any{"head_sha": "new", "review_requests": []string{"me"}, "merge_state_status": "BLOCKED"},
			[]map[string]any{ciRun("EAS PR Preview", "cancelled", "new", "1", 1), ciRun("A", "success", "new", "2", 1)},
			PanelTeamAwaitingMe,
		},
		{
			"no workflow runs at all, unapproved -> awaiting team (120536)",
			map[string]any{"head_sha": "new", "merge_state_status": "BLOCKED", "review_requests": []string{"some-team"}},
			nil,
			PanelTeamAwaitingTeam,
		},
		{
			"no workflow runs at all, approved, BLOCKED -> awaiting team",
			map[string]any{"head_sha": "new", "merge_state_status": "BLOCKED", "reviews": humanApproval},
			nil,
			PanelTeamAwaitingTeam,
		},
		{
			"approved, UNKNOWN merge state, two requesters pending -> awaiting team (112341)",
			map[string]any{"head_sha": "new", "merge_state_status": "UNKNOWN", "review_requests": []string{"bob", "carol"}, "reviews": humanApproval},
			green,
			PanelTeamAwaitingTeam,
		},
		{
			"approved, BLOCKED, requesters pending -> awaiting team (120115)",
			map[string]any{"head_sha": "new", "merge_state_status": "BLOCKED", "review_requests": []string{"bob"}, "reviews": humanApproval},
			green,
			PanelTeamAwaitingTeam,
		},
		{
			"approved, CLEAN, a requester still listed -> awaiting owner (120192)",
			map[string]any{"head_sha": "new", "merge_state_status": "CLEAN", "review_requests": []string{"bob"}, "reviews": humanApproval},
			green,
			PanelTeamAwaitingOwner,
		},
		{
			"approved, BLOCKED, required CI still running -> awaiting owner",
			map[string]any{"head_sha": "new", "merge_state_status": "BLOCKED", "reviews": humanApproval},
			[]map[string]any{inFlightRun("Required", "1")},
			PanelTeamAwaitingOwner,
		},
		{
			"bot found issues, requested of me -> awaiting me (115159)",
			map[string]any{"head_sha": "new", "comments": botComment, "review_requests": []string{"me"}},
			green,
			PanelTeamAwaitingMe,
		},
		{
			"bot found issues, NOT requested of me -> awaiting owner (113985)",
			map[string]any{"head_sha": "new", "comments": botComment, "review_requests": []string{"bob"}},
			green,
			PanelTeamAwaitingOwner,
		},
		{
			"bot found issues + real CI failure, requested of me -> awaiting owner (120572)",
			map[string]any{"head_sha": "new", "comments": botComment, "review_requests": []string{"me"}},
			[]map[string]any{ciRun("PR Checks", "failure", "new", "1", 1)},
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

// A cancelled run no longer blocks a reviewer, but it is still a failure in
// every DISPLAY of CI: the urgency signal is unchanged.
func TestInterpret_CancelledRun_StillCountsAsCIFailingSignal(t *testing.T) {
	runs := []map[string]any{ciRun("EAS PR Preview", "cancelled", "new", "1", 1)}
	interp, err := Interpret(teamPRFacts(t, map[string]any{"head_sha": "new"}, runs), wiringClock, wiringConfig())
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	if interp.Panel != PanelTeamAwaitingTeam {
		t.Fatalf("Panel = %q; want %q", interp.Panel, PanelTeamAwaitingTeam)
	}
	if !slices.Contains(interp.Urgency.Reasons, "ci-failing") {
		t.Fatalf("Urgency.Reasons = %v; want it to still carry ci-failing", interp.Urgency.Reasons)
	}
}

// The owner-side decisions must not inherit the reviewer-only softenings.
func TestInterpret_OwnPR_NotSoftenedByReviewerRules(t *testing.T) {
	cancelled := []map[string]any{ciRun("EAS PR Preview", "cancelled", "new", "1", 1)}
	cases := []struct {
		name        string
		extra       map[string]any
		runs        []map[string]any
		wantPanel   string
		wantPromote bool
	}{
		{"own PR, cancelled-only run -> still awaiting me", map[string]any{"head_sha": "new", "author": "me"}, cancelled, PanelMineAwaitingMe, false},
		{"own PR, no CI data -> still awaiting me", map[string]any{"head_sha": "new", "author": "me"}, nil, PanelMineAwaitingMe, false},
		{"own draft, cancelled-only run -> never ready to promote", map[string]any{"head_sha": "new", "author": "me", "draft": true}, cancelled, PanelMineAwaitingMe, false},
		{"own draft, no CI data -> never ready to promote", map[string]any{"head_sha": "new", "author": "me", "draft": true}, nil, PanelMineAwaitingMe, false},
		{"own draft, green -> ready to promote, awaiting me (2026-10-09: a draft is never awaiting team)", map[string]any{"head_sha": "new", "author": "me", "draft": true}, []map[string]any{ciRun("A", "success", "new", "1", 1)}, PanelMineAwaitingMe, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			interp, err := Interpret(teamPRFacts(t, tc.extra, tc.runs), wiringClock, wiringConfig())
			if err != nil {
				t.Fatalf("Interpret: %v", err)
			}
			if interp.Panel != tc.wantPanel {
				t.Errorf("Panel = %q; want %q", interp.Panel, tc.wantPanel)
			}
			if interp.ReadyToPromote != tc.wantPromote {
				t.Errorf("ReadyToPromote = %v; want %v", interp.ReadyToPromote, tc.wantPromote)
			}
		})
	}
}

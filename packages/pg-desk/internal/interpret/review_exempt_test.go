package interpret

import (
	"encoding/json"
	"testing"
)

// Review-exempt checks (config review_exempt_checks, bead pg2-p2ojd): a PR whose
// ONLY failing checks are exempt JOBS stays reviewable while its CI rollup
// still reads "failure". Fixture job names are generic placeholders.

const exemptJob = "slow-nightly"

func job(name, conclusion string) map[string]any {
	return map[string]any{"name": name, "status": "completed", "conclusion": conclusion}
}

// failedRunWithJobs is a failed run on the head commit carrying fetched jobs.
func failedRunWithJobs(name, id string, jobs ...map[string]any) map[string]any {
	r := ciRun(name, "failure", "new", id, 1)
	r["jobs"] = jobs
	return r
}

func rollupOf(t *testing.T, runs []map[string]any, exempt []string) ciRollupResult {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"runs": runs})
	return computeCIRollup(raw, nil, "new", exempt)
}

func TestComputeCIRollup_ReviewExempt(t *testing.T) {
	exempt := []string{exemptJob}
	cases := []struct {
		name        string
		runs        []map[string]any
		exempt      []string
		wantState   string
		wantReviews string
	}{
		{"only exempt job failed", []map[string]any{
			failedRunWithJobs("PR Checks", "1", job(exemptJob, "failure"), job("lint", "success")),
		}, exempt, "failure", "success"},
		{"exempt + other job failed in the same run", []map[string]any{
			failedRunWithJobs("PR Checks", "1", job(exemptJob, "failure"), job("lint", "failure")),
		}, exempt, "failure", "failure"},
		{"exempt failed in one run, other run failed", []map[string]any{
			failedRunWithJobs("PR Checks", "1", job(exemptJob, "failure")),
			ciRun("Other", "failure", "new", "2", 1),
		}, exempt, "failure", "failure"},
		{"empty list is today's behavior", []map[string]any{
			failedRunWithJobs("PR Checks", "1", job(exemptJob, "failure")),
		}, nil, "failure", "failure"},
		{"jobs not fetched is not provably exempt", []map[string]any{
			ciRun("PR Checks", "failure", "new", "1", 1),
		}, exempt, "failure", "failure"},
		{"jobs present but none failed is not provably exempt", []map[string]any{
			failedRunWithJobs("PR Checks", "1", job(exemptJob, "success"), job("lint", "skipped")),
		}, exempt, "failure", "failure"},
		{"a cancelled exempt job counts as a failed exempt job", []map[string]any{
			failedRunWithJobs("PR Checks", "1", job(exemptJob, "cancelled")),
		}, exempt, "failure", "success"},
		{"exact match only: substring and case differ", []map[string]any{
			failedRunWithJobs("PR Checks", "1", job("Slow-Nightly", "failure")),
			failedRunWithJobs("Second", "2", job(exemptJob+"-extra", "failure")),
		}, exempt, "failure", "failure"},
		{"run name is never matched, only job names", []map[string]any{
			failedRunWithJobs(exemptJob, "1", job("lint", "failure")),
		}, exempt, "failure", "failure"},
		{"exempt failure alongside a run still in flight stays pending", []map[string]any{
			failedRunWithJobs("PR Checks", "1", job(exemptJob, "failure")),
			{"name": "Slow", "status": "in_progress", "id": "3", "attempt": 1, "head_sha": "new"},
		}, exempt, "failure", "pending"},
		{"exempt failure alongside passing runs", []map[string]any{
			failedRunWithJobs("PR Checks", "1", job(exemptJob, "failure")),
			ciRun("Other", "success", "new", "2", 1),
		}, exempt, "failure", "success"},
		{"no failure: review state equals state", []map[string]any{
			ciRun("A", "success", "new", "1", 1),
		}, exempt, "success", "success"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rollupOf(t, tc.runs, tc.exempt)
			if got.State != tc.wantState {
				t.Errorf("State = %q; want %q", got.State, tc.wantState)
			}
			if got.ReviewState() != tc.wantReviews {
				t.Errorf("ReviewState = %q; want %q", got.ReviewState(), tc.wantReviews)
			}
		})
	}
}

// The three bead cases end to end through Interpret, for a team PR (panel) and
// an own draft PR (ready-to-promote): the review-blocking decisions follow
// ReviewState, the rollup itself is unchanged.
func TestInterpret_ReviewExemptChecks(t *testing.T) {
	onlyExempt := []map[string]any{
		failedRunWithJobs("PR Checks", "1", job(exemptJob, "failure"), job("lint", "success")),
	}
	exemptAndOther := []map[string]any{
		failedRunWithJobs("PR Checks", "1", job(exemptJob, "failure"), job("lint", "failure")),
	}
	notFetched := []map[string]any{ciRun("PR Checks", "failure", "new", "1", 1)}

	cases := []struct {
		name        string
		runs        []map[string]any
		exempt      []string
		wantPanel   string
		wantPromote bool
	}{
		{"only exempt failed -> reviewable", onlyExempt, []string{exemptJob}, PanelTeamAwaitingTeam, true},
		{"exempt + other failed -> not reviewable", exemptAndOther, []string{exemptJob}, PanelTeamAwaitingOwner, false},
		{"empty list -> today's behavior", onlyExempt, nil, PanelTeamAwaitingOwner, false},
		{"jobs not fetched -> stays blocked", notFetched, []string{exemptJob}, PanelTeamAwaitingOwner, false},
	}
	for _, tc := range cases {
		cfg := wiringConfig()
		cfg.ReviewExemptChecks = tc.exempt
		t.Run(tc.name+" (team panel)", func(t *testing.T) {
			interp, err := Interpret(teamPRFacts(t, map[string]any{"head_sha": "new"}, tc.runs), wiringClock, cfg)
			if err != nil {
				t.Fatalf("Interpret: %v", err)
			}
			if interp.Panel != tc.wantPanel {
				t.Fatalf("Panel = %q; want %q", interp.Panel, tc.wantPanel)
			}
		})
		t.Run(tc.name+" (own draft promotion)", func(t *testing.T) {
			facts := teamPRFacts(t, map[string]any{"head_sha": "new", "author": "me", "draft": true}, tc.runs)
			interp, err := Interpret(facts, wiringClock, cfg)
			if err != nil {
				t.Fatalf("Interpret: %v", err)
			}
			if interp.ReadyToPromote != tc.wantPromote {
				t.Fatalf("ReadyToPromote = %v; want %v", interp.ReadyToPromote, tc.wantPromote)
			}
		})
	}
}

// The exemption does not touch the urgency "ci-failing" signal or any other
// blocker: a conflicting PR whose only CI failure is exempt is still blocked.
func TestInterpret_ReviewExempt_OtherBlockersStillBlock(t *testing.T) {
	cfg := wiringConfig()
	cfg.ReviewExemptChecks = []string{exemptJob}
	runs := []map[string]any{failedRunWithJobs("PR Checks", "1", job(exemptJob, "failure"))}
	interp, err := Interpret(teamPRFacts(t, map[string]any{"head_sha": "new", "mergeable": "CONFLICTING"}, runs), wiringClock, cfg)
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	if interp.Panel != PanelTeamAwaitingOwner {
		t.Fatalf("Panel = %q; want %q", interp.Panel, PanelTeamAwaitingOwner)
	}
}

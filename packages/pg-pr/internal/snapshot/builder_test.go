package snapshot

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// TestBuildSplitsMineFromReview verifies the partition under the broadened "PRs
// to Review" contract (6b/B5): PRs authored by Self go to Mine (even drafts);
// every OTHER non-draft PR that still carries a live match reason (team-authored
// ∪ requested ∪ labeled) goes to the review set (Team). Reasons are sourced from
// ingest; the builder re-checks they hold (B5 review #1). Others' drafts, and
// non-mine PRs with no live reason, are excluded. LinesChanged = Additions +
// Deletions.
func TestBuildSplitsMineFromReview(t *testing.T) {
	reg, _ := agentregistry.New(nil) // empty registry

	now := time.Now()
	in := BuilderInput{
		GeneratedAt:         now,
		SyncIntervalSeconds: 60,
		Self:                "alice",
		TeamMembers:         []string{"bob", "carol"},
		Registry:            reg,
		PRs: []PRInput{
			// Mine: authored by Self
			{PR: api.PR{Repo: "org/repo", Number: 1, Author: "alice", Title: "my PR", URL: "u1"}},
			// To-review: non-draft team member
			{PR: api.PR{Repo: "org/repo", Number: 2, Author: "bob", Title: "bob PR", URL: "u2", Draft: false, Additions: 10, Deletions: 5, ChangedFiles: 3}},
			// Excluded: a DRAFT that isn't mine
			{PR: api.PR{Repo: "org/repo", Number: 3, Author: "carol", Title: "carol draft", URL: "u3", Draft: true}},
			// To-review: non-team, non-self, non-draft — ingest surfaced it because
			// review was requested of me (a live reason, so it survives the B5 guard)
			{PR: api.PR{Repo: "org/repo", Number: 4, Author: "zara", Title: "review PR", URL: "u4", ReviewRequestedOfMe: true}},
		},
	}

	snap := Build(in)

	if len(snap.Mine) != 1 || snap.Mine[0].Number != 1 {
		t.Fatalf("expected Mine=[#1], got %+v", snap.Mine)
	}
	got := map[int]bool{}
	for _, r := range snap.Team {
		got[r.Number] = true
	}
	if len(snap.Team) != 2 || !got[2] || !got[4] {
		t.Fatalf("expected PRs-to-Review = {#2,#4}, got %+v", snap.Team)
	}
	if got[3] {
		t.Errorf("a non-mine draft must be excluded from PRs to Review")
	}
	for _, r := range snap.Team {
		if r.Number == 2 && (r.LinesChanged != 15 || r.FilesChanged != 3) {
			t.Errorf("bob row: LinesChanged=%d FilesChanged=%d, want 15/3", r.LinesChanged, r.FilesChanged)
		}
	}
}

// TestBuild_MatchReasons: MatchReason explains why each PR is in the review set —
// team-authored, review-requested (ReviewRequestedOfMe), one label:<name> per
// matched watch label — and a PR matching several criteria carries all reasons.
func TestBuild_MatchReasons(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	in := BuilderInput{
		Self:        "alice",
		TeamMembers: []string{"bob"},
		WatchLabels: []string{"team/findev", "team/jvm-guild"},
		Registry:    reg,
		PRs: []PRInput{
			{PR: api.PR{Repo: "o/r", Number: 2, Author: "bob"}},                                                                // team-authored
			{PR: api.PR{Repo: "o/r", Number: 5, Author: "zara", ReviewRequestedOfMe: true}},                                    // requested
			{PR: api.PR{Repo: "o/r", Number: 6, Author: "yin", Labels: []string{"team/findev", "unrelated"}}},                  // labeled
			{PR: api.PR{Repo: "o/r", Number: 7, Author: "bob", ReviewRequestedOfMe: true, Labels: []string{"team/jvm-guild"}}}, // all three
		},
	}
	snap := Build(in)
	reasons := map[int][]string{}
	for _, r := range snap.Team {
		reasons[r.Number] = r.MatchReason
	}
	assertReasons(t, reasons[2], []string{"team-authored"})
	assertReasons(t, reasons[5], []string{"review-requested"})
	assertReasons(t, reasons[6], []string{"label:team/findev"})
	assertReasons(t, reasons[7], []string{"team-authored", "review-requested", "label:team/jvm-guild"})
}

func assertReasons(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MatchReason = %#v, want %#v", got, want)
	}
}

// TestBuild_MinePRStaysMineEvenDraft: my own PR is always Mine, never the review
// set, even as a draft (Q6 — exclude-mine applies only to the broadened criteria;
// mine is still self-reviewed elsewhere).
func TestBuild_MinePRStaysMineEvenDraft(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	snap := Build(BuilderInput{
		Self:     "alice",
		Registry: reg,
		PRs:      []PRInput{{PR: api.PR{Repo: "o/r", Number: 1, Author: "alice", Draft: true}}},
	})
	if len(snap.Mine) != 1 || len(snap.Team) != 0 {
		t.Errorf("my draft PR must be Mine, not review set: mine=%+v team=%+v", snap.Mine, snap.Team)
	}
}

// TestBuildExcludesReasonlessReviewPR verifies the self-correcting membership
// guard (pg2-ynhr.13 B5 review #1): a non-mine, non-draft PR that carries NO
// qualifying match reason — not team-authored, not review-requested, no watch
// label — is EXCLUDED from the "PRs to Review" set. This is the removal path for
// a PR that ENTERED the set (labeled/requested) then lost the qualifier while
// still open+non-draft; without the guard it would linger with an empty reason.
func TestBuildExcludesReasonlessReviewPR(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	snap := Build(BuilderInput{
		Self:        "alice",
		TeamMembers: []string{"bob"},
		WatchLabels: []string{"team/findev"},
		Registry:    reg,
		PRs: []PRInput{
			// non-mine, non-draft, but NO reason: author is not on the team, not
			// requested of me, and carries no watch label.
			{PR: api.PR{Repo: "o/r", Number: 8, Author: "zara", Labels: []string{"unrelated"}}},
		},
	})
	if len(snap.Team) != 0 {
		t.Errorf("a reasonless non-mine PR must be excluded from PRs to Review; got %+v", snap.Team)
	}
}

// TestBuildDerivesApprovalAndWaiting verifies:
// - human_approved from a non-agent APPROVED review
// - agent_approved from an agent's review body matching the approval regex
// - ci_status=success when all runs pass
// - waiting_on_me derived from beads dep set
func TestBuildDerivesApprovalAndWaiting(t *testing.T) {
	reg, err := agentregistry.New([]agentregistry.Entry{
		{Login: "claude[bot]", ApprovalRegex: `(?im)^verdict:\s*approve`},
	})
	if err != nil {
		t.Fatalf("agentregistry.New: %v", err)
	}

	deps := []beads.DepNode{
		{ID: "T-1", Title: "human task", Status: "open", Labels: []string{"human"}},
	}

	in := BuilderInput{
		GeneratedAt:         time.Now(),
		SyncIntervalSeconds: 30,
		Self:                "alice",
		TeamMembers:         []string{},
		Registry:            reg,
		PRs: []PRInput{
			{
				PR: api.PR{Repo: "org/repo", Number: 5, Author: "alice", Title: "feat", URL: "u5"},
				Reviews: []api.Review{
					{ID: "r1", Author: "humanreviewer", State: "APPROVED", Body: "LGTM"},
					{ID: "r2", Author: "claude[bot]", State: "APPROVED", Body: "Verdict: approve\nDetails here"},
				},
				CIRuns: []api.CIRun{
					{ID: "ci1", Status: "completed", Conclusion: "success"},
					{ID: "ci2", Status: "completed", Conclusion: "success"},
				},
				BeadsDeps: deps,
			},
		},
	}

	snap := Build(in)

	if len(snap.Mine) != 1 {
		t.Fatalf("expected 1 Mine row, got %d", len(snap.Mine))
	}
	row := snap.Mine[0]

	if !row.HumanApproved {
		t.Error("expected HumanApproved=true")
	}
	if !row.AgentApproved {
		t.Error("expected AgentApproved=true")
	}
	if row.CIStatus != "success" {
		t.Errorf("expected CIStatus=success, got %q", row.CIStatus)
	}
	if !row.WaitingOnMe {
		t.Error("expected WaitingOnMe=true (open dep with human label)")
	}
}

// TestBuildEmptyArraysNotNil verifies that JIRA and Beads fields are
// initialised as empty slices, not nil — important for JSON serialisation
// ([] vs null).
func TestBuildEmptyArraysNotNil(t *testing.T) {
	reg, _ := agentregistry.New(nil)

	in := BuilderInput{
		GeneratedAt:         time.Now(),
		SyncIntervalSeconds: 60,
		Self:                "alice",
		TeamMembers:         []string{"bob"},
		Registry:            reg,
		PRs: []PRInput{
			{
				PR: api.PR{Repo: "org/repo", Number: 10, Author: "alice", Title: "empty", URL: "u10"},
				// No JIRA, no BeadsDeps
			},
			{
				PR: api.PR{Repo: "org/repo", Number: 11, Author: "bob", Title: "team empty", URL: "u11"},
				// No JIRA
			},
		},
	}

	snap := Build(in)

	if len(snap.Mine) != 1 {
		t.Fatalf("expected 1 Mine row, got %d", len(snap.Mine))
	}
	if len(snap.Team) != 1 {
		t.Fatalf("expected 1 Team row, got %d", len(snap.Team))
	}

	// JSON round-trip: nil slices marshal as null, empty slices as [].
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	s := string(b)
	// Ensure we don't see "null" for jira or beads fields.
	// The easiest check: the JSON should not contain `:null` for these fields.
	// We specifically check Mine[0].jira and Mine[0].beads and Team[0].jira.
	var raw map[string]interface{}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	_ = s

	mine := raw["mine"].([]interface{})
	mineRow := mine[0].(map[string]interface{})
	if mineRow["jira"] == nil {
		t.Error("Mine jira field must not be null")
	}
	if mineRow["beads"] == nil {
		t.Error("Mine beads field must not be null")
	}

	team := raw["team"].([]interface{})
	teamRow := team[0].(map[string]interface{})
	if teamRow["jira"] == nil {
		t.Error("Team jira field must not be null")
	}
}

// TestBuildAgentApprovedViaInlineCommentIgnored verifies that an agent comment
// with an approval body but with Path/Line set (inline diff comment) does NOT
// trigger agent_approved.
func TestBuildAgentApprovedViaInlineCommentIgnored(t *testing.T) {
	reg, err := agentregistry.New([]agentregistry.Entry{
		{Login: "claude[bot]", ApprovalRegex: `(?im)^verdict:\s*approve`},
	})
	if err != nil {
		t.Fatalf("agentregistry.New: %v", err)
	}

	in := BuilderInput{
		GeneratedAt:         time.Now(),
		SyncIntervalSeconds: 60,
		Self:                "alice",
		TeamMembers:         []string{},
		Registry:            reg,
		PRs: []PRInput{
			{
				PR: api.PR{Repo: "org/repo", Number: 20, Author: "alice", Title: "test", URL: "u20"},
				// Inline comment — should be ignored for approval
				Comments: []api.Comment{
					{Author: "claude[bot]", Body: "Verdict: approve", Path: "foo.go", Line: 42},
				},
				// Reviews without a body that matches the approval regex
				Reviews: []api.Review{
					{ID: "r3", Author: "claude[bot]", State: "CHANGES_REQUESTED", Body: "needs work"},
				},
			},
		},
	}

	snap := Build(in)

	if len(snap.Mine) != 1 {
		t.Fatalf("expected 1 Mine row, got %d", len(snap.Mine))
	}
	row := snap.Mine[0]

	if row.AgentApproved {
		t.Error("AgentApproved must be false: inline comment approval should be ignored")
	}
	if row.HumanApproved {
		t.Error("HumanApproved must be false: no human approval")
	}
}

// TestRollupCI is a table-driven test covering all four rollupCI states.
func TestRollupCI(t *testing.T) {
	cases := []struct {
		name     string
		runs     []api.CIRun
		expected string
	}{
		{
			name:     "none when empty",
			runs:     []api.CIRun{},
			expected: "none",
		},
		{
			name: "success when all completed success",
			runs: []api.CIRun{
				{Status: "completed", Conclusion: "success"},
				{Status: "completed", Conclusion: "success"},
			},
			expected: "success",
		},
		{
			name: "failure on any failure",
			runs: []api.CIRun{
				{Status: "completed", Conclusion: "success"},
				{Status: "completed", Conclusion: "failure"},
			},
			expected: "failure",
		},
		{
			name: "failure on cancelled",
			runs: []api.CIRun{
				{Status: "completed", Conclusion: "cancelled"},
			},
			expected: "failure",
		},
		{
			name: "failure on timed_out",
			runs: []api.CIRun{
				{Status: "completed", Conclusion: "timed_out"},
			},
			expected: "failure",
		},
		{
			name: "pending when in_progress",
			runs: []api.CIRun{
				{Status: "in_progress", Conclusion: ""},
			},
			expected: "pending",
		},
		{
			name: "pending when queued",
			runs: []api.CIRun{
				{Status: "queued", Conclusion: ""},
			},
			expected: "pending",
		},
		{
			name: "pending when conclusion empty",
			runs: []api.CIRun{
				{Status: "completed", Conclusion: "success"},
				{Status: "completed", Conclusion: ""},
			},
			expected: "pending",
		},
		{
			name: "failure beats pending",
			runs: []api.CIRun{
				{Status: "in_progress", Conclusion: ""},
				{Status: "completed", Conclusion: "failure"},
			},
			expected: "failure",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rollupCI(tc.runs)
			if got != tc.expected {
				t.Errorf("rollupCI(%v) = %q, want %q", tc.runs, got, tc.expected)
			}
		})
	}
}

// TestBuildNilRegistry verifies that when Registry is nil all approvers count
// as human and there is no panic.
func TestBuildNilRegistry(t *testing.T) {
	in := BuilderInput{
		GeneratedAt:         time.Now(),
		SyncIntervalSeconds: 60,
		Self:                "alice",
		TeamMembers:         []string{},
		Registry:            nil, // explicit nil
		PRs: []PRInput{
			{
				PR: api.PR{Repo: "org/repo", Number: 30, Author: "alice", Title: "nil-reg", URL: "u30"},
				Reviews: []api.Review{
					{ID: "r4", Author: "anyone", State: "APPROVED", Body: ""},
					{ID: "r5", Author: "claude[bot]", State: "APPROVED", Body: "Verdict: approve"},
				},
			},
		},
	}

	// Must not panic.
	snap := Build(in)

	if len(snap.Mine) != 1 {
		t.Fatalf("expected 1 Mine row, got %d", len(snap.Mine))
	}
	row := snap.Mine[0]

	if !row.HumanApproved {
		t.Error("expected HumanApproved=true when registry is nil")
	}
	// AgentApproved stays false because with nil registry we only set human.
	if row.AgentApproved {
		t.Error("expected AgentApproved=false when registry is nil")
	}
}

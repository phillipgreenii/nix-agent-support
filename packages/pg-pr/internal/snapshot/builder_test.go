package snapshot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// TestBuildSplitsMineFromTeam verifies partitioning: PRs authored by Self go
// to Mine; non-draft team-member PRs go to Team; draft team PRs and
// non-self/non-team PRs are excluded. Also checks that LinesChanged equals
// Additions + Deletions.
func TestBuildSplitsMineFromTeam(t *testing.T) {
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
			// Team: non-draft team member
			{PR: api.PR{Repo: "org/repo", Number: 2, Author: "bob", Title: "bob PR", URL: "u2", Draft: false, Additions: 10, Deletions: 5, ChangedFiles: 3}},
			// Excluded: draft team member
			{PR: api.PR{Repo: "org/repo", Number: 3, Author: "carol", Title: "carol draft", URL: "u3", Draft: true}},
			// Excluded: non-team, non-self
			{PR: api.PR{Repo: "org/repo", Number: 4, Author: "zara", Title: "outsider PR", URL: "u4"}},
		},
	}

	snap := Build(in)

	if len(snap.Mine) != 1 {
		t.Fatalf("expected 1 Mine row, got %d", len(snap.Mine))
	}
	if snap.Mine[0].Number != 1 {
		t.Errorf("Mine row should be PR#1, got #%d", snap.Mine[0].Number)
	}

	if len(snap.Team) != 1 {
		t.Fatalf("expected 1 Team row, got %d", len(snap.Team))
	}
	if snap.Team[0].Number != 2 {
		t.Errorf("Team row should be PR#2, got #%d", snap.Team[0].Number)
	}
	if snap.Team[0].LinesChanged != 15 {
		t.Errorf("LinesChanged should be 15 (10+5), got %d", snap.Team[0].LinesChanged)
	}
	if snap.Team[0].FilesChanged != 3 {
		t.Errorf("FilesChanged should be 3, got %d", snap.Team[0].FilesChanged)
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

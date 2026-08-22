package snapshot

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
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
			{PR: api.PR{Repo: "org/repo", Number: 1, Author: "alice", Title: "my PR", URL: "u1"}, Ownership: ownership.Mine},
			// To-review: non-draft team member
			{PR: api.PR{Repo: "org/repo", Number: 2, Author: "bob", Title: "bob PR", URL: "u2", Draft: false, Additions: 10, Deletions: 5, ChangedFiles: 3}, Ownership: ownership.Team},
			// Excluded: a DRAFT that isn't mine
			{PR: api.PR{Repo: "org/repo", Number: 3, Author: "carol", Title: "carol draft", URL: "u3", Draft: true}, Ownership: ownership.Team},
			// To-review: non-team, non-self, non-draft — ingest surfaced it because
			// review was requested of me (a live reason, so it survives the B5 guard)
			{PR: api.PR{Repo: "org/repo", Number: 4, Author: "zara", Title: "review PR", URL: "u4", ReviewRequestedOfMe: true}, Ownership: ownership.Team},
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
			{PR: api.PR{Repo: "o/r", Number: 2, Author: "bob"}, Ownership: ownership.Team},                                                                // team-authored
			{PR: api.PR{Repo: "o/r", Number: 5, Author: "zara", ReviewRequestedOfMe: true}, Ownership: ownership.Team},                                    // requested
			{PR: api.PR{Repo: "o/r", Number: 6, Author: "yin", Labels: []string{"team/findev", "unrelated"}}, Ownership: ownership.Team},                  // labeled
			{PR: api.PR{Repo: "o/r", Number: 7, Author: "bob", ReviewRequestedOfMe: true, Labels: []string{"team/jvm-guild"}}, Ownership: ownership.Team}, // all three
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
		PRs:      []PRInput{{PR: api.PR{Repo: "o/r", Number: 1, Author: "alice", Draft: true}, Ownership: ownership.Mine}},
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
			{PR: api.PR{Repo: "o/r", Number: 8, Author: "zara", Labels: []string{"unrelated"}}, Ownership: ownership.Team},
		},
	})
	if len(snap.Team) != 0 {
		t.Errorf("a reasonless non-mine PR must be excluded from PRs to Review; got %+v", snap.Team)
	}
}

// TestBuildDerivesApprovalAndWaiting verifies:
//   - human_approved from a non-agent approver's PER-APPROVER row (the read path
//     since pg2-4dz88.1.9 — a live APPROVED review reaches the snapshot through
//     the row internal/sync/ingest.go writes for it, not through the review
//     object)
//   - agent_approved from an agent's review body matching the approval regex —
//     the legacy fallback, retained for a registry agent the store has NO row
//     for (see classifyApprovals's doc)
//   - the ONE human + ONE agent shape that renders as `human,agent`, so this
//     pair is exactly the fixture cmd/pg-pr's rendering contract rests on
//   - ci_status=success when all runs pass
//   - waiting_on_me derived from beads dep set
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
				PR:        api.PR{Repo: "org/repo", Number: 5, Author: "alice", Title: "feat", URL: "u5", HeadSHA: "h1"},
				Ownership: ownership.Mine,
				Approvals: []store.Approval{
					{Approver: "humanreviewer", State: "approved", HeadSHA: "h1"},
				},
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
	// The single-approver-per-class shape: one of each, never two. This is the
	// snapshot half of the `human,agent` backward-compatibility contract
	// cmd/pg-pr/open_test.go asserts byte-for-byte on the rendered column.
	if row.HumanApprovers != 1 || row.AgentApprovers != 1 {
		t.Errorf("approver counts = human %d / agent %d, want 1/1", row.HumanApprovers, row.AgentApprovers)
	}
	if row.CIStatus != "success" {
		t.Errorf("expected CIStatus=success, got %q", row.CIStatus)
	}
	if !row.WaitingOnMe {
		t.Error("expected WaitingOnMe=true (open dep with human label)")
	}
}

// TestBuildCountsTwoApproversAsTwo is the core INV-APPROVAL-1 assertion this
// leaf exists for: TWO distinct approvers approving reports TWO, not one. The
// retired (human, agent) boolean pair structurally could not express it — both
// fixtures below set exactly the same two bits.
func TestBuildCountsTwoApproversAsTwo(t *testing.T) {
	reg, err := agentregistry.New([]agentregistry.Entry{
		{Login: "claude[bot]", ApprovalRegex: `(?im)^verdict:\s*approve`},
	})
	if err != nil {
		t.Fatalf("agentregistry.New: %v", err)
	}
	mk := func(approvals []store.Approval) MineRow {
		snap := Build(BuilderInput{
			Self:     "alice",
			Registry: reg,
			PRs: []PRInput{{
				PR:        api.PR{Repo: "o/r", Number: 1, Author: "alice", HeadSHA: "h1"},
				Ownership: ownership.Mine,
				Approvals: approvals,
			}},
		})
		if len(snap.Mine) != 1 {
			t.Fatalf("want 1 mine row, got %d", len(snap.Mine))
		}
		return snap.Mine[0]
	}

	one := mk([]store.Approval{
		{Approver: "carol", State: "approved", HeadSHA: "h1"},
	})
	if one.HumanApprovers != 1 {
		t.Errorf("one human approver: HumanApprovers = %d, want 1", one.HumanApprovers)
	}

	two := mk([]store.Approval{
		{Approver: "carol", State: "approved", HeadSHA: "h1"},
		{Approver: "dave", State: "approved", HeadSHA: "h1"},
	})
	if two.HumanApprovers != 2 {
		t.Errorf("two human approvers: HumanApprovers = %d, want 2 — approvals must not collapse", two.HumanApprovers)
	}
	// The retired booleans read IDENTICALLY for both fixtures, which is exactly
	// why the counts had to be added rather than the booleans reinterpreted.
	if one.HumanApproved != two.HumanApproved {
		t.Fatalf("fixture invalid: the boolean is supposed to be blind to the difference")
	}

	// Two agents split the same way, and the two classes are counted separately.
	agents := mk([]store.Approval{
		{Approver: "claude[bot]", State: "approved", HeadSHA: "h1"},
		{Approver: "carol", State: "approved", HeadSHA: "h1"},
		{Approver: "dave", State: "approved", HeadSHA: "h1"},
	})
	if agents.AgentApprovers != 1 || agents.HumanApprovers != 2 {
		t.Errorf("mixed set: agent %d / human %d, want 1/2", agents.AgentApprovers, agents.HumanApprovers)
	}
}

// A per-approver row that is not a STANDING approval MUST NOT be counted: a
// teammate asking for changes, a neutral comment, an approval of an EARLIER
// head, and one the code host dismissed. None of the four was expressible in
// the collapsed booleans (pg2-4dz88.1.9).
func TestBuildApproverCountsExcludeNonStandingRows(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	tests := []struct {
		name string
		row  store.Approval
	}{
		{"changes-requested is not an approval", store.Approval{Approver: "carol", State: "changes-requested", HeadSHA: "h1"}},
		{"commented is not an approval", store.Approval{Approver: "carol", State: "commented", HeadSHA: "h1"}},
		{"approval of an earlier head is stale", store.Approval{Approver: "carol", State: "approved", HeadSHA: "h0"}},
		{"dismissed approval is stale", store.Approval{Approver: "carol", State: "approved", HeadSHA: "h1", Dismissed: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snap := Build(BuilderInput{
				Self:     "alice",
				Registry: reg,
				PRs: []PRInput{{
					PR:        api.PR{Repo: "o/r", Number: 1, Author: "alice", HeadSHA: "h1"},
					Ownership: ownership.Mine,
					Approvals: []store.Approval{tc.row},
				}},
			})
			row := snap.Mine[0]
			if row.HumanApprovers != 0 || row.AgentApprovers != 0 || row.HumanApproved || row.AgentApproved {
				t.Errorf("counted a non-standing row: human=%d agent=%d bools=%v/%v",
					row.HumanApprovers, row.AgentApprovers, row.HumanApproved, row.AgentApproved)
			}
		})
	}
}

// One approver observed several ways counts ONCE: the store row and the legacy
// regex-mined body are the SAME login, so a per-approver count must dedupe them
// rather than double-count. This also pins the precedence: the store row is
// authoritative for a login it knows, so a body that still matches the approval
// regex cannot resurrect that login's DISMISSED approval.
func TestBuildApproverCountsDedupeAndPreferStore(t *testing.T) {
	reg, err := agentregistry.New([]agentregistry.Entry{
		{Login: "claude[bot]", ApprovalRegex: `(?im)^verdict:\s*approve`},
	})
	if err != nil {
		t.Fatalf("agentregistry.New: %v", err)
	}
	mkRow := func(approvals []store.Approval) MineRow {
		snap := Build(BuilderInput{
			Self:     "alice",
			Registry: reg,
			PRs: []PRInput{{
				PR:        api.PR{Repo: "o/r", Number: 1, Author: "alice", HeadSHA: "h1"},
				Ownership: ownership.Mine,
				Approvals: approvals,
				Comments:  []api.Comment{{Author: "claude[bot]", Body: "Verdict: approve"}},
			}},
		})
		return snap.Mine[0]
	}

	// Standing store row + matching body, same login → ONE agent approver.
	if got := mkRow([]store.Approval{
		{Approver: "claude[bot]", State: "approved", HeadSHA: "h1"},
	}).AgentApprovers; got != 1 {
		t.Errorf("AgentApprovers = %d, want 1 — one approver observed twice is still one", got)
	}

	// DISMISSED store row + still-matching body → ZERO. The store wins.
	if got := mkRow([]store.Approval{
		{Approver: "claude[bot]", State: "approved", HeadSHA: "h1", Dismissed: true},
	}).AgentApprovers; got != 0 {
		t.Errorf("AgentApprovers = %d, want 0 — a matching body must not resurrect a dismissed approval", got)
	}
}

// The legacy approval-regex fallback mines only TOP-LEVEL comment bodies. This
// pins BOTH halves — that a top-level body DOES count (the positive case the
// pre-cutover suite never asserted; its agent approval always came through a
// review body instead) and that each of the three anchored shapes does NOT.
// "Anchored" means a path OR a line: a file-level comment carries a path with no
// line, and a body that carries a line anchor is diff feedback whatever its
// path, so the guard tests both fields independently rather than as a pair.
func TestBuildRegexFallbackMinesOnlyTopLevelComments(t *testing.T) {
	reg, err := agentregistry.New([]agentregistry.Entry{
		{Login: "claude[bot]", ApprovalRegex: `(?im)^verdict:\s*approve`},
	})
	if err != nil {
		t.Fatalf("agentregistry.New: %v", err)
	}
	tests := []struct {
		name    string
		comment api.Comment
		want    int
	}{
		{"top-level body is mined", api.Comment{Author: "claude[bot]", Body: "Verdict: approve"}, 1},
		{"inline diff comment is not", api.Comment{Author: "claude[bot]", Body: "Verdict: approve", Path: "foo.go", Line: 42}, 0},
		{"file-level comment (path, no line) is not", api.Comment{Author: "claude[bot]", Body: "Verdict: approve", Path: "foo.go"}, 0},
		{"line anchor with no path is not", api.Comment{Author: "claude[bot]", Body: "Verdict: approve", Line: 42}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snap := Build(BuilderInput{
				Self:     "alice",
				Registry: reg,
				PRs: []PRInput{{
					PR:        api.PR{Repo: "o/r", Number: 1, Author: "alice", HeadSHA: "h1"},
					Ownership: ownership.Mine,
					Comments:  []api.Comment{tc.comment},
				}},
			})
			if got := snap.Mine[0].AgentApprovers; got != tc.want {
				t.Errorf("AgentApprovers = %d, want %d", got, tc.want)
			}
		})
	}
}

// Only the EXACT store state "approved" is an approval. The three states the
// schema allows all sort at or after "approved" lexicographically, so an
// ordering comparison would be indistinguishable from equality over them —
// hence the deliberately unrecognized state below, which sorts BEFORE
// "approved" and pins that the seam matches the literal rather than a range.
func TestBuildApprovalStateMatchIsExact(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	for _, state := range []string{"", "APPROVED", "approve"} {
		t.Run("state="+state, func(t *testing.T) {
			snap := Build(BuilderInput{
				Self:     "alice",
				Registry: reg,
				PRs: []PRInput{{
					PR:        api.PR{Repo: "o/r", Number: 1, Author: "alice", HeadSHA: "h1"},
					Ownership: ownership.Mine,
					Approvals: []store.Approval{{Approver: "carol", State: state, HeadSHA: "h1"}},
				}},
			})
			if got := snap.Mine[0].HumanApprovers; got != 0 {
				t.Errorf("state %q counted as an approval: HumanApprovers = %d, want 0", state, got)
			}
		})
	}
}

// The TEAM row carries the same per-approver facts as the Mine row. Asserted
// separately because buildTeamRow and buildMineRow map the facts onto their own
// row types independently, so a mapping mistake in one is invisible in the
// other's tests.
func TestBuildTeamRowApproverCounts(t *testing.T) {
	// Registered with no ApprovalRegex: enough to make IsAgent true (the
	// human/agent split), with the regex fallback inert.
	reg, err := agentregistry.New([]agentregistry.Entry{{Login: "claude[bot]"}})
	if err != nil {
		t.Fatalf("agentregistry.New: %v", err)
	}
	row := func(approvals []store.Approval) TeamRow {
		snap := Build(BuilderInput{
			Self:        "alice",
			TeamMembers: []string{"bob"},
			Registry:    reg,
			PRs: []PRInput{{
				PR:        api.PR{Repo: "o/r", Number: 2, Author: "bob", HeadSHA: "h1"},
				Ownership: ownership.Team,
				Revisions: []store.Revision{{Seq: 1, HeadSHA: "h1"}},
				Approvals: approvals,
			}},
		})
		if len(snap.Team) != 1 {
			t.Fatalf("want 1 team row, got %d", len(snap.Team))
		}
		return snap.Team[0]
	}

	mixed := row([]store.Approval{
		{Approver: "carol", State: "approved", HeadSHA: "h1"},
		{Approver: "claude[bot]", State: "approved", HeadSHA: "h1"},
	})
	if mixed.HumanApprovers != 1 || mixed.AgentApprovers != 1 {
		t.Errorf("mixed: human %d / agent %d, want 1/1", mixed.HumanApprovers, mixed.AgentApprovers)
	}
	if !mixed.HumanApproved || !mixed.AgentApproved {
		t.Errorf("derived booleans = %v/%v, want true/true", mixed.HumanApproved, mixed.AgentApproved)
	}

	two := row([]store.Approval{
		{Approver: "carol", State: "approved", HeadSHA: "h1"},
		{Approver: "dave", State: "approved", HeadSHA: "h1"},
	})
	if two.HumanApprovers != 2 || two.AgentApprovers != 0 {
		t.Errorf("two humans: human %d / agent %d, want 2/0", two.HumanApprovers, two.AgentApprovers)
	}

	none := row(nil)
	if none.HumanApprovers != 0 || none.AgentApprovers != 0 || none.HumanApproved || none.AgentApproved {
		t.Errorf("no approvers: human %d / agent %d bools %v/%v, want 0/0 false/false",
			none.HumanApprovers, none.AgentApprovers, none.HumanApproved, none.AgentApproved)
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
				PR:        api.PR{Repo: "org/repo", Number: 10, Author: "alice", Title: "empty", URL: "u10"},
				Ownership: ownership.Mine,
				// No JIRA, no BeadsDeps
			},
			{
				PR:        api.PR{Repo: "org/repo", Number: 11, Author: "bob", Title: "team empty", URL: "u11"},
				Ownership: ownership.Team,
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
				PR:        api.PR{Repo: "org/repo", Number: 20, Author: "alice", Title: "test", URL: "u20"},
				Ownership: ownership.Mine,
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

// TestBuildExcludesAdvisoryCIChecks verifies the snapshot rollup drops
// excluded checks per repo via the shared cirollup classifier. (pg2-qs46b)
func TestBuildExcludesAdvisoryCIChecks(t *testing.T) {
	prInput := PRInput{
		PR:        api.PR{Repo: "o/n", Number: 1, Author: "me"},
		Ownership: ownership.Mine,
		CIRuns: []api.CIRun{
			{Name: "build", Status: "completed", Conclusion: "success"},
			{Name: "policy-bot: approval required (click for details): main", Status: "completed", Conclusion: "failure"},
		},
	}
	// No exclusion: policy-bot failure makes CIStatus "failure".
	snap := Build(BuilderInput{Self: "me", PRs: []PRInput{prInput}})
	if len(snap.Mine) != 1 || snap.Mine[0].CIStatus != "failure" {
		t.Fatalf("no exclusion: got %+v, want CIStatus=failure", snap.Mine)
	}
	// With exclusion: policy-bot dropped, real check passes → "success".
	snap = Build(BuilderInput{
		Self:                 "me",
		PRs:                  []PRInput{prInput},
		ExcludedChecksByRepo: map[string][]string{"o/n": {"^policy-bot"}},
	})
	if len(snap.Mine) != 1 || snap.Mine[0].CIStatus != "success" {
		t.Fatalf("with exclusion: got %+v, want CIStatus=success", snap.Mine)
	}
}

// TestBuildNilRegistry verifies that when Registry is nil all approvers count
// as human and there is no panic. With no registry there is no agent to
// recognise, so the whole regex-mining fallback is skipped too — a login that
// WOULD be an agent under a populated registry is simply another human approver
// here (both approvers below therefore land in HumanApprovers).
func TestBuildNilRegistry(t *testing.T) {
	in := BuilderInput{
		GeneratedAt:         time.Now(),
		SyncIntervalSeconds: 60,
		Self:                "alice",
		TeamMembers:         []string{},
		Registry:            nil, // explicit nil
		PRs: []PRInput{
			{
				PR:        api.PR{Repo: "org/repo", Number: 30, Author: "alice", Title: "nil-reg", URL: "u30", HeadSHA: "h1"},
				Ownership: ownership.Mine,
				Approvals: []store.Approval{
					{Approver: "anyone", State: "approved", HeadSHA: "h1"},
					{Approver: "claude[bot]", State: "approved", HeadSHA: "h1"},
				},
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
	if row.HumanApprovers != 2 || row.AgentApprovers != 0 {
		t.Errorf("nil registry: human %d / agent %d, want 2/0", row.HumanApprovers, row.AgentApprovers)
	}
}

// A nil registry MUST short-circuit the regex-mining block outright, not merely
// happen to skip it. The fixture is the one that distinguishes the two: a review
// and a comment from a login the store has NO row for, so the mining block would
// be REACHED and would call a method on the nil *agentregistry.Registry — which
// panics. TestBuildNilRegistry above cannot catch that, because every login it
// names has a store row and is therefore skipped inside the block.
func TestBuildNilRegistrySkipsRegexMiningEntirely(t *testing.T) {
	snap := Build(BuilderInput{
		Self:     "alice",
		Registry: nil,
		PRs: []PRInput{{
			PR:        api.PR{Repo: "o/r", Number: 40, Author: "alice", HeadSHA: "h1"},
			Ownership: ownership.Mine,
			// No Approvals at all, so nothing is pre-recorded.
			Comments: []api.Comment{{Author: "claude[bot]", Body: "Verdict: approve"}},
			Reviews:  []api.Review{{ID: "r1", Author: "claude[bot]", State: "APPROVED", Body: "Verdict: approve"}},
		}},
	})
	row := snap.Mine[0]
	if row.HumanApprovers != 0 || row.AgentApprovers != 0 {
		t.Errorf("nil registry with un-recorded logins: human %d / agent %d, want 0/0 — "+
			"with no registry there is no approval regex to mine, and a live review is not a store row",
			row.HumanApprovers, row.AgentApprovers)
	}
}

// TestBuildMineRowMergeReminder verifies MineRow surfaces GitHub's
// authoritative MergeStateStatus/AutoMergeEnabled and derives the
// "ready to merge / automerge-forgotten" NeedsMergeReminder signal. (pg2-dwfld)
func TestBuildMineRowMergeReminder(t *testing.T) {
	mk := func(state string, auto bool) MineRow {
		p := PRInput{PR: api.PR{Repo: "o/n", Number: 1, Author: "me", MergeStateStatus: state, AutoMergeEnabled: auto}, Ownership: ownership.Mine}
		return buildMineRow(p, nil, nil)
	}
	if !mk("CLEAN", false).NeedsMergeReminder {
		t.Errorf("CLEAN + no automerge should need reminder")
	}
	if mk("CLEAN", true).NeedsMergeReminder {
		t.Errorf("CLEAN + automerge armed should NOT need reminder")
	}
	if mk("BLOCKED", false).NeedsMergeReminder {
		t.Errorf("BLOCKED should NOT need reminder")
	}
	if got := mk("CLEAN", false).MergeStateStatus; got != "CLEAN" {
		t.Errorf("MergeStateStatus passthrough got %q", got)
	}
}

// TestBuild_CoOwnedInMinePanelBadged verifies the partition keys on the
// ownership classifier, not raw author-equality: a teammate-authored PR
// classified CoOwned (I pushed a commit onto it) lands in the Mine panel,
// badged, rather than the Team panel.
func TestBuild_CoOwnedInMinePanelBadged(t *testing.T) {
	in := BuilderInput{
		Self:        "me",
		TeamMembers: []string{"you"},
		PRs: []PRInput{{
			PR:        api.PR{Repo: "o/r", Number: 5, Author: "you", Draft: false},
			Ownership: ownership.CoOwned,
		}},
	}
	out := Build(in)
	if len(out.Mine) != 1 || len(out.Team) != 0 {
		t.Fatalf("want 1 mine / 0 team; got %d / %d", len(out.Mine), len(out.Team))
	}
	if !out.Mine[0].CoOwned {
		t.Errorf("MineRow.CoOwned = false, want true")
	}
}

// TestBuild_MineConflictFlags verifies a mine PR flagged CONFLICTING by GitHub
// surfaces HasConflicts on its MineRow.
func TestBuild_MineConflictFlags(t *testing.T) {
	in := BuilderInput{
		Self: "me",
		PRs: []PRInput{{
			PR:        api.PR{Repo: "o/r", Number: 1, Author: "me", Mergeable: "CONFLICTING"},
			Ownership: ownership.Mine,
		}},
	}
	out := Build(in)
	if len(out.Mine) != 1 {
		t.Fatalf("want 1 mine row, got %d", len(out.Mine))
	}
	if !out.Mine[0].HasConflicts {
		t.Errorf("mine HasConflicts = %v, want true", out.Mine[0].HasConflicts)
	}
}

// TestBuild_TeamConflictFlag verifies a team PR flagged DIRTY by GitHub
// surfaces HasConflicts on its TeamRow.
func TestBuild_TeamConflictFlag(t *testing.T) {
	in := BuilderInput{
		Self:        "me",
		TeamMembers: []string{"you"},
		PRs: []PRInput{{
			PR:        api.PR{Repo: "o/r", Number: 2, Author: "you", Draft: false, MergeStateStatus: "DIRTY"},
			Ownership: ownership.Team,
		}},
	}
	out := Build(in)
	if len(out.Team) != 1 || !out.Team[0].HasConflicts {
		t.Fatalf("want 1 team row with HasConflicts; got %d rows", len(out.Team))
	}
}

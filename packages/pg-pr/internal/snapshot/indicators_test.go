package snapshot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// TestBuildStateFor_MapsFourValuedRollupOntoThree pins the mapping from
// cirollup.Compute's ACTUAL four-valued Rollup.State (failure/pending/
// success/none) onto the operator's requested three-valued vocabulary
// (broken/pending/passing) — and, specifically, the decided "none" mapping
// (pg2-4dz88.7.5 acceptance criterion).
func TestBuildStateFor_MapsFourValuedRollupOntoThree(t *testing.T) {
	tests := []struct {
		ciStatus string
		want     string
	}{
		{"failure", BuildStateBroken},
		{"pending", BuildStatePending},
		{"success", BuildStatePassing},
		// "none" (no countable CI run at all) is decided to map to pending,
		// NOT passing — panels.go's ActNow already treats anything other
		// than "success" as not-yet-green, so folding "none" into "passing"
		// here would contradict that established meaning.
		{"none", BuildStatePending},
		// An unrecognised value fails closed into pending rather than
		// panicking or guessing broken/passing.
		{"unrecognised", BuildStatePending},
		{"", BuildStatePending},
	}
	for _, tc := range tests {
		t.Run(tc.ciStatus, func(t *testing.T) {
			if got := buildStateFor(tc.ciStatus); got != tc.want {
				t.Errorf("buildStateFor(%q) = %q, want %q", tc.ciStatus, got, tc.want)
			}
		})
	}
}

// TestBotVerdictFor_TriState proves the tri-state read is genuinely three
// distinguishable states, not a relabeled boolean. Approved and no-decision
// are BOTH "not disapproved" — a shipped-but-still-boolean implementation
// (checking only for a standing disapproval) would collapse them into the
// same value. This test fails against such an implementation because it
// asserts the TWO NON-DISAPPROVED cases produce DIFFERENT BotVerdict values.
func TestBotVerdictFor_TriState(t *testing.T) {
	allowlist := map[string]struct{}{"policy-bot": {}}

	noDecision := botVerdictFor(nil, allowlist, "h1")
	approved := botVerdictFor([]store.Approval{
		{Approver: "policy-bot", State: "approved", HeadSHA: "h1"},
	}, allowlist, "h1")
	disapproved := botVerdictFor([]store.Approval{
		{Approver: "policy-bot", State: "changes-requested", HeadSHA: "h1"},
	}, allowlist, "h1")

	if noDecision != BotVerdictNoDecision {
		t.Errorf("no approvals: got %q, want %q", noDecision, BotVerdictNoDecision)
	}
	if approved != BotVerdictApproved {
		t.Errorf("standing allowlisted approval: got %q, want %q", approved, BotVerdictApproved)
	}
	if disapproved != BotVerdictDisapproved {
		t.Errorf("standing allowlisted disapproval: got %q, want %q", disapproved, BotVerdictDisapproved)
	}
	// The load-bearing assertion: approved and no-decision MUST differ. A
	// boolean relabeling (only "is there a standing disapproval") cannot
	// represent this distinction at all — both would read as "false".
	if approved == noDecision {
		t.Fatalf("BotVerdictApproved (%q) must differ from BotVerdictNoDecision (%q) — "+
			"a boolean-relabeled implementation would collapse these", approved, noDecision)
	}
}

// TestBotVerdictFor_AllowlistAndStaleness covers the filtering rules
// botVerdictFor must apply: non-allowlisted logins never count, a stale
// (superseded-head or host-dismissed) row never counts, and a standing
// disapproval from ANY allowlisted approver wins over a standing approval
// from another.
func TestBotVerdictFor_AllowlistAndStaleness(t *testing.T) {
	allowlist := map[string]struct{}{"policy-bot": {}}

	tests := []struct {
		name      string
		approvals []store.Approval
		headSHA   string
		want      string
	}{
		{
			name: "non-allowlisted disapproval is ignored",
			approvals: []store.Approval{
				{Approver: "carol", State: "changes-requested", HeadSHA: "h1"},
			},
			headSHA: "h1",
			want:    BotVerdictNoDecision,
		},
		{
			name: "stale (superseded head) disapproval withdrawn",
			approvals: []store.Approval{
				{Approver: "policy-bot", State: "changes-requested", HeadSHA: "h0"},
			},
			headSHA: "h1",
			want:    BotVerdictNoDecision,
		},
		{
			name: "dismissed disapproval withdrawn even at current head",
			approvals: []store.Approval{
				{Approver: "policy-bot", State: "changes-requested", HeadSHA: "h1", Dismissed: true},
			},
			headSHA: "h1",
			want:    BotVerdictNoDecision,
		},
		{
			name: "allowlisted commented is neither approval nor disapproval",
			approvals: []store.Approval{
				{Approver: "policy-bot", State: "commented", HeadSHA: "h1"},
			},
			headSHA: "h1",
			want:    BotVerdictNoDecision,
		},
		{
			name: "disapproval wins over a mixed approve+disapprove from two allowlisted approvers",
			approvals: []store.Approval{
				{Approver: "policy-bot", State: "approved", HeadSHA: "h1"},
				{Approver: "other-bot", State: "changes-requested", HeadSHA: "h1"},
			},
			headSHA: "h1",
			want:    BotVerdictDisapproved,
		},
	}
	// "other-bot" must also be allowlisted for the mixed case.
	mixedAllowlist := map[string]struct{}{"policy-bot": {}, "other-bot": {}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			al := allowlist
			if tc.name == "disapproval wins over a mixed approve+disapprove from two allowlisted approvers" {
				al = mixedAllowlist
			}
			if got := botVerdictFor(tc.approvals, al, tc.headSHA); got != tc.want {
				t.Errorf("botVerdictFor() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSelfApprovalStateFor_NotAReExportOfNeedsAttention proves this is
// genuinely new wiring: two scenarios where NeedsAttention reads the SAME
// (false, "") — "off the hook" — for DIFFERENT underlying reasons (a
// teammate approved vs. self approved), which SelfApprovalState MUST tell
// apart. A re-export of NeedsAttention/AttentionReasonReReview could not
// distinguish these; this test fails against such a re-export.
func TestSelfApprovalStateFor_NotAReExportOfNeedsAttention(t *testing.T) {
	const self = "alice"
	head := "h1"

	// Scenario A: a TEAMMATE approved the current head; self never reviewed.
	teammateApproved := []store.Approval{{Approver: "bob", State: "approved", HeadSHA: head}}
	need, _ := NeedsAttention([]store.Revision{{Seq: 1, HeadSHA: head}}, teammateApproved, self, false)
	if need {
		t.Fatalf("precondition: teammate approval must take the PR off the attention hook")
	}
	if got := selfApprovalStateFor(teammateApproved, self, head); got != SelfApprovalNotApproved {
		t.Errorf("teammate-only approval: selfApprovalStateFor = %q, want %q (self never approved)",
			got, SelfApprovalNotApproved)
	}

	// Scenario B: SELF approved the current head.
	selfApproved := []store.Approval{{Approver: self, State: "approved", HeadSHA: head}}
	need2, _ := NeedsAttention([]store.Revision{{Seq: 1, HeadSHA: head}}, selfApproved, self, false)
	if need2 {
		t.Fatalf("precondition: self's own standing approval must take the PR off the attention hook")
	}
	if got := selfApprovalStateFor(selfApproved, self, head); got != SelfApprovalStanding {
		t.Errorf("self approval: selfApprovalStateFor = %q, want %q", got, SelfApprovalStanding)
	}

	// Both scenarios read NeedsAttention == false identically, but
	// selfApprovalStateFor MUST have told them apart above (asserted
	// separately); this final check just documents the parity.
	if need != need2 {
		t.Fatalf("both scenarios must read NeedsAttention identically (false) for this test to be meaningful")
	}
}

// TestSelfApprovalStateFor_Staleness covers the three states directly: no
// approval on record, a standing approval, and a stale one (superseded head
// or host-dismissed).
func TestSelfApprovalStateFor_Staleness(t *testing.T) {
	const self = "alice"
	tests := []struct {
		name      string
		approvals []store.Approval
		headSHA   string
		self      string
		want      string
	}{
		{"empty self", []store.Approval{{Approver: self, State: "approved", HeadSHA: "h1"}}, "h1", "", SelfApprovalNotApproved},
		// A coincidental row with Approver=="" must NOT be treated as self's
		// own row when self is ALSO "": self=="" means "no viewer identified
		// at all", never "the viewer whose login happens to be empty". This
		// pins the early-return guard itself (pg-go-mutate: without it, the
		// loop below would match this row by login equality and misreport
		// SelfApprovalStanding).
		{"empty self guards against a coincidental empty-login approver row", []store.Approval{{Approver: "", State: "approved", HeadSHA: "h1"}}, "h1", "", SelfApprovalNotApproved},
		{"no rows at all", nil, "h1", self, SelfApprovalNotApproved},
		{"self commented, not approved", []store.Approval{{Approver: self, State: "commented", HeadSHA: "h1"}}, "h1", self, SelfApprovalNotApproved},
		{"self changes-requested (own review, not approved)", []store.Approval{{Approver: self, State: "changes-requested", HeadSHA: "h1"}}, "h1", self, SelfApprovalNotApproved},
		{"self approved current head", []store.Approval{{Approver: self, State: "approved", HeadSHA: "h1"}}, "h1", self, SelfApprovalStanding},
		{"self approved an earlier head (stale)", []store.Approval{{Approver: self, State: "approved", HeadSHA: "h0"}}, "h1", self, SelfApprovalStale},
		{"self approved but host-dismissed (stale)", []store.Approval{{Approver: self, State: "approved", HeadSHA: "h1", Dismissed: true}}, "h1", self, SelfApprovalStale},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := selfApprovalStateFor(tc.approvals, tc.self, tc.headSHA); got != tc.want {
				t.Errorf("selfApprovalStateFor() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSelfApprovalStateFor_OnlyMatchesExactSelfLogin proves the per-row
// login match (indicators.go's `a.Approver != self` guard) is an EXACT
// equality check, not an ordering comparison — pinned in BOTH lexical
// directions so a mutation replacing != with an ordering operator (>, >=,
// <, <=) cannot survive by accident on just one direction. A teammate whose
// login sorts either before or after self must be skipped identically: self
// never approved, regardless of whose login is lexically "smaller".
func TestSelfApprovalStateFor_OnlyMatchesExactSelfLogin(t *testing.T) {
	const head = "h1"
	tests := []struct {
		name     string
		self     string
		approver string
	}{
		{"teammate login sorts BEFORE self", "carol", "alice"},
		{"teammate login sorts AFTER self", "alice", "carol"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			approvals := []store.Approval{{Approver: tc.approver, State: "approved", HeadSHA: head}}
			if got := selfApprovalStateFor(approvals, tc.self, head); got != SelfApprovalNotApproved {
				t.Errorf("selfApprovalStateFor(self=%q, approver=%q) = %q, want %q — "+
					"a teammate's standing approval must never be read as self's own",
					tc.self, tc.approver, got, SelfApprovalNotApproved)
			}
		})
	}
}

// TestSelfCommentedFor covers the scoped (option-b) reading directly.
func TestSelfCommentedFor(t *testing.T) {
	const self = "alice"
	tests := []struct {
		name      string
		approvals []store.Approval
		self      string
		want      bool
	}{
		{"empty self", []store.Approval{{Approver: self, State: "commented"}}, "", false},
		// Mirrors selfApprovalStateFor's guard test: a coincidental
		// Approver=="" row must not be read as self's own comment when self
		// is ALSO "" — pins the early-return guard itself.
		{"empty self guards against a coincidental empty-login approver row", []store.Approval{{Approver: "", State: "commented"}}, "", false},
		{"no rows", nil, self, false},
		{"self approved, not commented", []store.Approval{{Approver: self, State: "approved"}}, self, false},
		{"self changes-requested, not commented", []store.Approval{{Approver: self, State: "changes-requested"}}, self, false},
		{"self commented", []store.Approval{{Approver: self, State: "commented"}}, self, true},
		{"teammate commented, self silent", []store.Approval{{Approver: "bob", State: "commented"}}, self, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := selfCommentedFor(tc.approvals, tc.self); got != tc.want {
				t.Errorf("selfCommentedFor() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSelfCommentedFor_OnlyMatchesExactSelfLogin is selfCommentedFor's
// counterpart to TestSelfApprovalStateFor_OnlyMatchesExactSelfLogin: the
// per-row login match (`a.Approver == self`) is exact equality, not an
// ordering comparison, pinned in both lexical directions.
func TestSelfCommentedFor_OnlyMatchesExactSelfLogin(t *testing.T) {
	tests := []struct {
		name     string
		self     string
		approver string
	}{
		{"teammate login sorts BEFORE self", "bob", "aaron"},
		{"teammate login sorts AFTER self", "aaron", "bob"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			approvals := []store.Approval{{Approver: tc.approver, State: "commented"}}
			if got := selfCommentedFor(approvals, tc.self); got {
				t.Errorf("selfCommentedFor(self=%q, approver=%q) = true, want false — "+
					"a teammate's comment must never be read as self's own", tc.self, tc.approver)
			}
		})
	}
}

// TestSelfCommentedDoesNotDuplicateSelfApprovalState pins the non-duplication
// claim from selfCommentedFor's own doc: because pr_approval is UNIQUE
// (pr_id, approver), self's single row can set SelfCommented true only when
// SelfApprovalState is SelfApprovalNotApproved (a "commented" row is never
// simultaneously an "approved" row), and vice versa — the two facts read the
// SAME underlying row but are never redundant restatements of each other.
func TestSelfCommentedDoesNotDuplicateSelfApprovalState(t *testing.T) {
	const self = "alice"
	const head = "h1"

	commentedRow := []store.Approval{{Approver: self, State: "commented", HeadSHA: head}}
	if !selfCommentedFor(commentedRow, self) {
		t.Fatalf("precondition: commented row must set SelfCommented true")
	}
	if got := selfApprovalStateFor(commentedRow, self, head); got != SelfApprovalNotApproved {
		t.Errorf("a self row in state=commented must read SelfApprovalNotApproved, got %q — "+
			"otherwise the two fields would double-count the same positive signal", got)
	}

	approvedRow := []store.Approval{{Approver: self, State: "approved", HeadSHA: head}}
	if selfCommentedFor(approvedRow, self) {
		t.Error("a self row in state=approved must NOT set SelfCommented — it is not a comment disposition")
	}
	if got := selfApprovalStateFor(approvedRow, self, head); got != SelfApprovalStanding {
		t.Errorf("a self row in state=approved must read SelfApprovalStanding, got %q", got)
	}
}

// TestMatchReasonBreakdown_Booleans proves the three discrete booleans
// (indicator #5) are computed from the pre-existing MatchReason set via
// Build, each independently, and that a row whose ONLY match reason is
// reviewed-by-me sets NONE of the three — the double-count guard this bead's
// acceptance criteria require (MatchReasonReviewedByMe stays undecomposed
// here; it is not this indicator's concern).
func TestMatchReasonBreakdown_Booleans(t *testing.T) {
	base := BuilderInput{
		GeneratedAt: time.Now(),
		Self:        "alice",
		TeamMembers: []string{"bob"},
		WatchLabels: []string{"watch-a"},
	}

	tests := []struct {
		name                string
		pr                  api.PR
		wantTeamAuthored    bool
		wantReviewRequested bool
		wantHasWatchLabel   bool
	}{
		{
			name:             "team-authored only",
			pr:               api.PR{Repo: "o/r", Number: 1, Author: "bob"},
			wantTeamAuthored: true,
		},
		{
			name:                "review-requested only",
			pr:                  api.PR{Repo: "o/r", Number: 2, Author: "carol", ReviewRequestedOfMe: true},
			wantReviewRequested: true,
		},
		{
			name:              "watch label only",
			pr:                api.PR{Repo: "o/r", Number: 3, Author: "carol", Labels: []string{"watch-a"}},
			wantHasWatchLabel: true,
		},
		{
			name: "reviewed-by-me only sets NONE of the three (double-count guard)",
			pr:   api.PR{Repo: "o/r", Number: 4, Author: "carol"},
			// reasons populated below via Reviews, not PR fields.
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			pi := PRInput{PR: tc.pr, Ownership: ownership.Team}
			if tc.name == "reviewed-by-me only sets NONE of the three (double-count guard)" {
				pi.Reviews = []api.Review{{Author: "alice", State: "APPROVED"}}
			}
			in.PRs = []PRInput{pi}
			snap := Build(in)
			if len(snap.Team) != 1 {
				t.Fatalf("want 1 team row, got %d (dropped=%d)", len(snap.Team), snap.DroppedCount)
			}
			row := snap.Team[0]
			if row.MatchTeamAuthored != tc.wantTeamAuthored {
				t.Errorf("MatchTeamAuthored = %v, want %v", row.MatchTeamAuthored, tc.wantTeamAuthored)
			}
			if row.MatchReviewRequested != tc.wantReviewRequested {
				t.Errorf("MatchReviewRequested = %v, want %v", row.MatchReviewRequested, tc.wantReviewRequested)
			}
			if row.MatchHasWatchLabel != tc.wantHasWatchLabel {
				t.Errorf("MatchHasWatchLabel = %v, want %v", row.MatchHasWatchLabel, tc.wantHasWatchLabel)
			}
		})
	}
}

// TestIndicatorFieldsUniversalAcrossMineAndTeam builds one Mine PR and one
// Team PR sharing the identical CI/bot-approval shape and asserts BuildState
// and BotVerdict come out IDENTICAL on both — computed the same way
// regardless of which row-producing path (Mine vs Team) the PR lands on
// (pg2-4dz88.7.5's "universal, independent of membership" requirement,
// extending the CI-red precedent from pg2-4dz88.7's design).
func TestIndicatorFieldsUniversalAcrossMineAndTeam(t *testing.T) {
	allowlist := []string{"policy-bot"}
	shared := func(repo string, n int, ownr ownership.Ownership, author string) PRInput {
		return PRInput{
			PR:        api.PR{Repo: repo, Number: n, Author: author, HeadSHA: "h1"},
			Ownership: ownr,
			CIRuns:    []api.CIRun{{Status: "completed", Conclusion: "failure"}},
			Approvals: []store.Approval{{Approver: "policy-bot", State: "changes-requested", HeadSHA: "h1"}},
		}
	}
	in := BuilderInput{
		GeneratedAt:       time.Now(),
		Self:              "alice",
		TeamMembers:       []string{"bob"},
		ApproverAllowlist: allowlist,
		PRs: []PRInput{
			shared("o/r", 1, ownership.Mine, "alice"),
			shared("o/r", 2, ownership.Team, "bob"),
		},
	}
	snap := Build(in)
	if len(snap.Mine) != 1 || len(snap.Team) != 1 {
		t.Fatalf("want 1 mine + 1 team row, got mine=%d team=%d (dropped=%d)",
			len(snap.Mine), len(snap.Team), snap.DroppedCount)
	}
	mine, team := snap.Mine[0], snap.Team[0]
	if mine.BuildState != BuildStateBroken || team.BuildState != BuildStateBroken {
		t.Errorf("BuildState: mine=%q team=%q, want both %q", mine.BuildState, team.BuildState, BuildStateBroken)
	}
	if mine.BotVerdict != BotVerdictDisapproved || team.BotVerdict != BotVerdictDisapproved {
		t.Errorf("BotVerdict: mine=%q team=%q, want both %q", mine.BotVerdict, team.BotVerdict, BotVerdictDisapproved)
	}
}

// TestIndicatorFieldsRoundTripJSON proves every new indicator field survives
// the JSON snapshot encode/decode round trip — a Go-only field is
// unrenderable to Grafana (TestDashboardDroppedCountSerializesAsZero /
// TestDashboard200WhenPopulated's precedent, generalised to this bead's
// whole field set).
func TestIndicatorFieldsRoundTripJSON(t *testing.T) {
	team := TeamRow{
		Repo: "o/r", Number: 1,
		BuildState:           BuildStateBroken,
		BotVerdict:           BotVerdictDisapproved,
		MatchTeamAuthored:    true,
		MatchReviewRequested: true,
		MatchHasWatchLabel:   true,
		SelfApprovalState:    SelfApprovalStale,
		SelfCommented:        true,
	}
	b, err := json.Marshal(team)
	if err != nil {
		t.Fatalf("marshal TeamRow: %v", err)
	}
	var gotTeam TeamRow
	if err := json.Unmarshal(b, &gotTeam); err != nil {
		t.Fatalf("unmarshal TeamRow: %v", err)
	}
	// TeamRow is not comparable with == (it holds slice fields), so compare
	// exactly the indicator fields this test cares about.
	if gotTeam.BuildState != team.BuildState ||
		gotTeam.BotVerdict != team.BotVerdict ||
		gotTeam.MatchTeamAuthored != team.MatchTeamAuthored ||
		gotTeam.MatchReviewRequested != team.MatchReviewRequested ||
		gotTeam.MatchHasWatchLabel != team.MatchHasWatchLabel ||
		gotTeam.SelfApprovalState != team.SelfApprovalState ||
		gotTeam.SelfCommented != team.SelfCommented {
		t.Errorf("TeamRow indicator round trip mismatch:\n got  %+v\n want %+v", gotTeam, team)
	}

	mine := MineRow{
		Repo: "o/r", Number: 1,
		BuildState: BuildStatePassing,
		BotVerdict: BotVerdictApproved,
	}
	bm, err := json.Marshal(mine)
	if err != nil {
		t.Fatalf("marshal MineRow: %v", err)
	}
	var gotMine MineRow
	if err := json.Unmarshal(bm, &gotMine); err != nil {
		t.Fatalf("unmarshal MineRow: %v", err)
	}
	if gotMine.BuildState != mine.BuildState || gotMine.BotVerdict != mine.BotVerdict {
		t.Errorf("MineRow round trip mismatch: got BuildState=%q BotVerdict=%q, want %q/%q",
			gotMine.BuildState, gotMine.BotVerdict, mine.BuildState, mine.BotVerdict)
	}
}

package snapshot

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// baselineActNowRow is a TeamRow satisfying every ACT-NOW clause: CI green,
// no bot disapproval, no conflict. Each PerClause case flips exactly one
// field false in isolation.
func baselineActNowRow() TeamRow {
	return TeamRow{
		Repo:     "o/r",
		Number:   1,
		CIStatus: "success",
	}
}

// TestTeamPanelMembership_PerClause covers every clause of the ACT-NOW
// predicate (panels.go's ActNow): one case per clause, each flipped false in
// isolation, asserting the row moves to the BLOCKED (complement) panel. Per
// the 2026-08-28 operator ruling recorded on pg2-4dz88.7.3, there is
// deliberately NO "not draft" case: Build never produces a TeamRow{Draft:
// true} for this predicate to exclude, so encoding that clause here would be
// untestable.
func TestTeamPanelMembership_PerClause(t *testing.T) {
	base := baselineActNowRow()
	if !ActNow(base) {
		t.Fatalf("baseline row must satisfy ActNow: %+v", base)
	}

	cases := []struct {
		name string
		row  TeamRow
	}{
		{
			name: "CI not green (failure)",
			row:  func() TeamRow { r := base; r.CIStatus = "failure"; return r }(),
		},
		{
			name: "CI not green (pending)",
			row:  func() TeamRow { r := base; r.CIStatus = "pending"; return r }(),
		},
		{
			name: "CI not green (none)",
			row:  func() TeamRow { r := base; r.CIStatus = "none"; return r }(),
		},
		{
			name: "bot disapproval",
			row:  func() TeamRow { r := base; r.BotDisapproved = true; return r }(),
		},
		{
			name: "merge conflict",
			row:  func() TeamRow { r := base; r.HasConflicts = true; return r }(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if ActNow(tc.row) {
				t.Errorf("ActNow(%+v) = true, want false", tc.row)
			}
			actNow, blocked := PartitionTeamPanels([]TeamRow{tc.row})
			if len(actNow) != 0 || len(blocked) != 1 {
				t.Errorf("PartitionTeamPanels: got actNow=%d blocked=%d, want 0/1", len(actNow), len(blocked))
			}
		})
	}
}

// TestTeamPanelMembership_Exclusive asserts every row in a mixed corpus lands
// in EXACTLY one of the two panels (never both, never neither) — a
// REGRESSION PIN for the specific row-shape the OLD hand-rolled panel-2
// predicate ("failed build or failed bot review or merge conflict") would
// have dropped: a PR whose CI is merely PENDING (not "failure") with no bot
// disapproval and no conflict. That old enumeration never mentions "pending",
// so under it such a row would satisfy neither panel's predicate — the exact
// O3 grooming-review hole. Under the exhaustive-complement design that hole
// cannot recur: panel 2 has no independent predicate of its own to miss a
// case from, so the row correctly lands in BLOCKED as !ActNow.
func TestTeamPanelMembership_Exclusive(t *testing.T) {
	corpus := []TeamRow{
		{Repo: "o/r", Number: 1, CIStatus: "success"},                       // act-now
		{Repo: "o/r", Number: 2, CIStatus: "failure"},                       // blocked: build failed
		{Repo: "o/r", Number: 3, CIStatus: "success", BotDisapproved: true}, // blocked: bot disapproval
		{Repo: "o/r", Number: 4, CIStatus: "success", HasConflicts: true},   // blocked: conflict
		{Repo: "o/r", Number: 5, CIStatus: "pending"},                       // the O3-hole row
	}

	actNow, blocked := PartitionTeamPanels(corpus)
	if len(actNow)+len(blocked) != len(corpus) {
		t.Fatalf("partition dropped rows: got %d+%d, want %d", len(actNow), len(blocked), len(corpus))
	}

	seen := map[int]int{}
	for _, r := range actNow {
		seen[r.Number]++
	}
	for _, r := range blocked {
		seen[r.Number]++
	}
	for _, r := range corpus {
		if seen[r.Number] != 1 {
			t.Errorf("PR #%d appeared in %d panels, want exactly 1", r.Number, seen[r.Number])
		}
	}

	// The O3-hole row specifically: must land in BLOCKED, not vanish.
	foundPending := false
	for _, r := range blocked {
		if r.Number == 5 {
			foundPending = true
		}
	}
	if !foundPending {
		t.Errorf("pending-CI row (#5, the O3-hole shape) must land in BLOCKED, not be dropped")
	}
	for _, r := range actNow {
		if r.Number == 5 {
			t.Errorf("pending-CI row (#5) must not land in ACT-NOW")
		}
	}
}

// TestTeamPanel1Empty and TestTeamPanel2Empty: each panel independently empty
// renders as an empty slice, never nil, so a JSON consumer sees `[]`.
func TestTeamPanel1Empty(t *testing.T) {
	rows := []TeamRow{{Repo: "o/r", Number: 1, CIStatus: "failure"}} // all blocked
	actNow, blocked := PartitionTeamPanels(rows)
	if actNow == nil {
		t.Error("actNow must be an empty slice, not nil")
	}
	if len(actNow) != 0 {
		t.Errorf("actNow = %+v, want empty", actNow)
	}
	if len(blocked) != 1 {
		t.Errorf("blocked = %+v, want 1 row", blocked)
	}
}

func TestTeamPanel2Empty(t *testing.T) {
	rows := []TeamRow{{Repo: "o/r", Number: 1, CIStatus: "success"}} // all act-now
	actNow, blocked := PartitionTeamPanels(rows)
	if blocked == nil {
		t.Error("blocked must be an empty slice, not nil")
	}
	if len(blocked) != 0 {
		t.Errorf("blocked = %+v, want empty", blocked)
	}
	if len(actNow) != 1 {
		t.Errorf("actNow = %+v, want 1 row", actNow)
	}
}

// TestTeamPanel1Empty and TestTeamPanel2Empty also hold over a fully empty
// input corpus: no rows at all must still yield two empty (non-nil) slices.
func TestTeamPanelsEmptyCorpusYieldsNoNilSlices(t *testing.T) {
	actNow, blocked := PartitionTeamPanels(nil)
	if actNow == nil || blocked == nil {
		t.Fatalf("both panels must be non-nil for an empty corpus, got actNow=%v blocked=%v", actNow, blocked)
	}
	if len(actNow) != 0 || len(blocked) != 0 {
		t.Errorf("expected both panels empty, got actNow=%+v blocked=%+v", actNow, blocked)
	}
}

// TestBuildTeamRow_BotDisapprovalVsHumanChangesRequested is the safety-critical
// disambiguation test the bead's description flags: a human teammate's
// CHANGES_REQUESTED review lands in the SAME pr_approval table, with the SAME
// state string ("changes-requested"), as a bot verdict — the only
// distinguishing signal is whether the approver's login is in
// config.Config.ApproverAllowlist (BuilderInput.ApproverAllowlist). A human
// review from a non-allowlisted login must NOT set BotDisapproved / must NOT
// move the row to BLOCKED by itself; the identical state from an allowlisted
// login must.
func TestBuildTeamRow_BotDisapprovalVsHumanChangesRequested(t *testing.T) {
	reg, _ := agentregistry.New(nil)

	// Case 1: "changes-requested" from carol, who is NOT in the allowlist —
	// an ordinary human review. Must not set BotDisapproved.
	humanIn := BuilderInput{
		GeneratedAt: time.Now(),
		Self:        "alice",
		TeamMembers: []string{"bob"},
		Registry:    reg,
		PRs: []PRInput{
			{
				PR:        api.PR{Repo: "o/r", Number: 10, Author: "bob", URL: "u10", HeadSHA: "h1"},
				Revisions: []store.Revision{{Seq: 1, HeadSHA: "h1"}},
				CIRuns:    []api.CIRun{{Status: "completed", Conclusion: "success"}},
				Approvals: []store.Approval{{Approver: "carol", State: "changes-requested", HeadSHA: "h1"}},
			},
		},
		ApproverAllowlist: []string{"policy-bot"},
	}
	humanSnap := Build(humanIn)
	if len(humanSnap.Team) != 1 {
		t.Fatalf("want 1 team row, got %d", len(humanSnap.Team))
	}
	if humanSnap.Team[0].BotDisapproved {
		t.Error("a human teammate's changes-requested review must NOT set BotDisapproved")
	}
	if !ActNow(humanSnap.Team[0]) {
		t.Error("a human-only changes-requested review must not by itself move the row out of ActNow")
	}

	// Case 2: the SAME state, same head, but from an allowlisted login.
	botIn := humanIn
	botIn.PRs = []PRInput{
		{
			PR:        api.PR{Repo: "o/r", Number: 11, Author: "bob", URL: "u11", HeadSHA: "h1"},
			Revisions: []store.Revision{{Seq: 1, HeadSHA: "h1"}},
			CIRuns:    []api.CIRun{{Status: "completed", Conclusion: "success"}},
			Approvals: []store.Approval{{Approver: "policy-bot", State: "changes-requested", HeadSHA: "h1"}},
		},
	}
	botSnap := Build(botIn)
	if len(botSnap.Team) != 1 {
		t.Fatalf("want 1 team row, got %d", len(botSnap.Team))
	}
	if !botSnap.Team[0].BotDisapproved {
		t.Error("an allowlisted approver's changes-requested verdict MUST set BotDisapproved")
	}
	if ActNow(botSnap.Team[0]) {
		t.Error("a standing bot disapproval must move the row out of ActNow")
	}
}

// TestBuildTeamRow_StaleBotDisapprovalWithdrawn pins the staleness decision
// pg2-4dz88.7.3's description calls out explicitly: a bot disapproval that no
// longer STANDS for the PR's current head — here, one observed against an
// earlier head than the PR's current HeadSHA — is treated as WITHDRAWN, not
// blocking, mirroring the staleness treatment classifyApprovals/NeedsAttention
// already give every other pr_approval row (INV-APPROVAL-3).
func TestBuildTeamRow_StaleBotDisapprovalWithdrawn(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	in := BuilderInput{
		GeneratedAt: time.Now(),
		Self:        "alice",
		TeamMembers: []string{"bob"},
		Registry:    reg,
		PRs: []PRInput{
			{
				PR:        api.PR{Repo: "o/r", Number: 20, Author: "bob", URL: "u20", HeadSHA: "h2"},
				Revisions: []store.Revision{{Seq: 1, HeadSHA: "h2"}},
				// Disapproval was observed against h1; the PR has since moved to h2.
				Approvals: []store.Approval{{Approver: "policy-bot", State: "changes-requested", HeadSHA: "h1"}},
			},
		},
		ApproverAllowlist: []string{"policy-bot"},
	}
	snap := Build(in)
	if len(snap.Team) != 1 {
		t.Fatalf("want 1 team row, got %d", len(snap.Team))
	}
	if snap.Team[0].BotDisapproved {
		t.Error("a bot disapproval of a SUPERSEDED head must be treated as withdrawn, not blocking")
	}

	// Also pin the Dismissed-by-host path to the same "withdrawn" outcome,
	// even at the CURRENT head (IsStale is head-independent for Dismissed).
	dismissedIn := in
	dismissedIn.PRs = []PRInput{
		{
			PR:        api.PR{Repo: "o/r", Number: 21, Author: "bob", URL: "u21", HeadSHA: "h1"},
			Revisions: []store.Revision{{Seq: 1, HeadSHA: "h1"}},
			Approvals: []store.Approval{{Approver: "policy-bot", State: "changes-requested", HeadSHA: "h1", Dismissed: true}},
		},
	}
	dismissedSnap := Build(dismissedIn)
	if len(dismissedSnap.Team) != 1 {
		t.Fatalf("want 1 team row, got %d", len(dismissedSnap.Team))
	}
	if dismissedSnap.Team[0].BotDisapproved {
		t.Error("a host-dismissed bot disapproval must be treated as withdrawn, not blocking")
	}
}

// TestBuildTeamRow_BotVerdictApprovedDoesNotSetBotDisapproved (pg2-4dz88.7.5)
// covers the one bot-verdict shape none of this file's existing BotDisapproved
// tests exercise: an ALLOWLISTED approver's STANDING "approved" row. Both
// BotDisapproved==false and BotVerdict==BotVerdictApproved must hold
// together — pinning that "not disapproved" and "positively approved" are
// two different facts, not the same boolean read twice (pg-go-mutate: a
// mutation weakening BotDisapproved's `== BotVerdictDisapproved` check to an
// ordering comparison collapses this exact case, since "approved" sorts
// before "disapproved" lexicographically).
func TestBuildTeamRow_BotVerdictApprovedDoesNotSetBotDisapproved(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	in := BuilderInput{
		GeneratedAt: time.Now(),
		Self:        "alice",
		TeamMembers: []string{"bob"},
		Registry:    reg,
		PRs: []PRInput{
			{
				PR:        api.PR{Repo: "o/r", Number: 30, Author: "bob", URL: "u30", HeadSHA: "h1"},
				Revisions: []store.Revision{{Seq: 1, HeadSHA: "h1"}},
				Approvals: []store.Approval{{Approver: "policy-bot", State: "approved", HeadSHA: "h1"}},
			},
		},
		ApproverAllowlist: []string{"policy-bot"},
	}
	snap := Build(in)
	if len(snap.Team) != 1 {
		t.Fatalf("want 1 team row, got %d", len(snap.Team))
	}
	row := snap.Team[0]
	if row.BotDisapproved {
		t.Error("an allowlisted approver's standing APPROVAL must NOT set BotDisapproved")
	}
	if row.BotVerdict != BotVerdictApproved {
		t.Errorf("BotVerdict = %q, want %q", row.BotVerdict, BotVerdictApproved)
	}
}

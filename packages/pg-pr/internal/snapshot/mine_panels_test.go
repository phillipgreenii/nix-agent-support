package snapshot

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// mineTestReg is a shared empty agent registry: every approver in these
// fixtures counts as human (classifyApprovals' documented nil/empty-registry
// default), which is exactly what these tests need — none of them are
// exercising the agent-vs-human split.
func mineTestReg() *agentregistry.Registry {
	reg, _ := agentregistry.New(nil)
	return reg
}

// ==========================================================================
// TestMinePanelMembership_ThreeViews
// ==========================================================================

// allCleanMineFacts is a MineViewFacts that satisfies none of ACT NOW's
// clauses and is NOT the AWAITING-OTHER-THINGS carve-out — the baseline
// every per-clause case below flips exactly one field away from.
func allCleanMineFacts() MineViewFacts {
	return MineViewFacts{}
}

// TestMinePanelMembership_ThreeViews pins ClassifyMine's per-clause behavior,
// the ACT-NOW>AWAITING-OTHERS>AWAITING-OTHER-THINGS precedence, and the
// exhaustive-partition + never-nil-when-empty properties Build's Snapshot
// fields must uphold (pg2-4dz88.7.4 acceptance criteria).
func TestMinePanelMembership_ThreeViews(t *testing.T) {
	t.Run("one ACT NOW clause at a time routes to ACT NOW", func(t *testing.T) {
		cases := []struct {
			name string
			f    MineViewFacts
		}{
			{"conflict", MineViewFacts{MineActNowFacts: MineActNowFacts{HasConflict: true}}},
			{"blocking bot verdict", MineViewFacts{MineActNowFacts: MineActNowFacts{BlockingBotVerdict: true}}},
			{"WIP ready for promotion", MineViewFacts{MineActNowFacts: MineActNowFacts{WIPReadyForPromotion: true}}},
			{"CI red", MineViewFacts{MineActNowFacts: MineActNowFacts{CIRed: true}}},
			{"open conversation only", MineViewFacts{MineActNowFacts: MineActNowFacts{OpenConversationOnly: true}}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if got := ClassifyMine(tc.f); got != MineViewActNow {
					t.Errorf("ClassifyMine(%+v) = %v, want MineViewActNow", tc.f, got)
				}
			})
		}
	})

	t.Run("no ACT NOW clause and not approved routes to AWAITING OTHERS", func(t *testing.T) {
		f := allCleanMineFacts()
		f.HumanApproved = false
		if got := ClassifyMine(f); got != MineViewAwaitingOthers {
			t.Errorf("ClassifyMine(%+v) = %v, want MineViewAwaitingOthers", f, got)
		}
	})

	t.Run("approved+clean+CI-pending routes to AWAITING OTHER THINGS", func(t *testing.T) {
		f := MineViewFacts{HumanApproved: true, MergeStateClean: true, CIPending: true}
		if got := ClassifyMine(f); got != MineViewAwaitingOtherThings {
			t.Errorf("ClassifyMine(%+v) = %v, want MineViewAwaitingOtherThings", f, got)
		}
	})

	t.Run("approved but NOT clean, CI pending: not the OTHER-THINGS carve-out", func(t *testing.T) {
		f := MineViewFacts{HumanApproved: true, MergeStateClean: false, CIPending: true}
		if got := ClassifyMine(f); got != MineViewAwaitingOthers {
			t.Errorf("ClassifyMine(%+v) = %v, want MineViewAwaitingOthers (not the CI-pending carve-out without a clean merge state)", f, got)
		}
	})

	t.Run("precedence: ACT NOW wins over the AWAITING-OTHER-THINGS carve-out", func(t *testing.T) {
		// A row satisfying BOTH ACT NOW (conflict) and, independently, the raw
		// facts the OTHER-THINGS carve-out looks at (approved+clean+CI-pending)
		// must resolve to ACT NOW.
		f := MineViewFacts{
			MineActNowFacts: MineActNowFacts{HasConflict: true},
			HumanApproved:   true,
			MergeStateClean: true,
			CIPending:       true,
		}
		if got := ClassifyMine(f); got != MineViewActNow {
			t.Errorf("ClassifyMine(%+v) = %v, want MineViewActNow (precedence must override the other-things carve-out)", f, got)
		}
	})

	t.Run("precedence: ACT NOW wins over the not-approved AWAITING-OTHERS default", func(t *testing.T) {
		f := MineViewFacts{
			MineActNowFacts: MineActNowFacts{CIRed: true},
			HumanApproved:   false,
		}
		if got := ClassifyMine(f); got != MineViewActNow {
			t.Errorf("ClassifyMine(%+v) = %v, want MineViewActNow", f, got)
		}
	})

	t.Run("exactly-one-view membership and never-nil-when-empty, via Build", func(t *testing.T) {
		reg := mineTestReg()
		in := BuilderInput{
			Self:     "alice",
			Registry: reg,
			PRs: []PRInput{
				// ACT NOW: merge conflict.
				{PR: api.PR{Repo: "o/r", Number: 1, Author: "alice", Mergeable: "CONFLICTING"}, Ownership: ownership.Mine},
				// AWAITING OTHERS: clean, CI success, no conflict, not approved.
				{
					PR:        api.PR{Repo: "o/r", Number: 2, Author: "alice", MergeStateStatus: "CLEAN"},
					CIRuns:    []api.CIRun{{Name: "build", Status: "completed", Conclusion: "success"}},
					Ownership: ownership.Mine,
				},
				// AWAITING OTHER THINGS: approved, clean, CI pending.
				{
					PR:        api.PR{Repo: "o/r", Number: 3, Author: "alice", HeadSHA: "h3", MergeStateStatus: "CLEAN"},
					Approvals: []store.Approval{{Approver: "carol", State: "approved", HeadSHA: "h3"}},
					CIRuns:    []api.CIRun{{Name: "build", Status: "in_progress"}},
					Ownership: ownership.Mine,
				},
			},
		}
		snap := Build(in)

		if snap.MineActNow == nil || snap.MineAwaitingOthers == nil || snap.MineAwaitingOtherThings == nil {
			t.Fatalf("all three view slices must be non-nil, got ActNow=%v Others=%v OtherThings=%v",
				snap.MineActNow, snap.MineAwaitingOthers, snap.MineAwaitingOtherThings)
		}

		membership := map[int]int{} // PR number -> count of views it appeared in
		for _, r := range snap.MineActNow {
			membership[r.Number]++
		}
		for _, r := range snap.MineAwaitingOthers {
			membership[r.Number]++
		}
		for _, r := range snap.MineAwaitingOtherThings {
			membership[r.Number]++
		}
		for _, n := range []int{1, 2, 3} {
			if membership[n] != 1 {
				t.Errorf("PR #%d appeared in %d views, want exactly 1", n, membership[n])
			}
		}

		wantView := func(rows []MineRow, num int, viewName string) {
			for _, r := range rows {
				if r.Number == num {
					return
				}
			}
			t.Errorf("PR #%d not found in %s", num, viewName)
		}
		wantView(snap.MineActNow, 1, "MineActNow")
		wantView(snap.MineAwaitingOthers, 2, "MineAwaitingOthers")
		wantView(snap.MineAwaitingOtherThings, 3, "MineAwaitingOtherThings")
	})

	t.Run("each view independently empty renders [] not nil, on an empty PR set", func(t *testing.T) {
		snap := Build(BuilderInput{Self: "alice"})
		if snap.MineActNow == nil {
			t.Error("MineActNow must be non-nil ([]MineRow{}) when empty")
		}
		if snap.MineAwaitingOthers == nil {
			t.Error("MineAwaitingOthers must be non-nil ([]MineRow{}) when empty")
		}
		if snap.MineAwaitingOtherThings == nil {
			t.Error("MineAwaitingOtherThings must be non-nil ([]MineRow{}) when empty")
		}
		if len(snap.MineActNow) != 0 || len(snap.MineAwaitingOthers) != 0 || len(snap.MineAwaitingOtherThings) != 0 {
			t.Errorf("expected all three views empty, got ActNow=%d Others=%d OtherThings=%d",
				len(snap.MineActNow), len(snap.MineAwaitingOthers), len(snap.MineAwaitingOtherThings))
		}
	})
}

// ==========================================================================
// TestMineActNowDefaultNotVacuous
// ==========================================================================

// TestMineActNowDefaultNotVacuous is the mechanical stand-in for "any new
// mine-side act-now predicate MUST be validated against real PRs before its
// default is flipped back" (the regression pg2-yf8pr already fixed once: a
// predicate that is almost always false makes the panel print nothing). Over
// the parent bead's five real shapes, ACT NOW must be non-empty for at least
// the conflicted and CI-red shapes.
func TestMineActNowDefaultNotVacuous(t *testing.T) {
	reg := mineTestReg()
	in := BuilderInput{
		Self:     "alice",
		Registry: reg,
		PRs: []PRInput{
			// 1: open+clean+approved — fully ready, nothing outstanding.
			{
				PR:        api.PR{Repo: "o/r", Number: 1, Author: "alice", HeadSHA: "h1", MergeStateStatus: "CLEAN"},
				Approvals: []store.Approval{{Approver: "carol", State: "approved", HeadSHA: "h1"}},
				CIRuns:    []api.CIRun{{Name: "build", Status: "completed", Conclusion: "success"}},
				Ownership: ownership.Mine,
			},
			// 2: open+clean+unapproved — waiting on a reviewer.
			{
				PR:        api.PR{Repo: "o/r", Number: 2, Author: "alice", MergeStateStatus: "CLEAN"},
				CIRuns:    []api.CIRun{{Name: "build", Status: "completed", Conclusion: "success"}},
				Ownership: ownership.Mine,
			},
			// 3: conflicted.
			{
				PR:        api.PR{Repo: "o/r", Number: 3, Author: "alice", Mergeable: "CONFLICTING"},
				Ownership: ownership.Mine,
			},
			// 4: CI-red.
			{
				PR:        api.PR{Repo: "o/r", Number: 4, Author: "alice"},
				CIRuns:    []api.CIRun{{Name: "build", Status: "completed", Conclusion: "failure"}},
				Ownership: ownership.Mine,
			},
			// 5: WIP draft — WIP on, but CI still pending, so it does NOT yet
			// meet the promotion predicate (proves the clause checks every
			// condition, not just Draft+WIP alone).
			{
				PR:        api.PR{Repo: "o/r", Number: 5, Author: "alice", Draft: true},
				WIP:       true,
				CIRuns:    []api.CIRun{{Name: "build", Status: "in_progress"}},
				Ownership: ownership.Mine,
			},
		},
	}
	snap := Build(in)

	inActNow := map[int]bool{}
	for _, r := range snap.MineActNow {
		inActNow[r.Number] = true
	}

	if !inActNow[3] {
		t.Errorf("the conflicted shape (#3) must be in ACT NOW; ACT NOW must not be vacuously empty")
	}
	if !inActNow[4] {
		t.Errorf("the CI-red shape (#4) must be in ACT NOW; ACT NOW must not be vacuously empty")
	}
	if inActNow[1] {
		t.Errorf("the fully-approved-and-clean shape (#1) must NOT be in ACT NOW")
	}
	if inActNow[2] {
		t.Errorf("the merely-unapproved shape (#2) must NOT be in ACT NOW")
	}
	if inActNow[5] {
		t.Errorf("the WIP-draft-with-CI-still-pending shape (#5) must NOT be in ACT NOW (promotion predicate not yet met)")
	}
}

// ==========================================================================
// TestMineActNow_OpenConversationOnlyBlock
// ==========================================================================

// TestMineActNow_OpenConversationOnlyBlock: a PR with CI green, approved, no
// conflict, MergeStateStatus not CLEAN, and one unresolved thread lands in
// ACT NOW; the same PR with the thread resolved (or with no thread at all)
// does NOT get flagged by this clause.
func TestMineActNow_OpenConversationOnlyBlock(t *testing.T) {
	reg := mineTestReg()
	base := func(number int, comments []api.Comment) PRInput {
		return PRInput{
			PR:        api.PR{Repo: "o/r", Number: number, Author: "alice", HeadSHA: "h1", MergeStateStatus: "BLOCKED"},
			Approvals: []store.Approval{{Approver: "carol", State: "approved", HeadSHA: "h1"}},
			CIRuns:    []api.CIRun{{Name: "build", Status: "completed", Conclusion: "success"}},
			Comments:  comments,
			Ownership: ownership.Mine,
		}
	}

	cases := []struct {
		name       string
		comments   []api.Comment
		wantActNow bool
	}{
		{
			name:       "unresolved inline thread",
			comments:   []api.Comment{{ID: "c1", Author: "bob", Body: "please fix", ThreadID: "t1", Resolved: false}},
			wantActNow: true,
		},
		{
			name:       "resolved inline thread",
			comments:   []api.Comment{{ID: "c1", Author: "bob", Body: "please fix", ThreadID: "t1", Resolved: true}},
			wantActNow: false,
		},
		{
			name:       "no thread at all",
			comments:   nil,
			wantActNow: false,
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := Build(BuilderInput{
				Self:     "alice",
				Registry: reg,
				PRs:      []PRInput{base(100+i, tc.comments)},
			})
			got := false
			for _, r := range snap.MineActNow {
				if r.Number == 100+i {
					got = true
				}
			}
			if got != tc.wantActNow {
				t.Errorf("MineActNow membership = %v, want %v", got, tc.wantActNow)
			}
		})
	}
}

// ==========================================================================
// OpenConversationOnly guard tests
// ==========================================================================

// openConvBaseline returns a fact tuple that DOES satisfy OpenConversationOnly
// — the baseline each guard test below flips exactly one axis away from, with
// a sanity check that the unflipped baseline still fires (so a guard test
// passing can't be explained by OpenConversationOnly being vacuously false).
func openConvBaseline() (humanApproved bool, mergeStateStatus, ci string, hasConflict bool, comments []api.Comment) {
	return true, "BLOCKED", "success", false, []api.Comment{{ID: "c1", ThreadID: "t1", Resolved: false}}
}

func TestOpenConversation_IgnoresTopLevelComments(t *testing.T) {
	humanApproved, mergeStateStatus, ci, hasConflict, _ := openConvBaseline()
	topLevelOnly := []api.Comment{{ID: "c1", Author: "bob", Body: "lgtm", ThreadID: "", Resolved: false}}
	if OpenConversationOnly(humanApproved, mergeStateStatus, ci, hasConflict, topLevelOnly) {
		t.Errorf("a top-level (non-thread) comment must not satisfy the ThreadID gate")
	}
	if _, _, _, _, baseComments := openConvBaseline(); !OpenConversationOnly(humanApproved, mergeStateStatus, ci, hasConflict, baseComments) {
		t.Fatalf("sanity check failed: the baseline (inline unresolved thread) should satisfy the predicate")
	}
}

func TestOpenConversation_CleanMergeStateDoesNotFire(t *testing.T) {
	humanApproved, _, ci, hasConflict, comments := openConvBaseline()
	if OpenConversationOnly(humanApproved, "CLEAN", ci, hasConflict, comments) {
		t.Errorf("MergeStateStatus == CLEAN must not satisfy the clause even with an unresolved thread")
	}
	if _, baseMergeState, _, _, _ := openConvBaseline(); !OpenConversationOnly(humanApproved, baseMergeState, ci, hasConflict, comments) {
		t.Fatalf("sanity check failed: the baseline (non-CLEAN merge state) should satisfy the predicate")
	}
}

func TestOpenConversation_RESTFallbackEmptyDoesNotFire(t *testing.T) {
	humanApproved, _, ci, hasConflict, comments := openConvBaseline()
	if OpenConversationOnly(humanApproved, "", ci, hasConflict, comments) {
		t.Errorf("empty MergeStateStatus (REST-fallback degenerate value) must not satisfy the clause")
	}
	if _, baseMergeState, _, _, _ := openConvBaseline(); !OpenConversationOnly(humanApproved, baseMergeState, ci, hasConflict, comments) {
		t.Fatalf("sanity check failed: the baseline (non-empty merge state) should satisfy the predicate")
	}
}

func TestOpenConversation_RequiresApproval(t *testing.T) {
	_, mergeStateStatus, ci, hasConflict, comments := openConvBaseline()
	if OpenConversationOnly(false, mergeStateStatus, ci, hasConflict, comments) {
		t.Errorf("no standing human approval must not satisfy the clause")
	}
	if baseApproved, _, _, _, _ := openConvBaseline(); !OpenConversationOnly(baseApproved, mergeStateStatus, ci, hasConflict, comments) {
		t.Fatalf("sanity check failed: the baseline (human approved) should satisfy the predicate")
	}
}

// ==========================================================================
// Supporting unit coverage for the other ACT-NOW clauses (blockingBotVerdict,
// wipReadyForPromotion) not otherwise named in the acceptance criteria but
// exercised end-to-end above; a couple of direct guard tests here pin the
// allowlist-vs-agentregistry distinction and staleness handling precisely.
// ==========================================================================

func TestBlockingBotVerdict_OnlyAllowlistedLoginCounts(t *testing.T) {
	p := PRInput{
		PR:        api.PR{HeadSHA: "h1"},
		Approvals: []store.Approval{{Approver: "not-allowlisted-bot", State: "changes-requested", HeadSHA: "h1"}},
	}
	if blockingBotVerdict(p, allowlistSet([]string{"allowlisted-bot"})) {
		t.Errorf("a changes-requested row from a login NOT on the allowlist must not block")
	}
	p.Approvals[0].Approver = "allowlisted-bot"
	if !blockingBotVerdict(p, allowlistSet([]string{"allowlisted-bot"})) {
		t.Errorf("a changes-requested row from an ALLOWLISTED login must block")
	}
}

func TestBlockingBotVerdict_StaleRowDoesNotBlock(t *testing.T) {
	p := PRInput{
		PR:        api.PR{HeadSHA: "h2"}, // current head has moved past the approval's head
		Approvals: []store.Approval{{Approver: "allowlisted-bot", State: "changes-requested", HeadSHA: "h1"}},
	}
	if blockingBotVerdict(p, allowlistSet([]string{"allowlisted-bot"})) {
		t.Errorf("a stale (superseded-head) changes-requested row must not block")
	}
}

func TestWIPReadyForPromotion_RequiresEveryCondition(t *testing.T) {
	greenCI := []api.CIRun{{Name: "build", Status: "completed", Conclusion: "success"}}
	base := func() PRInput {
		return PRInput{PR: api.PR{Draft: true, HeadSHA: "h1"}, WIP: true, CIRuns: greenCI}
	}

	if !wipReadyForPromotion(base(), "success", false, nil) {
		t.Fatalf("sanity check failed: draft+WIP+conflict-free+CI-green+no-bot-verdict should be ready for promotion")
	}
	notDraft := base()
	notDraft.PR.Draft = false
	if wipReadyForPromotion(notDraft, "success", false, nil) {
		t.Errorf("a non-draft PR must never satisfy the WIP-promotion clause")
	}
	notWIP := base()
	notWIP.WIP = false
	if wipReadyForPromotion(notWIP, "success", false, nil) {
		t.Errorf("WIP==false must never satisfy the WIP-promotion clause")
	}
	if wipReadyForPromotion(base(), "success", true /* hasConflict */, nil) {
		t.Errorf("a conflict must block the WIP-promotion clause")
	}
	if wipReadyForPromotion(base(), "pending", false, nil) {
		t.Errorf("CI not green must block the WIP-promotion clause")
	}
	withBotVerdict := base()
	withBotVerdict.Approvals = []store.Approval{{Approver: "allowlisted-bot", State: "changes-requested", HeadSHA: "h1"}}
	if wipReadyForPromotion(withBotVerdict, "success", false, allowlistSet([]string{"allowlisted-bot"})) {
		t.Errorf("a blocking bot verdict must block the WIP-promotion clause")
	}
}

package sync

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
)

// These tests guard bead pg2-jj0ym: the anchor content hash MUST NOT depend
// on the anchor's CURRENT labels/priority. Before the fix the hash covered
// the conflict-priority delta (priorityDelta against the live labels), so
// every applied delta made the next run recompute an EMPTY delta, see a
// different hash, and rewrite the anchor again with identical content; and a
// transient UNKNOWN mergeability read (73% of stored PRs on 2026-10-07)
// cleared the conflict episode and re-opened it on the next DIRTY read. Each
// anchor write bumps the bead's updated_at, which the issue changes feed
// turns into a desk-issue dispatch.

func TestPRShowFields_ConflictUnknown(t *testing.T) {
	cases := []struct {
		name string
		pr   prShowFields
		want bool
	}{
		{"both UNKNOWN", prShowFields{Mergeable: "UNKNOWN", MergeStateStatus: "UNKNOWN"}, true},
		{"mergeable UNKNOWN, status absent", prShowFields{Mergeable: "UNKNOWN"}, true},
		{"status UNKNOWN, mergeable absent", prShowFields{MergeStateStatus: "UNKNOWN"}, true},
		{"conflicting is a definite answer", prShowFields{Mergeable: "CONFLICTING", MergeStateStatus: "UNKNOWN"}, false},
		{"dirty is a definite answer", prShowFields{Mergeable: "UNKNOWN", MergeStateStatus: "DIRTY"}, false},
		{"mergeable is a definite answer", prShowFields{Mergeable: "MERGEABLE", MergeStateStatus: "UNKNOWN"}, false},
		{"known status with unknown mergeable is definite", prShowFields{Mergeable: "UNKNOWN", MergeStateStatus: "BLOCKED"}, false},
		{"no mergeability data at all is not UNKNOWN", prShowFields{}, false},
	}
	for _, c := range cases {
		if got := c.pr.conflictUnknown(); got != c.want {
			t.Errorf("%s: conflictUnknown() = %v, want %v", c.name, got, c.want)
		}
	}
}

// conflictFacts builds Facts for the fixture PR with the given mergeability
// reads and, when anchorID is non-empty, a work-beads fan-out carrying that
// anchor with the given labels/priority.
func conflictFacts(mergeable, status, anchorID string, anchorLabels []string, anchorPriority string) gather.Facts {
	f := gather.Facts{
		PRShow:  prFixture(map[string]any{"mergeable": mergeable, "merge_state_status": status}),
		HeadSHA: fixtureHeadSHA,
	}
	if anchorID != "" {
		f.WorkBeads = workBeadsFixture(map[string]any{
			"id": anchorID, "title": fixturePRKey + ": fix the bug", "state": "open",
			"priority": anchorPriority, "labels": anchorLabels,
			"metadata": map[string]string{"repo": fixtureRepo, "pr_number": "42"},
		})
	}
	return f
}

func TestSync_Anchor_AppliedConflictDeltaDoesNotRewriteOnNextRun(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)
	interp := interpFor("mine", nil)
	ctx := context.Background()

	// Run 1: the PR is conflicting; the anchor is created (pbase stash + nudge).
	if err := s.Sync(ctx, fixtureRepo, fixtureEntity, gather.ChangeAdded,
		conflictFacts("CONFLICTING", "DIRTY", "", nil, ""), interp); err != nil {
		t.Fatalf("Sync 1: %v", err)
	}
	anchor, _, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor)

	// Run 2: same PR content, but the anchor now carries the applied delta
	// (pbase:2 label, priority raised to P1). The delta against the live
	// labels is empty, yet nothing about the PR changed: no write.
	before := len(readCallRecords(t, recordFile))
	if err := s.Sync(ctx, fixtureRepo, fixtureEntity, gather.ChangeChanged,
		conflictFacts("CONFLICTING", "DIRTY", anchor.BeadID, []string{"pbase:2"}, "P1"), interp); err != nil {
		t.Fatalf("Sync 2: %v", err)
	}
	if calls := anchorCalls(t, recordFile, anchor.BeadID, before); len(calls) != 0 {
		t.Fatalf("an applied conflict delta was rewritten on the next run (echo write): %+v", calls)
	}
}

func TestSync_Anchor_UnknownMergeabilityHoldsConflictEpisode(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)
	interp := interpFor("mine", nil)
	ctx := context.Background()

	if err := s.Sync(ctx, fixtureRepo, fixtureEntity, gather.ChangeAdded,
		conflictFacts("CONFLICTING", "DIRTY", "", nil, ""), interp); err != nil {
		t.Fatalf("Sync 1: %v", err)
	}
	anchor, _, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor)
	withBaseline := func(mergeable, status string) gather.Facts {
		return conflictFacts(mergeable, status, anchor.BeadID, []string{"pbase:2"}, "P1")
	}

	// A transient UNKNOWN read neither clears the episode nor writes.
	before := len(readCallRecords(t, recordFile))
	if err := s.Sync(ctx, fixtureRepo, fixtureEntity, gather.ChangeChanged, withBaseline("UNKNOWN", "UNKNOWN"), interp); err != nil {
		t.Fatalf("Sync UNKNOWN: %v", err)
	}
	if calls := anchorCalls(t, recordFile, anchor.BeadID, before); len(calls) != 0 {
		t.Fatalf("an UNKNOWN mergeability read wrote the anchor: %+v", calls)
	}

	// The next DIRTY read after the UNKNOWN one is the same episode: no write.
	before = len(readCallRecords(t, recordFile))
	if err := s.Sync(ctx, fixtureRepo, fixtureEntity, gather.ChangeChanged, withBaseline("CONFLICTING", "DIRTY"), interp); err != nil {
		t.Fatalf("Sync DIRTY again: %v", err)
	}
	if calls := anchorCalls(t, recordFile, anchor.BeadID, before); len(calls) != 0 {
		t.Fatalf("DIRTY after UNKNOWN re-opened the episode and wrote the anchor: %+v", calls)
	}
}

func TestSync_Anchor_ConflictClearsOnceOnDefiniteCleanRead(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)
	interp := interpFor("mine", nil)
	ctx := context.Background()

	if err := s.Sync(ctx, fixtureRepo, fixtureEntity, gather.ChangeAdded,
		conflictFacts("CONFLICTING", "DIRTY", "", nil, ""), interp); err != nil {
		t.Fatalf("Sync 1: %v", err)
	}
	anchor, _, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor)

	// A definite MERGEABLE read ends the episode: exactly one update that
	// drops the pbase marker and restores the baseline priority.
	before := len(readCallRecords(t, recordFile))
	later := time.Date(2026, 9, 17, 1, 0, 0, 0, time.UTC)
	s2 := New(testConfig(ModeApply), s.store, WithClock(interpret.FixedClock(later)))
	if err := s2.Sync(ctx, fixtureRepo, fixtureEntity, gather.ChangeChanged,
		conflictFacts("MERGEABLE", "CLEAN", anchor.BeadID, []string{"pbase:2"}, "P1"), interp); err != nil {
		t.Fatalf("Sync clear: %v", err)
	}
	calls := anchorCalls(t, recordFile, anchor.BeadID, before)
	if len(calls) != 1 || calls[0].verb() != "issue update" {
		t.Fatalf("want exactly one anchor update clearing the episode, got %+v", calls)
	}
	var sawRemove bool
	for i, a := range calls[0].Args {
		if a == "--remove-label" && i+1 < len(calls[0].Args) && calls[0].Args[i+1] == "pbase:2" {
			sawRemove = true
		}
	}
	if !sawRemove {
		t.Fatalf("clearing update did not remove pbase:2: %v", calls[0].Args)
	}

	// The follow-up run (anchor now clean) is quiet: the clear costs ONE
	// write, not the two the delta-based hash cost.
	before = len(readCallRecords(t, recordFile))
	if err := s2.Sync(ctx, fixtureRepo, fixtureEntity, gather.ChangeChanged,
		conflictFacts("MERGEABLE", "CLEAN", anchor.BeadID, nil, "P2"), interp); err != nil {
		t.Fatalf("Sync after clear: %v", err)
	}
	if calls := anchorCalls(t, recordFile, anchor.BeadID, before); len(calls) != 0 {
		t.Fatalf("a cleared episode was rewritten on the next run: %+v", calls)
	}
}

// TestSync_Anchor_NoConflictHashUnchangedByFix pins that an anchor with no
// conflict hashes exactly as it did before this fix, so deployed ledgers do
// not trigger a one-time rewrite of every quiet anchor.
func TestSync_Anchor_NoConflictHashUnchangedByFix(t *testing.T) {
	got := contentHash(anchorHashInput{
		State: "open", Branch: "feature", Base: "main", Author: "alice", URL: "https://example.test/pr/42",
		AddLabels: sortedCopy(nil), RemoveLabels: sortedCopy(nil),
	})
	s := newTestSyncer(t, ModeApply)
	withFactory(t)
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded,
		conflictFacts("MERGEABLE", "CLEAN", "", nil, ""), interpFor("team", nil)); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	// team + non-draft => needs review => anchor created.
	anchor, found, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor)
	if !found || anchor.LastSyncedContentHash != got {
		t.Fatalf("no-conflict anchor hash = %q, want the pre-fix value %q", anchor.LastSyncedContentHash, got)
	}
}

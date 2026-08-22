package sync

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// ingestFeedbackToStore MUST record a teammate (non-self) CHANGES_REQUESTED
// review as its own row in the per-approver pr_approval table (pg2-4dz88.1.8)
// — distinct from that same PR's teammate APPROVED row, and distinct from a
// teammate whose only review is COMMENTED (which must not land in
// pr_approval at all, and specifically must never be mistaken for
// changes-requested).
func TestIngestFeedbackToStore_RecordsTeammateChangesRequested(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)

	pr := api.PR{
		Repo: "o/r", Number: 7, State: "open", Author: "phillipg",
		HeadSHA: "sha-head", BaseSHA: "sha-base",
		URL: "https://github.com/o/r/pull/7",
	}
	enriched := &vcs.EnrichedPR{
		PR: pr,
		Reviews: []api.Review{
			// A teammate approves the current head.
			{Author: "alice", State: "APPROVED", CommitOID: "sha-head", SubmittedAt: "2026-07-01T00:00:00Z"},
			// A different teammate requests changes at the same head.
			{Author: "bob", State: "CHANGES_REQUESTED", CommitOID: "sha-head", SubmittedAt: "2026-07-02T00:00:00Z"},
			// A third teammate only comments — must not be conflated with
			// changes-requested and must not land in pr_approval at all.
			{Author: "carol", State: "COMMENTED", CommitOID: "sha-head", SubmittedAt: "2026-07-03T00:00:00Z"},
		},
	}

	e, err := New(Deps{
		Cfg: &config.Config{
			SelfLogin: "phillipg",
			Repos:     []config.RepoConfig{{Remote: "o/r", VCS: "github"}},
		},
		VCS:      map[string]VCSProvider{"github": newFakeVCS()},
		Beads:    &noopBeads{},
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := e.ingestFeedbackToStore(ctx, "o/r", pr, enriched); err != nil {
		t.Fatalf("ingestFeedbackToStore: %v", err)
	}

	storedPR, err := db.GetPR(ctx, "o/r", 7)
	if err != nil || storedPR == nil {
		t.Fatalf("GetPR: pr=%v err=%v", storedPR, err)
	}

	bobApproval, err := db.GetApproval(ctx, storedPR.ID, "bob")
	if err != nil {
		t.Fatalf("GetApproval(bob): %v", err)
	}
	if bobApproval == nil {
		t.Fatalf("bob's CHANGES_REQUESTED review must land in pr_approval")
	}
	if bobApproval.State != "changes-requested" {
		t.Errorf("bob State = %q, want \"changes-requested\"", bobApproval.State)
	}
	if bobApproval.HeadSHA != "sha-head" || bobApproval.ObservedAt != "2026-07-02T00:00:00Z" {
		t.Errorf("bob approval = %+v, want head_sha=sha-head observed_at=2026-07-02T00:00:00Z", bobApproval)
	}

	aliceApproval, err := db.GetApproval(ctx, storedPR.ID, "alice")
	if err != nil || aliceApproval == nil {
		t.Fatalf("GetApproval(alice): err=%v got=%+v", err, aliceApproval)
	}
	if aliceApproval.State != "approved" {
		t.Errorf("alice State = %q, want \"approved\" (bob's changes-requested must not overwrite alice's row)", aliceApproval.State)
	}

	carolApproval, err := db.GetApproval(ctx, storedPR.ID, "carol")
	if err != nil {
		t.Fatalf("GetApproval(carol): %v", err)
	}
	if carolApproval != nil {
		t.Errorf("carol's COMMENTED-only review must not land in pr_approval, got %+v", carolApproval)
	}

	// A teammate's CHANGES_REQUESTED/COMMENTED must NOT touch the (unrelated)
	// others_approved "off the hook" marker: it must reflect ONLY alice's
	// APPROVED observation. If bob's or carol's later timestamps had leaked
	// into this marker (a regression this loop must not cause), OthersApprovedAt
	// would read "2026-07-02..." or "2026-07-03..." instead of alice's
	// "2026-07-01...".
	revs, err := db.ListRevisions(ctx, storedPR.ID)
	if err != nil {
		t.Fatalf("ListRevisions: %v", err)
	}
	if len(revs) != 1 {
		t.Fatalf("want 1 revision, got %d: %+v", len(revs), revs)
	}
	if !revs[0].OthersApproved {
		t.Errorf("alice's APPROVED review must still set others_approved")
	}
	if revs[0].OthersApprovedAt != "2026-07-01T00:00:00Z" {
		t.Errorf("others_approved_at = %q, want alice's timestamp 2026-07-01T00:00:00Z (bob/carol must not touch this marker)", revs[0].OthersApprovedAt)
	}
}

package sync

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// ingestFeedbackToStore MUST record a teammate (non-self) APPROVED review as the
// others_approved marker on the matching revision, and MUST NOT record the
// viewer's own approval there (X3). This keeps the attention predicate
// store-derived.
func TestIngestFeedbackToStore_MarksOthersApproved(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)

	pr := api.PR{
		Repo: "o/r", Number: 7, State: "open", Author: "alice", // teammate-authored
		HeadSHA: "sha-head", BaseSHA: "sha-base",
		URL: "https://github.com/o/r/pull/7",
	}
	enriched := &vcs.EnrichedPR{
		PR: pr,
		Reviews: []api.Review{
			// The viewer's OWN approval — must NOT set others_approved.
			{Author: "phillipg", State: "APPROVED", CommitOID: "sha-head", SubmittedAt: "2026-07-01T00:00:00Z"},
			// A teammate's approval — MUST set others_approved on the sha-head revision.
			{Author: "bob", State: "APPROVED", CommitOID: "sha-head", SubmittedAt: "2026-07-02T00:00:00Z"},
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
	revs, err := db.ListRevisions(ctx, storedPR.ID)
	if err != nil {
		t.Fatalf("ListRevisions: %v", err)
	}
	if len(revs) != 1 {
		t.Fatalf("want 1 revision, got %d: %+v", len(revs), revs)
	}
	if !revs[0].OthersApproved {
		t.Errorf("teammate approval must set others_approved on the head revision")
	}
	if revs[0].OthersApprovedAt != "2026-07-02T00:00:00Z" {
		t.Errorf("others_approved_at = %q, want bob's timestamp", revs[0].OthersApprovedAt)
	}
	// The viewer's own approval is still recorded in my_review_state (unchanged
	// mySubmittedReviews path) — proving self and others are separated (X3).
	if revs[0].MyReviewState != "approved" {
		t.Errorf("my_review_state = %q, want \"approved\" (self path still records)", revs[0].MyReviewState)
	}
}

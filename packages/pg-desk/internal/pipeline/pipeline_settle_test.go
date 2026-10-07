package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Reconcile re-drives a PR whose review request is waiting out a head's
// settle window once that window has elapsed (bead pg2-a9yhn): sync runs only
// on events, so without this a quiet PR would wait for its next unrelated
// event before the settled head is requested.
func TestPipelineReconcile_RedrivesPRWhoseReviewSettleWindowElapsed(t *testing.T) {
	first := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	clk := &steppingClock{now: first.Add(time.Minute)}
	reads := 0
	p := newTestPipeline(t, openPRGatherer(t, &reads), nil)
	p.clock = clk

	seedReconcileEntity(t, p, "7", "") // no anchor row: only the review row below is a candidate
	if err := p.store.UpsertLedger(store.LedgerEntry{
		Repo: "acme/widgets", EntityType: "pr", EntityID: "7", Kind: "review-request",
		BeadID: "bead-review-7", LastReviewedHeadSHA: "sha-0",
		FirstSeenHeadSHA: "sha-1", FirstSeenHeadAt: first.Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}

	syncCalls := 0
	p.syncer = syncerFunc(func(ctx context.Context, repo, entityID string, change gather.ChangeKind, facts gather.Facts, interp interpret.Interpretation) error {
		syncCalls++
		return nil
	})

	// One minute in, inside the 2m default window: nothing to do.
	if err := p.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile inside the window: %v", err)
	}
	if syncCalls != 0 || reads != 0 {
		t.Fatalf("a row still settling was re-driven (sync calls %d, reads %d)", syncCalls, reads)
	}

	// Past the window: the PR is re-driven through an ordinary run.
	clk.now = first.Add(2 * time.Minute)
	if err := p.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile after the window: %v", err)
	}
	if syncCalls != 1 {
		t.Fatalf("sync calls = %d after the window elapsed, want 1", syncCalls)
	}

	// A review row with no pending head is not a candidate.
	if err := p.store.UpsertLedger(store.LedgerEntry{
		Repo: "acme/widgets", EntityType: "pr", EntityID: "7", Kind: "review-request",
		BeadID: "bead-review-7", LastReviewedHeadSHA: "sha-1",
	}); err != nil {
		t.Fatal(err)
	}
	clk.now = first.Add(time.Hour)
	if err := p.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile with no pending head: %v", err)
	}
	if syncCalls != 1 {
		t.Fatalf("a settled row was re-driven again (sync calls = %d)", syncCalls)
	}
}

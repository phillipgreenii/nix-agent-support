package sync

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
)

// TestSync_WriteTally_ReportsAnchorWrites guards bead pg2-dpml1: the per-run
// record learns whether Sync wrote the anchor, and why, through the tally in
// the context, using the same cause vocabulary as the anchor-write log.
func TestSync_WriteTally_ReportsAnchorWrites(t *testing.T) {
	s, _ := newLoggedSyncer(t)
	withFactory(t)
	interp := interpFor("mine", nil)

	ctx, created := WithWriteTally(context.Background())
	if err := s.Sync(ctx, fixtureRepo, fixtureEntity, gather.ChangeAdded,
		conflictFacts("MERGEABLE", "CLEAN", "", nil, ""), interp); err != nil {
		t.Fatalf("Sync 1: %v", err)
	}
	if !created.AnchorWritten() || created.AnchorCause() != "created" {
		t.Fatalf("creation: written=%v cause=%q, want true/created", created.AnchorWritten(), created.AnchorCause())
	}
	anchor, _, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor)

	// An unchanged anchor is not written, so the tally stays empty.
	ctx, quiet := WithWriteTally(context.Background())
	if err := s.Sync(ctx, fixtureRepo, fixtureEntity, gather.ChangeSweep,
		conflictFacts("MERGEABLE", "CLEAN", anchor.BeadID, nil, "P2"), interp); err != nil {
		t.Fatalf("Sync 2: %v", err)
	}
	if quiet.AnchorWritten() || quiet.AnchorCause() != "" {
		t.Fatalf("no-op sync: written=%v cause=%q, want false/empty", quiet.AnchorWritten(), quiet.AnchorCause())
	}

	// A Sync with no tally in its context still works.
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeSweep,
		conflictFacts("MERGEABLE", "CLEAN", anchor.BeadID, nil, "P2"), interp); err != nil {
		t.Fatalf("Sync 3 (no tally): %v", err)
	}

	// A nil tally is safe to read.
	var none *WriteTally
	if none.AnchorWritten() || none.AnchorCause() != "" {
		t.Fatal("nil tally must read as no write")
	}
}

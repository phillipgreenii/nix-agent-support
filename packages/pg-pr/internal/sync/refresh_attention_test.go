package sync

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// TestRefreshPR_TeamPR_ThreadsStoreRevisions: refreshPR's returned snapshot
// input MUST carry the store revisions so the dashboard read model is
// store-derived. (This test used to also assert refreshPR emits a
// pr.attention event; that bead-projection mechanism was removed by
// pg2-ynhr.5 — see internal/beadsbridge's package doc. The dashboard's own
// attention verdict, unaffected, is covered by internal/snapshot's
// NeedsAttention tests, which this store-threading behavior feeds.)
func TestRefreshPR_TeamPR_ThreadsStoreRevisions(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)

	// Seed a team PR row + a revision (nobody approved, I haven't reviewed).
	prID, err := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 7, Ownership: "team", State: "open", HeadSHA: "h1"})
	if err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}
	if _, _, err := db.RecordRevision(ctx, prID, "h1", "b1"); err != nil {
		t.Fatalf("RecordRevision: %v", err)
	}

	bdc := &refreshFakeBeads{}
	pr := api.PR{
		Repo: "o/r", Number: 7, State: "open",
		Author: "teammate", HeadSHA: "h1", Base: "b1",
		URL: "https://github.com/o/r/pull/7",
	}
	e := newRefreshEngineWithStore(t, "me", bdc, pr, db)

	in, err := e.refreshPR(ctx, "o/r", 7)
	if err != nil {
		t.Fatalf("refreshPR: %v", err)
	}
	if in == nil {
		t.Fatal("active team PR must yield a non-nil snapshot input")
	}

	// Read model: the snapshot input threads the store revisions.
	if len(in.Revisions) != 1 || in.Revisions[0].HeadSHA != "h1" {
		t.Fatalf("snapshot input must carry store revisions, got %+v", in.Revisions)
	}
}

package sync

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// refreshPR for an active TEAM PR MUST emit a pr.attention event derived from
// persisted facts (per-tick, self-healing), and the returned snapshot input MUST
// carry the store revisions + draft-review-closed signal so the dashboard read
// model is store-derived (matching the bead).
func TestRefreshPR_TeamPR_EmitsAttentionAndThreadsStore(t *testing.T) {
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

	// Draft review is READY (bead closed) → attention needed.
	bdc := &attnFinderBeads{closed: true, found: true}
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

	// (a) Write model: a pr.attention{need:true} event was emitted.
	evs := attentionEvents(t, db)
	if len(evs) != 1 {
		t.Fatalf("want exactly 1 pr.attention event, got %d: %+v", len(evs), evs)
	}
	if !evs[0].Need || evs[0].Reason != "draft-review-ready-unapproved" {
		t.Fatalf("attention event = %+v, want need:true draft-review-ready-unapproved", evs[0])
	}

	// (b) Read model: the snapshot input threads the store revisions + readiness.
	if len(in.Revisions) != 1 || in.Revisions[0].HeadSHA != "h1" {
		t.Fatalf("snapshot input must carry store revisions, got %+v", in.Revisions)
	}
	if !in.DraftReviewClosed {
		t.Fatal("snapshot input must carry DraftReviewClosed=true (draft review ready)")
	}
}

// A MINE PR does NOT emit pr.attention (attention is a teammate-PR signal only).
func TestRefreshPR_MinePR_NoAttentionEmit(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	prID, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 3, Ownership: "mine", State: "open", HeadSHA: "h1"})
	if _, _, err := db.RecordRevision(ctx, prID, "h1", "b1"); err != nil {
		t.Fatalf("RecordRevision: %v", err)
	}
	bdc := &attnFinderBeads{closed: true, found: true}
	pr := api.PR{Repo: "o/r", Number: 3, State: "open", Author: "me", HeadSHA: "h1", URL: "https://github.com/o/r/pull/3"}
	e := newRefreshEngineWithStore(t, "me", bdc, pr, db)

	if _, err := e.refreshPR(ctx, "o/r", 3); err != nil {
		t.Fatalf("refreshPR: %v", err)
	}
	if evs := attentionEvents(t, db); len(evs) != 0 {
		t.Fatalf("mine PR must not emit pr.attention, got %+v", evs)
	}
}

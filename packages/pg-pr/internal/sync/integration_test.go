package sync

// integration_test.go wires the REAL ingest → outbox → event.Dispatcher →
// beadsbridge chain end-to-end and asserts the bead client received the
// feedback.created-driven calls (i.e. the process-feedback bead path ran).
//
// This is the full-chain test that proves the payload contract between each
// link — previously only tested in isolation — holds together.
//
// Ordering invariant: pr.opened is enqueued (committed) BEFORE feedback.created
// for any given PR. The outbox drains in FIFO order (ORDER BY id), so the bridge
// always sees the PR-bead creation before the feedback handler runs. These tests
// verify that invariant and catch any regression that breaks it.

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/beadsbridge"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// fullChainBeadClient is a minimal local fake satisfying beadsbridge.BeadClient.
// It records calls so the test can assert the bridge ran the expected path.
//
// Critically, ReconcileMergeRequest populates findResult so that a subsequent
// FindByRepoAndNumber returns the bead — mirroring what the real beads backend
// does. Starting with findResult == nil means the feedback handler will fail
// ("no merge-request bead") unless ReconcileMergeRequest ran first, which only
// happens when the pr.opened event is processed before feedback.created.
type fullChainBeadClient struct {
	ensureCalls     []beads.MergeRequestFields
	findCalls       []string // repo args
	findResult      *beads.MergeRequest
	createCycles    int
	findOpenResults map[string]bool // prBeadID → open
}

// FindByRepoAndNumberUncached is the read-once (pg2-pz7y8) fetch the bridge
// issues before ReconcileMergeRequest; this fake has no cache to bypass, so it
// is the same lookup as FindByRepoAndNumber.
func (f *fullChainBeadClient) FindByRepoAndNumberUncached(_ context.Context, _ string, _ int) (*beads.MergeRequest, error) {
	return f.findResult, nil
}

func (f *fullChainBeadClient) ReconcileMergeRequest(_ context.Context, _ *beads.MergeRequest, _ string, fields beads.MergeRequestFields, _, _, _ bool) (string, bool, error) {
	f.ensureCalls = append(f.ensureCalls, fields)
	f.findResult = &beads.MergeRequest{ID: "mr-chain-1", Status: "open"} // bridge created it
	return "mr-chain-1", false, nil
}

func (f *fullChainBeadClient) FindByRepoAndNumber(_ context.Context, repo string, _ int) (*beads.MergeRequest, error) {
	f.findCalls = append(f.findCalls, repo)
	return f.findResult, nil
}

func (f *fullChainBeadClient) CloseMergeRequest(_ context.Context, _, _ string) error { return nil }

func (f *fullChainBeadClient) ListChildrenOfPR(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (f *fullChainBeadClient) CreateProcessingCycle(_ context.Context, _ beads.CreateProcessingCycleInput) (string, error) {
	f.createCycles++
	return "cycle-chain-1", nil
}

func (f *fullChainBeadClient) ResolveProcessingCycle(_ context.Context, _, prBeadID string) (beads.ProcessingCycleState, error) {
	if f.findOpenResults[prBeadID] {
		return beads.ProcessingCycleState{Open: &beads.ProcessingCycle{ID: "cycle-chain-1", Status: "open"}}, nil
	}
	return beads.ProcessingCycleState{}, nil
}

func (f *fullChainBeadClient) AppendProcessingCycleNote(context.Context, string, string, string, []string) error {
	return nil
}

func (f *fullChainBeadClient) CloseProcessingCycle(_ context.Context, _, _ string) error { return nil }

func (f *fullChainBeadClient) CloseFeedback(_ context.Context, _, _ string) error { return nil }

func (f *fullChainBeadClient) EnsureDraftReviewBead(context.Context, string, string, bool) (string, error) {
	return "", nil
}

func (f *fullChainBeadClient) EnsureAttentionBead(context.Context, string, string) (string, error) {
	return "", nil
}

func (f *fullChainBeadClient) CloseAttentionBead(context.Context, string, string) error { return nil }

func (f *fullChainBeadClient) EnsureDraftReviewMineLabel(context.Context, string) error { return nil }

// compile-time check: fullChainBeadClient satisfies the bridge interface.
var _ beadsbridge.BeadClient = (*fullChainBeadClient)(nil)

// TestFullChain_IngestOutboxDispatchBridge is the full end-to-end wiring test:
//
//	emitPREvent(pr.opened) → outbox row #1
//	ingestFeedbackToStore  → feedback row + outbox row #2 (feedback.created)
//	→ store.RunOutbox (ORDER BY id) → event.Dispatcher.Dispatch
//	→ beadsbridge.Handler.Handle:
//	    row #1: pr.opened  → EnsureMergeRequest (sets findResult)
//	    row #2: feedback.created → FindByRepoAndNumber (now finds it) → CreateProcessingCycle
//	→ assert: EnsureMergeRequest called (bead created) AND CreateProcessingCycle called
//
// The test FAILS if pr.opened is not processed before feedback.created: with
// findResult starting nil, the feedback handler returns "no merge-request bead"
// and no cycle is created — caught by the createCycles == 0 assertion.
func TestFullChain_IngestOutboxDispatchBridge(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)

	// PR with one top-level comment (produces a feedback.created event).
	pr := api.PR{
		Repo:    "o/r",
		Number:  55,
		State:   "open",
		Branch:  "feat/chain",
		Base:    "main",
		Author:  "phillipg",
		URL:     "https://github.com/o/r/pull/55",
		HeadSHA: "sha-chain",
	}
	comment := api.Comment{
		ID:     "c-chain",
		Author: "alice",
		Body:   "Looks good overall.",
	}
	enriched := &vcs.EnrichedPR{
		PR:       pr,
		Comments: []api.Comment{comment},
	}

	// Wire the engine with a real store.
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

	// Step 1: emit pr.opened — this MUST be enqueued before feedback.created so
	// the outbox (ORDER BY id) delivers it first. The bridge's EnsureMergeRequest
	// handler sets findResult, enabling the subsequent feedback handler to find
	// the bead.
	if err := e.emitPREvent(ctx, store.EventPROpened, "o/r", pr, "team"); err != nil {
		t.Fatalf("emitPREvent: %v", err)
	}

	// Step 2: ingest feedback — enqueues feedback.created AFTER pr.opened.
	if err := e.ingestFeedbackToStore(ctx, "o/r", pr, enriched); err != nil {
		t.Fatalf("ingestFeedbackToStore: %v", err)
	}

	// Set up the REAL bridge with a fake bead client.
	// findResult starts nil — it will only be populated when EnsureMergeRequest
	// runs (triggered by the pr.opened event). If pr.opened is not processed
	// before feedback.created, FindByRepoAndNumber returns nil and the feedback
	// handler errors "no merge-request bead" → CreateProcessingCycle is never called.
	fakeClient := &fullChainBeadClient{
		findOpenResults: map[string]bool{"mr-chain-1": false}, // no open cycle → must create one
	}
	bridge := beadsbridge.New(fakeClient)

	// Build the REAL dispatcher and register the bridge handler.
	dispatcher := event.New()
	dispatcher.Register(bridge.Handle)

	// Drain the outbox through the real dispatcher → real bridge handler.
	if err := db.RunOutbox(ctx, dispatcher.Dispatch); err != nil {
		t.Fatalf("RunOutbox: %v", err)
	}

	// Assert: pr.opened triggered EnsureMergeRequest (the bead was created).
	if len(fakeClient.ensureCalls) == 0 {
		t.Error("expected EnsureMergeRequest to be called by the bridge (pr.opened path)")
	}
	// Assert: feedback.created found the bead and triggered CreateProcessingCycle.
	if len(fakeClient.findCalls) == 0 {
		t.Error("expected FindByRepoAndNumber to be called by the bridge (feedback.created path)")
	}
	if fakeClient.createCycles == 0 {
		t.Error("expected CreateProcessingCycle to be called — this means pr.opened was processed before feedback.created and the bead existed when the feedback handler ran")
	}
}

// TestOutboxOrdering_PROpenedBeforeFeedback asserts that after emitting pr.opened
// and then ingesting feedback for the same PR, the pr.opened outbox row has a
// strictly smaller id than the feedback.created row. Because RunOutbox drains in
// ORDER BY id (FIFO), this guarantees the bridge always projects the PR bead
// before the feedback handler runs.
//
// The test fails if the emit order is reversed: if feedback.created is enqueued
// before pr.opened, the recorded sequence will show feedback.created first.
func TestOutboxOrdering_PROpenedBeforeFeedback(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)

	pr := api.PR{
		Repo:    "o/r",
		Number:  99,
		State:   "open",
		Branch:  "feat/order",
		Base:    "main",
		Author:  "alice",
		URL:     "https://github.com/o/r/pull/99",
		HeadSHA: "sha-order",
	}
	comment := api.Comment{
		ID:     "c-order",
		Author: "bob",
		Body:   "LGTM",
	}
	enriched := &vcs.EnrichedPR{
		PR:       pr,
		Comments: []api.Comment{comment},
	}

	e, err := New(Deps{
		Cfg: &config.Config{
			SelfLogin: "alice",
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

	// Emit pr.opened first — this is the correct production order.
	if err := e.emitPREvent(ctx, store.EventPROpened, "o/r", pr, "team"); err != nil {
		t.Fatalf("emitPREvent: %v", err)
	}
	// Then ingest feedback — enqueues feedback.created after pr.opened.
	if err := e.ingestFeedbackToStore(ctx, "o/r", pr, enriched); err != nil {
		t.Fatalf("ingestFeedbackToStore: %v", err)
	}

	// Drain the outbox, recording event types in dispatch order (ORDER BY id).
	var sequence []string
	if err := db.RunOutbox(ctx, func(_ context.Context, ev store.Event) error {
		sequence = append(sequence, ev.Type)
		return nil
	}); err != nil {
		t.Fatalf("RunOutbox: %v", err)
	}

	// Find the positions of pr.opened and feedback.created in the dispatch sequence.
	prOpenedIdx := -1
	feedbackIdx := -1
	for i, typ := range sequence {
		if typ == store.EventPROpened && prOpenedIdx == -1 {
			prOpenedIdx = i
		}
		if typ == store.EventFeedbackCreated && feedbackIdx == -1 {
			feedbackIdx = i
		}
	}

	if prOpenedIdx == -1 {
		t.Fatalf("pr.opened event not found in outbox sequence %v", sequence)
	}
	if feedbackIdx == -1 {
		t.Fatalf("feedback.created event not found in outbox sequence %v", sequence)
	}
	if prOpenedIdx >= feedbackIdx {
		t.Errorf("ordering violation: pr.opened at position %d must precede feedback.created at position %d; full sequence: %v",
			prOpenedIdx, feedbackIdx, sequence)
	}
}

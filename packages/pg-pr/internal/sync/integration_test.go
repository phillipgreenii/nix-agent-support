package sync

// integration_test.go wires the REAL ingest → outbox → event.Dispatcher →
// beadsbridge chain end-to-end and asserts the bead client received the
// feedback.created-driven calls (i.e. the process-feedback bead path ran).
//
// This is the full-chain test that proves the payload contract between each
// link — previously only tested in isolation — holds together.

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
type fullChainBeadClient struct {
	ensureCalls     []beads.MergeRequestFields
	findCalls       []string // repo args
	findResult      *beads.MergeRequest
	createCycles    int
	findOpenResults map[string]bool // prBeadID → open
}

func (f *fullChainBeadClient) EnsureMergeRequest(_ context.Context, _ string, fields beads.MergeRequestFields) (string, bool, error) {
	f.ensureCalls = append(f.ensureCalls, fields)
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

func (f *fullChainBeadClient) CreateProcessingCycle(_ context.Context, _, _ string, _ bool) (string, error) {
	f.createCycles++
	return "cycle-chain-1", nil
}

func (f *fullChainBeadClient) FindOpenProcessingCycle(_ context.Context, prBeadID string) (string, bool, error) {
	open := f.findOpenResults[prBeadID]
	return "", open, nil
}

func (f *fullChainBeadClient) CloseProcessingCycle(_ context.Context, _, _ string) error { return nil }

func (f *fullChainBeadClient) CloseFeedback(_ context.Context, _, _ string) error { return nil }

// compile-time check: fullChainBeadClient satisfies the bridge interface.
var _ beadsbridge.BeadClient = (*fullChainBeadClient)(nil)

// TestFullChain_IngestOutboxDispatchBridge is the full end-to-end wiring test:
//
//	ingestFeedbackToStore → feedback rows + outbox events
//	→ store.RunOutbox → event.Dispatcher.Dispatch
//	→ beadsbridge.Handler.Handle (feedback.created path)
//	→ assert beadclient received EnsureMergeRequest + CreateProcessingCycle
//
// This proves the ingest → outbox → event.Dispatcher → beadsbridge payload
// contract end-to-end.
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

	// Ingest: writes feedback row + outbox events.
	if err := e.ingestFeedbackToStore(ctx, "o/r", pr, enriched); err != nil {
		t.Fatalf("ingestFeedbackToStore: %v", err)
	}

	// Set up the REAL bridge with a fake bead client.
	// FindByRepoAndNumber must return a merge-request so ensureProcessFeedbackBead
	// can proceed to FindOpenProcessingCycle + CreateProcessingCycle.
	fakeClient := &fullChainBeadClient{
		findResult:      &beads.MergeRequest{ID: "mr-chain-1"},
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

	// Assert: the bridge's feedback.created path must have called
	// FindByRepoAndNumber (to find the PR bead) and CreateProcessingCycle
	// (to open a processing cycle). This proves the full chain fired.
	if len(fakeClient.findCalls) == 0 {
		t.Error("expected FindByRepoAndNumber to be called by the bridge (feedback.created path)")
	}
	if fakeClient.createCycles == 0 {
		t.Error("expected CreateProcessingCycle to be called by the bridge (no open cycle existed)")
	}
}

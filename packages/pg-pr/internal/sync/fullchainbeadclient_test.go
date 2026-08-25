package sync

// fullChainBeadClient moved out of integration_test.go (untagged, bead
// pg2-h05lt): sync_test.go (unit) uses it too, so it must keep compiling once
// integration_test.go's `//go:build integration` tag is absent (the default
// `go test ./...` build).

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/beadsbridge"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// fullChainBeadClient is a minimal local fake satisfying beadsbridge.BeadClient.
// It records calls so a test can assert the bridge ran the expected path.
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

// compile-time check: fullChainBeadClient satisfies the bridge interface.
var _ beadsbridge.BeadClient = (*fullChainBeadClient)(nil)

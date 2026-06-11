package sync

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// fpCountBeads embeds noopBeads and records how many times PRFeedbackInSubtree
// is called (both processFeedback passes must share a single subtree read per
// refresh), the feedback it creates, and the feedback it resolves. `subtree` is
// the PR's existing feedback (what the single dep-tree read returns).
type fpCountBeads struct {
	noopBeads
	fpCalls   int
	subtree   []beads.Feedback
	created   []beads.CreateFeedbackInput
	closedIDs []string
}

func (f *fpCountBeads) FindOpenProcessingCycle(context.Context, string) (string, bool, error) {
	return "cycle-1", true, nil
}

func (f *fpCountBeads) PRFeedbackInSubtree(context.Context, string) ([]beads.Feedback, error) {
	f.fpCalls++
	return f.subtree, nil
}

func (f *fpCountBeads) CreateFeedback(_ context.Context, in beads.CreateFeedbackInput) (string, error) {
	f.created = append(f.created, in)
	return "fb-new", nil
}

func (f *fpCountBeads) MarkFeedbackResolvedUpstream(_ context.Context, id string) error {
	f.closedIDs = append(f.closedIDs, id)
	return nil
}

// TestProcessFeedback_DedupIsHoistedOutOfEventLoop verifies the cache-less
// (daemon) dedup path builds the PR's existing-feedback fingerprint set ONCE
// per refresh instead of re-listing the PR's cycles per event. The previous
// per-event findFeedbackForPR call made dedup O(events x cycles) bd calls,
// which collapsed daemon throughput on heavily-commented PRs. Behavior
// (which events are net-new vs deduped) is preserved.
func TestProcessFeedback_DedupIsHoistedOutOfEventLoop(t *testing.T) {
	dup := api.Comment{ID: "c1", Author: "bot", Body: "duplicate comment"}
	fresh1 := api.Comment{ID: "c2", Author: "bot", Body: "fresh one"}
	fresh2 := api.Comment{ID: "c3", Author: "bot", Body: "fresh two"}
	fresh3 := api.Comment{ID: "c4", Author: "bot", Body: "fresh three"}

	// The dup comment's fingerprint already exists under the PR's cycle.
	dupFP := commentEvent(dup).fingerprint
	bdc := &fpCountBeads{
		subtree: []beads.Feedback{
			{ID: "fb-dup", Status: "hooked", Fields: beads.FeedbackFields{Fingerprint: dupFP}},
		},
	}

	e := newRefreshEngine(t, "me", &refreshFakeBeads{}, api.PR{Repo: "o/r", Number: 1, Author: "me", State: "open"})
	enriched := &vcs.EnrichedPR{Comments: []api.Comment{dup, fresh1, fresh2, fresh3}}

	summary := &Summary{}
	if err := e.processFeedback(context.Background(), bdc, nil /* cache */, enriched, "o/r",
		api.PR{Repo: "o/r", Number: 1}, "pr-bead-1", summary); err != nil {
		t.Fatalf("processFeedback: %v", err)
	}

	// The PR's existing feedback is read exactly once per refresh, not per event.
	if bdc.fpCalls != 1 {
		t.Fatalf("PRFeedbackInSubtree should be called once per refresh, got %d", bdc.fpCalls)
	}

	// Dedup correctness preserved: the 3 fresh comments are created; the dup is not.
	if len(bdc.created) != 3 {
		t.Fatalf("expected 3 net-new feedback beads created (dup skipped), got %d", len(bdc.created))
	}
	for _, in := range bdc.created {
		if in.Fingerprint == dupFP {
			t.Fatalf("duplicate event (fingerprint %s) must not be recreated", dupFP)
		}
	}
}

// TestProcessFeedback_UnifiedFirstAndSecondPass verifies the cache-less path
// serves BOTH passes from a single PRFeedbackInSubtree read: the first pass
// (CI-success resolver) closes a matching open ci-failure feedback drawn from
// the subtree, and the second pass dedups a duplicate comment against the same
// slice — with exactly one subtree read and no duplicate creation.
func TestProcessFeedback_UnifiedFirstAndSecondPass(t *testing.T) {
	dupComment := api.Comment{ID: "c1", Author: "bot", Body: "duplicate comment"}
	dupFP := commentEvent(dupComment).fingerprint

	// A CI-success run whose ID matches an OPEN ci-failure feedback's
	// external_id. ciRunEvent carries r.ID (NOT the run name) as externalID,
	// so the run's ID must equal the seeded feedback's ExternalID.
	successRun := api.CIRun{ID: "run-x", Name: "build", Conclusion: "success", Provider: "gha"}

	bdc := &fpCountBeads{
		subtree: []beads.Feedback{
			// Open ci-failure feedback the success run should resolve (first pass).
			{ID: "fb-ci", Status: "hooked", Fields: beads.FeedbackFields{
				Kind: string(beads.FeedbackKindCIFailure), ExternalID: "run-x",
			}},
			// Existing feedback with the dup comment's fingerprint (second-pass dedup).
			{ID: "fb-dup", Status: "hooked", Fields: beads.FeedbackFields{Fingerprint: dupFP}},
		},
	}

	e := newRefreshEngine(t, "me", &refreshFakeBeads{}, api.PR{Repo: "o/r", Number: 1, Author: "me", State: "open"})
	enriched := &vcs.EnrichedPR{
		Comments: []api.Comment{dupComment},
		CIRuns:   []api.CIRun{successRun},
	}

	summary := &Summary{}
	if err := e.processFeedback(context.Background(), bdc, nil /* cache */, enriched, "o/r",
		api.PR{Repo: "o/r", Number: 1}, "pr-bead-1", summary); err != nil {
		t.Fatalf("processFeedback: %v", err)
	}

	// Single subtree read serves both passes.
	if bdc.fpCalls != 1 {
		t.Fatalf("PRFeedbackInSubtree should be called once, got %d", bdc.fpCalls)
	}
	// First pass: the open ci-failure feedback is resolved by the success run.
	if len(bdc.closedIDs) != 1 || bdc.closedIDs[0] != "fb-ci" {
		t.Fatalf("expected fb-ci closed by CI-success resolver, got %v", bdc.closedIDs)
	}
	// Second pass: the duplicate comment is NOT recreated (it dedups against the
	// same subtree slice); the CI-success event is skipped by the create loop.
	if len(bdc.created) != 0 {
		t.Fatalf("expected no feedback created (dup deduped, CI-success skipped), got %d: %+v", len(bdc.created), bdc.created)
	}
}

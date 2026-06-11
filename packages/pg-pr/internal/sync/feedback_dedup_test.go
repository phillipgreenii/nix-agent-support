package sync

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// fpCountBeads embeds noopBeads and records how many times ListChildrenOfPR is
// called, plus the feedback it creates. It models one open processing-cycle
// ("cycle-1") whose existing feedback is fed in via `feedback`.
type fpCountBeads struct {
	noopBeads
	childrenCalls int
	feedback      []beads.Feedback
	created       []beads.CreateFeedbackInput
}

func (f *fpCountBeads) FindOpenProcessingCycle(context.Context, string) (string, bool, error) {
	return "cycle-1", true, nil
}

func (f *fpCountBeads) ListChildrenOfPR(context.Context, string) ([]string, error) {
	f.childrenCalls++
	return []string{"cycle-1"}, nil
}

func (f *fpCountBeads) ListFeedback(_ context.Context, cycleID string, _ bool) ([]beads.Feedback, error) {
	if cycleID == "cycle-1" {
		return f.feedback, nil
	}
	return nil, nil
}

func (f *fpCountBeads) CreateFeedback(_ context.Context, in beads.CreateFeedbackInput) (string, error) {
	f.created = append(f.created, in)
	return "fb-new", nil
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
		feedback: []beads.Feedback{{ID: "fb1", Fields: beads.FeedbackFields{Fingerprint: dupFP}}},
	}

	e := newRefreshEngine(t, "me", &refreshFakeBeads{}, api.PR{Repo: "o/r", Number: 1, Author: "me", State: "open"})
	enriched := &vcs.EnrichedPR{Comments: []api.Comment{dup, fresh1, fresh2, fresh3}}

	summary := &Summary{}
	if err := e.processFeedback(context.Background(), bdc, nil /* cache */, enriched, "o/r",
		api.PR{Repo: "o/r", Number: 1}, "pr-bead-1", summary); err != nil {
		t.Fatalf("processFeedback: %v", err)
	}

	// The hoist: the PR's cycles are listed exactly once, not once per event.
	if bdc.childrenCalls != 1 {
		t.Fatalf("ListChildrenOfPR should be called once (dedup hoisted out of the event loop), got %d", bdc.childrenCalls)
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

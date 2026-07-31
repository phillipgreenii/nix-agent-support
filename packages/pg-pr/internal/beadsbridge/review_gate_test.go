package beadsbridge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// TestWithoutDraftReviews_SuppressesProductionKeepsMergeRequest is the NH3 kill
// switch at the beadsbridge layer: with WithoutDraftReviews, a pr.updated event
// still ensures the merge-request bead (dashboard depends on it) but does NOT
// produce a draft-review bead. The gate is localized to the draft-review call;
// the attention and process-feedback branches are unaffected by construction.
func TestWithoutDraftReviews_SuppressesProductionKeepsMergeRequest(t *testing.T) {
	c := &draftReviewClient{}
	h := New(c, WithoutDraftReviews())

	// A ready mine PR — the case that WOULD produce a draft-review by default.
	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Ownership: "mine", Draft: false})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPRUpdated, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if c.mrCalls != 1 {
		t.Errorf("merge-request production must be preserved when reviews are off, got %d ensures", c.mrCalls)
	}
	if c.drCalls != 0 {
		t.Errorf("WithoutDraftReviews must suppress draft-review production, got %d", c.drCalls)
	}
}

// TestDefaultStillProducesDraftReview: without the option, today's behavior is
// preserved (a ready PR produces a draft-review bead).
func TestDefaultStillProducesDraftReview(t *testing.T) {
	c := &draftReviewClient{}
	h := New(c)

	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Ownership: "mine", Draft: false})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPRUpdated, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if c.drCalls != 1 {
		t.Errorf("default (no option) must still produce a draft-review bead, got %d", c.drCalls)
	}
	if c.mrCalls != 1 {
		t.Errorf("merge-request must be produced, got %d", c.mrCalls)
	}
}

// feedbackCycleClient models an open PR bead with no open processing cycle, and
// counts CreateProcessingCycle calls.
type feedbackCycleClient struct {
	noopBeadClient
	created int
}

func (c *feedbackCycleClient) FindByRepoAndNumber(context.Context, string, int) (*beads.MergeRequest, error) {
	return &beads.MergeRequest{ID: "mr-1", Status: "open"}, nil
}

func (c *feedbackCycleClient) ResolveProcessingCycle(context.Context, string, string) (beads.ProcessingCycleState, error) {
	return beads.ProcessingCycleState{}, nil
}

func (c *feedbackCycleClient) CreateProcessingCycle(context.Context, beads.CreateProcessingCycleInput) (string, error) {
	c.created++
	return "pf-1", nil
}

// TestWithoutDraftReviews_StillProducesProcessFeedback confirms the kill switch
// is narrow: with reviews off, a feedback.created event still produces the
// process-feedback cycle (it flows through an independent switch branch that
// never consults suppressDraftReviews). Attention is structurally identical.
func TestWithoutDraftReviews_StillProducesProcessFeedback(t *testing.T) {
	c := &feedbackCycleClient{}
	h := New(c, WithoutDraftReviews())

	payload, _ := json.Marshal(FeedbackPayload{Repo: "o/r", Number: 7, Mine: false})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventFeedbackCreated, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if c.created != 1 {
		t.Errorf("process-feedback must still be produced when reviews are off, got %d cycles", c.created)
	}
}

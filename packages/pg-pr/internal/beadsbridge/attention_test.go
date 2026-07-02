package beadsbridge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// attentionClient records ensure/close attention calls on top of a resolvable MR.
type attentionClient struct {
	noopBeadClient
	mr           *beads.MergeRequest
	ensureCalls  int
	closeCalls   int
	lastEnsureMR string
	lastCloseMR  string
}

func (c *attentionClient) FindByRepoAndNumber(context.Context, string, int) (*beads.MergeRequest, error) {
	return c.mr, nil
}
func (c *attentionClient) EnsureAttentionBead(_ context.Context, prBeadID, _ string) (string, error) {
	c.ensureCalls++
	c.lastEnsureMR = prBeadID
	return "att-1", nil
}
func (c *attentionClient) CloseAttentionBead(_ context.Context, prBeadID, _ string) error {
	c.closeCalls++
	c.lastCloseMR = prBeadID
	return nil
}

func attentionPayload(t *testing.T, need bool, reason string) []byte {
	t.Helper()
	b, err := json.Marshal(store.AttentionPayload{Repo: "o/r", Number: 7, Need: need, Reason: reason})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestAttentionNeedEnsuresBead(t *testing.T) {
	c := &attentionClient{mr: &beads.MergeRequest{ID: "mr-1", Status: "open"}}
	h := New(c)
	err := h.Handle(context.Background(), store.Event{
		Type: store.EventPRAttention, Payload: attentionPayload(t, true, "draft-review-ready-unapproved"),
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if c.ensureCalls != 1 || c.closeCalls != 0 {
		t.Fatalf("ensure=%d close=%d, want ensure=1 close=0", c.ensureCalls, c.closeCalls)
	}
	if c.lastEnsureMR != "mr-1" {
		t.Fatalf("ensure parent = %q, want mr-1", c.lastEnsureMR)
	}
}

func TestAttentionNoNeedClosesBead(t *testing.T) {
	c := &attentionClient{mr: &beads.MergeRequest{ID: "mr-1", Status: "open"}}
	h := New(c)
	err := h.Handle(context.Background(), store.Event{
		Type: store.EventPRAttention, Payload: attentionPayload(t, false, ""),
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if c.ensureCalls != 0 || c.closeCalls != 1 {
		t.Fatalf("ensure=%d close=%d, want ensure=0 close=1", c.ensureCalls, c.closeCalls)
	}
	if c.lastCloseMR != "mr-1" {
		t.Fatalf("close parent = %q, want mr-1", c.lastCloseMR)
	}
}

// Idempotent under re-dispatch (fire-once resilience): re-delivering the same
// need:true event must not double-open (the wrapper dedups; here we just prove
// the handler re-runs the ensure each time it is dispatched — self-healing).
func TestAttentionRedispatchReEnsures(t *testing.T) {
	c := &attentionClient{mr: &beads.MergeRequest{ID: "mr-1", Status: "open"}}
	h := New(c)
	for i := 0; i < 3; i++ {
		if err := h.Handle(context.Background(), store.Event{
			Type: store.EventPRAttention, Payload: attentionPayload(t, true, "draft-review-ready-unapproved"),
		}); err != nil {
			t.Fatalf("Handle #%d: %v", i, err)
		}
	}
	if c.ensureCalls != 3 {
		t.Fatalf("ensure re-run each tick: got %d, want 3 (idempotent ensure delegated to wrapper)", c.ensureCalls)
	}
}

// A closed merge-request parent must NOT get a new attention child (matches the
// draft-review / process-feedback closed-parent guard).
func TestAttentionSkippedWhenParentClosed(t *testing.T) {
	c := &attentionClient{mr: &beads.MergeRequest{ID: "mr-1", Status: "closed"}}
	h := New(c)
	if err := h.Handle(context.Background(), store.Event{
		Type: store.EventPRAttention, Payload: attentionPayload(t, true, "draft-review-ready-unapproved"),
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if c.ensureCalls != 0 {
		t.Fatalf("must not ensure under a closed parent, got %d", c.ensureCalls)
	}
}

// A missing merge-request bead surfaces an error (the bead should exist; the
// projector ran after the PR bead was projected).
func TestAttentionErrorsWhenNoMergeRequest(t *testing.T) {
	c := &attentionClient{mr: nil}
	h := New(c)
	if err := h.Handle(context.Background(), store.Event{
		Type: store.EventPRAttention, Payload: attentionPayload(t, true, "r"),
	}); err == nil {
		t.Fatal("expected an error when no merge-request bead resolves")
	}
}

// Cascade (M6): the attention bead is a CHILD of the merge-request bead, so a PR
// close/merge closes it via cascadeClose (which closes every child id via
// CloseProcessingCycle — title-agnostic close-by-id). ListChildrenOfPR returns
// the attention child; it must be among the closed children.
func TestAttentionBeadCascadeClosesOnMerge(t *testing.T) {
	closedChildren := map[string]bool{}
	client := &cascadeClient{
		find:         &beads.MergeRequest{ID: "mr-1", Status: "open"},
		children:     []string{"att-1", "dr-1"}, // attention bead + draft-review bead
		onCloseMR:    func(string) {},
		onCloseChild: func() {},
	}
	// Wrap to record which child ids were closed.
	rec := &recordingCascadeClient{cascadeClient: client, closed: closedChildren}
	h := New(rec)
	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Merged: true})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPRMerged, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !closedChildren["att-1"] {
		t.Fatalf("attention child att-1 was NOT cascade-closed; closed=%v", closedChildren)
	}
}

// recordingCascadeClient records the child ids passed to CloseProcessingCycle.
type recordingCascadeClient struct {
	*cascadeClient
	closed map[string]bool
}

func (c *recordingCascadeClient) CloseProcessingCycle(_ context.Context, id, _ string) error {
	c.closed[id] = true
	return nil
}

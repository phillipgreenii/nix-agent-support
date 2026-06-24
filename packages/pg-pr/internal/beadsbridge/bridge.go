// Package beadsbridge is the event handler that projects pg-pr's PR + process-
// feedback beads. It relocates the bead-orchestration that used to live inline
// in internal/sync. It creates the PR (merge-request) bead and the process-
// feedback bead, and cascade-closes on PR close. It does NOT create feedback
// beads — feedback now lives in internal/store.
package beadsbridge

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// BeadClient is the subset of *beads.Client the bridge needs (narrow for tests).
type BeadClient interface {
	EnsureMergeRequest(ctx context.Context, title string, fields beads.MergeRequestFields) (string, bool, error)
	FindByRepoAndNumber(ctx context.Context, repo string, number int) (*beads.MergeRequest, error)
	CloseMergeRequest(ctx context.Context, id, reason string) error
	ListChildrenOfPR(ctx context.Context, prBeadID string) ([]string, error)
	CreateProcessingCycle(ctx context.Context, prBeadID, title string, mine bool) (string, error)
	FindOpenProcessingCycle(ctx context.Context, prBeadID string) (string, bool, error)
	CloseProcessingCycle(ctx context.Context, id, reason string) error
	CloseFeedback(ctx context.Context, id, reason string) error
}

// Handler is the beads event handler.
type Handler struct{ client BeadClient }

// New constructs the handler.
func New(client BeadClient) *Handler { return &Handler{client: client} }

// FeedbackPayload is the JSON payload for feedback.created events.
type FeedbackPayload struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Mine   bool   `json:"mine"`
}

// Handle implements event.Handler. Idempotent: re-dispatch under the
// at-least-once outbox must not duplicate beads.
func (h *Handler) Handle(ctx context.Context, e store.Event) error {
	switch e.Type {
	case store.EventPROpened, store.EventPRUpdated:
		var p store.PRPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("beadsbridge: decode pr payload: %w", err)
		}
		_, _, err := h.client.EnsureMergeRequest(ctx, p.Title, beads.MergeRequestFields{
			Repo: p.Repo, PRNumber: p.Number, State: p.State, Branch: p.Branch,
			Base: p.Base, Author: p.Author, URL: p.URL, Draft: p.Draft,
			LastSyncedAt: p.LastSyncedAt,
		})
		return err
	case store.EventFeedbackCreated:
		var p FeedbackPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("beadsbridge: decode feedback payload: %w", err)
		}
		return h.ensureProcessFeedbackBead(ctx, p)
	case store.EventPRClosed, store.EventPRMerged:
		var p store.PRPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("beadsbridge: decode pr payload: %w", err)
		}
		return h.cascadeClose(ctx, p)
	}
	return nil
}

// ensureProcessFeedbackBead upserts the open process-feedback bead for a PR.
// FindOpenProcessingCycle's error MUST propagate (swallowing it as "none open"
// is the documented duplicate-cycle bug).
func (h *Handler) ensureProcessFeedbackBead(ctx context.Context, p FeedbackPayload) error {
	mr, err := h.client.FindByRepoAndNumber(ctx, p.Repo, p.Number)
	if err != nil {
		return err
	}
	if mr == nil {
		return fmt.Errorf("beadsbridge: no merge-request bead for %s#%d", p.Repo, p.Number)
	}
	if mr.Status == "closed" {
		return nil // do not attach a live cycle under a closed PR bead
	}
	_, open, err := h.client.FindOpenProcessingCycle(ctx, mr.ID)
	if err != nil {
		return err // propagate — do NOT treat as "no open cycle"
	}
	if open {
		return nil
	}
	_, err = h.client.CreateProcessingCycle(ctx, mr.ID, fmt.Sprintf("%s#%d", p.Repo, p.Number), p.Mine)
	return err
}

// cascadeClose closes the PR bead and its descendants.
func (h *Handler) cascadeClose(ctx context.Context, p store.PRPayload) error {
	mr, err := h.client.FindByRepoAndNumber(ctx, p.Repo, p.Number)
	if err != nil || mr == nil {
		return err
	}
	reason := "pr-closed"
	if p.Merged {
		reason = "upstream-merged"
	}
	children, err := h.client.ListChildrenOfPR(ctx, mr.ID)
	if err != nil {
		return err
	}
	for _, child := range children {
		_ = h.client.CloseProcessingCycle(ctx, child, reason)
	}
	return h.client.CloseMergeRequest(ctx, mr.ID, reason)
}

// compile-time check: *beads.Client must satisfy BeadClient.
var _ BeadClient = (*beads.Client)(nil)

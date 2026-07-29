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
	"strconv"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// BeadClient is the subset of *beads.Client the bridge needs (narrow for tests).
type BeadClient interface {
	EnsureMergeRequest(ctx context.Context, title string, fields beads.MergeRequestFields) (string, bool, error)
	SetMergeRequestCoOwned(ctx context.Context, id string, coOwned bool) error
	FindByRepoAndNumber(ctx context.Context, repo string, number int) (*beads.MergeRequest, error)
	CloseMergeRequest(ctx context.Context, id, reason string) error
	ListChildrenOfPR(ctx context.Context, prBeadID string) ([]string, error)
	CreateProcessingCycle(ctx context.Context, prBeadID, title string, mine bool) (string, error)
	FindOpenProcessingCycle(ctx context.Context, prBeadID string) (string, bool, error)
	CloseProcessingCycle(ctx context.Context, id, reason string) error
	CloseFeedback(ctx context.Context, id, reason string) error
	EnsureDraftReviewBead(ctx context.Context, prBeadID, title string, mine bool) (string, error)
	EnsureDraftReviewMineLabel(ctx context.Context, prBeadID string) error
	EnsureAttentionBead(ctx context.Context, prBeadID, title string) (string, error)
	CloseAttentionBead(ctx context.Context, prBeadID, reason string) error
	GetMergeRequest(ctx context.Context, id string) (*beads.MergeRequest, error)
	SetPriority(ctx context.Context, id string, p int) error
	AddLabel(ctx context.Context, id, label string) error
	RemoveLabel(ctx context.Context, id, label string) error
}

// Handler is the beads event handler.
type Handler struct {
	client BeadClient
	// suppressDraftReviews, when true, makes the pr.opened/pr.updated projection
	// skip EnsureDraftReviewBead (the NH3 kill switch, bead pg2-ynhr.11). All
	// other production (merge-request, attention, process-feedback) is
	// unaffected.
	suppressDraftReviews bool
}

// Option customizes a Handler.
type Option func(*Handler)

// WithoutDraftReviews disables draft-review bead PRODUCTION on pr.opened/updated
// (the daemon's review kill switch, bead pg2-ynhr.11). Merge-request, attention,
// and process-feedback beads are still produced.
func WithoutDraftReviews() Option {
	return func(h *Handler) { h.suppressDraftReviews = true }
}

// New constructs the handler, applying any options.
func New(client BeadClient, opts ...Option) *Handler {
	h := &Handler{client: client}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

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
		mrID, alreadyClosed, err := h.client.EnsureMergeRequest(ctx, p.Title, beads.MergeRequestFields{
			Repo: p.Repo, PRNumber: p.Number, State: p.State, Branch: p.Branch,
			Base: p.Base, Author: p.Author, URL: p.URL, Draft: p.Draft,
			LastSyncedAt: p.LastSyncedAt,
		})
		if err != nil {
			return err
		}
		if alreadyClosed {
			return nil // closed PR bead: do not attach a draft-review under it
		}
		// Keep the co-owned visibility label in sync with the current
		// ownership verdict — added when co-owned, removed otherwise.
		if err := h.client.SetMergeRequestCoOwned(ctx, mrID, p.Ownership == "co-owned"); err != nil {
			return err
		}
		if err := h.reconcilePriority(ctx, mrID, p.Ownership, p.HasConflict); err != nil {
			return err
		}
		// Emit the review work item. My PRs and co-owned PRs are reviewed even
		// while a GitHub draft; team PRs wait until the draft flag is removed
		// (which fires on the pr.updated that flips it). EnsureDraftReviewBead
		// is idempotent. When the review kill switch is on
		// (suppressDraftReviews), production is skipped entirely — the
		// merge-request bead above is still ensured.
		//
		// The acts-as-mine test goes through the SHARED predicate
		// ownership.ActsAsMine (mine OR co-owned), the same one replyposter,
		// snapshot.builder, sync.ingest and nudged below use — never a local
		// `!= "team"`. Over the closed 3-value set the two agree; they diverge on
		// an out-of-band/empty value, where ActsAsMine degrades to team-style
		// selection (a draft is skipped, not auto-reviewed) — the conservative
		// direction, matching pr-pool's copy of the predicate. (pg2-q2drf)
		mine := ownership.Ownership(p.Ownership).ActsAsMine()
		if !h.suppressDraftReviews && (mine || !p.Draft) {
			drID, err := h.client.EnsureDraftReviewBead(ctx, mrID, fmt.Sprintf("%s#%d", p.Repo, p.Number), mine)
			if err != nil {
				return err
			}
			// team->co-owned: an earlier team-style review bead must flip to mine.
			if p.Ownership == "co-owned" {
				if err := h.client.EnsureDraftReviewMineLabel(ctx, mrID); err != nil {
					return err
				}
			}
			_ = drID
			return nil
		}
		return nil
	case store.EventFeedbackCreated:
		var p FeedbackPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("beadsbridge: decode feedback payload: %w", err)
		}
		return h.ensureProcessFeedbackBead(ctx, p)
	case store.EventPRAttention:
		var p store.AttentionPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("beadsbridge: decode attention payload: %w", err)
		}
		return h.projectAttentionBead(ctx, p)
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

// projectAttentionBead ensures or closes the teammate-attention bead for a PR
// from the shared needsAttention verdict carried in the event. Re-run every tick
// (the projector re-emits from persisted facts), so both ensure and close are
// idempotent and a dropped fire-once event self-heals on the next tick (R1).
// Mirrors ensureProcessFeedbackBead: the FindByRepoAndNumber error PROPAGATES,
// and a closed parent suppresses opening a new child.
func (h *Handler) projectAttentionBead(ctx context.Context, p store.AttentionPayload) error {
	mr, err := h.client.FindByRepoAndNumber(ctx, p.Repo, p.Number)
	if err != nil {
		return err
	}
	if mr == nil {
		return fmt.Errorf("beadsbridge: no merge-request bead for %s#%d", p.Repo, p.Number)
	}
	if mr.Status == "closed" {
		return nil // do not attach/reopen an attention child under a closed PR bead
	}
	if p.Need {
		_, err := h.client.EnsureAttentionBead(ctx, mr.ID, fmt.Sprintf("%s#%d", p.Repo, p.Number))
		return err
	}
	return h.client.CloseAttentionBead(ctx, mr.ID, "attention-cleared")
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

const pbaseLabelPrefix = "pbase:"

// reconcilePriority nudges the merge-request bead's priority on conflict and
// reverts it when the conflict clears, statelessly. The pre-adjustment priority
// is stashed in a `pbase:<n>` label so a repeated conflicting tick is a no-op
// and a clear restores the exact baseline. mine/co-owned raise (−1, clamp 0);
// team lowers (+1, clamp 4). (pg2-tsgkj)
func (h *Handler) reconcilePriority(ctx context.Context, mrID, ownershipStr string, hasConflict bool) error {
	mr, err := h.client.GetMergeRequest(ctx, mrID)
	if err != nil {
		return err
	}
	if mr == nil {
		return nil
	}
	baseline, hasBaseline := parsePbase(mr.Labels)

	switch {
	case hasConflict && !hasBaseline:
		// First conflicting tick: stash current priority, then nudge.
		desired := nudged(mr.Priority, ownershipStr)
		// Stash unconditionally — even when the priority is already clamped at the
		// boundary (desired == mr.Priority) — so a later clear is a no-op-safe restore.
		if err := h.client.AddLabel(ctx, mrID, pbaseLabelPrefix+strconv.Itoa(mr.Priority)); err != nil {
			return err
		}
		if desired != mr.Priority {
			return h.client.SetPriority(ctx, mrID, desired)
		}
		return nil
	case hasConflict && hasBaseline:
		return nil // already adjusted this conflict episode — idempotent no-op
	case !hasConflict && hasBaseline:
		// Conflict cleared: restore baseline, drop the marker.
		if mr.Priority != baseline {
			if err := h.client.SetPriority(ctx, mrID, baseline); err != nil {
				return err
			}
		}
		return h.client.RemoveLabel(ctx, mrID, pbaseLabelPrefix+strconv.Itoa(baseline))
	default:
		return nil // no conflict, no baseline — nothing to do
	}
}

// nudged returns the conflict-adjusted priority: mine/co-owned raise (toward 0),
// team lower (toward 4). Clamped to [0,4].
func nudged(p int, ownershipStr string) int {
	if ownership.Ownership(ownershipStr).ActsAsMine() {
		if p > 0 {
			return p - 1
		}
		return 0
	}
	if p < 4 {
		return p + 1
	}
	return 4
}

// parsePbase extracts the stashed baseline priority from a `pbase:<n>` label.
func parsePbase(labels []string) (int, bool) {
	for _, l := range labels {
		if strings.HasPrefix(l, pbaseLabelPrefix) {
			if n, err := strconv.Atoi(strings.TrimPrefix(l, pbaseLabelPrefix)); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

// compile-time check: *beads.Client must satisfy BeadClient.
var _ BeadClient = (*beads.Client)(nil)

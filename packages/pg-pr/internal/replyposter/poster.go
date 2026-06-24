// Package replyposter reconciles queued replies from the feedback store to GitHub.
// It is designed to run periodically (e.g. on a timer or explicit reconcile call).
// Delivery is durable: the store is re-scanned each reconcile, and idempotency is
// enforced via response_id — a row with a non-empty response_id is skipped.
package replyposter

import (
	"context"
	"log/slog"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/marker"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// Replier is the subset of github.Provider used to post comments/replies.
// *github.Provider satisfies this interface.
type Replier interface {
	// ReplyToThread posts a reply on an existing review thread. threadID is the
	// GitHub review-thread node id (PRRT_…).
	ReplyToThread(ctx context.Context, repo, threadID, body string) (*api.Comment, error)
	// AddComment posts a top-level PR comment.
	AddComment(ctx context.Context, repo string, number int, body string) (*api.Comment, error)
}

// Poster reconciles pending replies from the store to GitHub.
type Poster struct {
	db      *store.DB
	replier Replier
	log     *slog.Logger
}

// New creates a Poster using the default slog logger.
func New(db *store.DB, replier Replier) *Poster {
	return &Poster{
		db:      db,
		replier: replier,
		log:     slog.Default(),
	}
}

// Reconcile scans for pending replies and posts each one to GitHub.
// Best-effort: a post error is logged and the loop continues (the row stays
// pending and will be retried on the next reconcile).
// Returns an error only if the store scan itself fails.
func (p *Poster) Reconcile(ctx context.Context) error {
	pending, err := p.db.ListPendingReplies(ctx)
	if err != nil {
		return err
	}

	for _, fb := range pending {
		if fb.ManagedUpstream {
			p.log.DebugContext(ctx, "replyposter: skip managed_upstream", "feedback_id", fb.ID)
			continue
		}

		pr, err := p.db.GetPRByID(ctx, fb.PRID)
		if err != nil {
			p.log.ErrorContext(ctx, "replyposter: get pr by id", "pr_id", fb.PRID, "feedback_id", fb.ID, "err", err)
			continue
		}
		if pr == nil {
			p.log.WarnContext(ctx, "replyposter: pr not found, skipping", "pr_id", fb.PRID, "feedback_id", fb.ID)
			continue
		}
		// Only auto-reply on PRs we own. Team-owned PRs are monitored but
		// replies are not auto-posted (M2: ownership gate).
		if pr.Ownership != "mine" {
			p.log.DebugContext(ctx, "replyposter: skip team-owned pr", "pr_id", fb.PRID, "feedback_id", fb.ID, "ownership", pr.Ownership)
			continue
		}

		body := marker.Stamp(fb.ReplyBody)

		var resp *api.Comment
		if fb.Kind == "code-comment-thread" {
			resp, err = p.replier.ReplyToThread(ctx, pr.Repo, fb.ExternalID, body)
		} else {
			resp, err = p.replier.AddComment(ctx, pr.Repo, pr.Number, body)
		}
		if err != nil {
			p.log.ErrorContext(ctx, "replyposter: post failed, will retry", "feedback_id", fb.ID, "kind", fb.Kind, "err", err)
			continue
		}

		if markErr := p.db.MarkReplied(ctx, fb.ID, resp.ID); markErr != nil {
			p.log.ErrorContext(ctx, "replyposter: mark replied", "feedback_id", fb.ID, "err", markErr)
		}
	}

	return nil
}

package conversation

import (
	"context"
	"errors"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/metered"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// PointsPerTarget is the GraphQL points one target's fetch is estimated to
// cost: three single-PR page calls (threads, issue comments, reviews) at one
// point each, as the Phase 1 spike measured for today's `pr show` shape. A PR
// with more than a page of anything costs more, and the metered runner books
// what each page really cost.
const PointsPerTarget = 3

// ConversationReader is the two Provider reads the group is made of. A
// *github.Provider built over the metered runner (metered.NewProvider)
// satisfies it, so every page of both reads is admitted and booked by the
// governor under the call context Fetch states.
type ConversationReader interface {
	ListCommentsReport(ctx context.Context, repo string, number int) (*api.CommentsResult, error)
	ListReviewsReport(ctx context.Context, repo string, number int) (*api.ReviewsResult, error)
}

// Fetcher fetches the conversation group; it implements groups.Fetcher.
type Fetcher struct {
	// Reader runs the two reads; it MUST be metered (metered.NewProvider) so the
	// governor sees every page.
	Reader ConversationReader
}

var _ groups.Fetcher = (*Fetcher)(nil)

// Group implements groups.Fetcher.
func (f *Fetcher) Group() string { return groups.Conversation }

// MaxBatch implements groups.Fetcher: the group is fetched one PR per call.
func (f *Fetcher) MaxBatch() int { return 1 }

// Spend implements groups.Fetcher: PointsPerTarget GraphQL points per target.
func (f *Fetcher) Spend(class governor.Class, n int) governor.Request {
	return governor.Request{Bucket: governor.BucketGraphQL, Class: class, Cost: int64(PointsPerTarget * max(n, 1))}
}

// Fetch implements groups.Fetcher. It fetches the targets in order, one PR
// each, and returns one Outcome per target. Every Outcome carries the stamps its
// target had before the reads (if the head moved during them the row is stale at
// once against the newer summary). Outcome.Cost is left zero: the metered runner
// books each page's real spend as it is made.
//
// A read that fails fails its target with that error and no content: a PR GitHub
// no longer finds is groups.ErrGone; a *governor.Refusal comes back untouched,
// and the targets after it carry it too, without a read, as does everything left
// once the context is done.
func (f *Fetcher) Fetch(ctx context.Context, call ratefeed.Meta, targets []groups.Target) []groups.Outcome {
	out := make([]groups.Outcome, len(targets))
	call.Group, call.BatchSize = groups.Conversation, 1
	ctx = metered.WithCall(ctx, call)
	var stop error
	for i, t := range targets {
		out[i].Stamps = t.Stamps
		if stop == nil {
			stop = ctx.Err()
		}
		if stop != nil {
			out[i].Err = stop
			continue
		}
		out[i].Content, out[i].Fingerprint, out[i].Err = f.fetchOne(ctx, t)
		if errors.Is(out[i].Err, governor.ErrRefused) {
			stop = out[i].Err
		}
	}
	return out
}

// fetchOne reads one PR's conversation: its comments (threads, then issue
// comments), then its reviews. Either failing is the target's error.
func (f *Fetcher) fetchOne(ctx context.Context, t groups.Target) (content []byte, fingerprint string, err error) {
	repo, number := t.Ref.NameWithOwner(), t.Ref.Number
	comments, err := f.Reader.ListCommentsReport(ctx, repo, number)
	if err != nil {
		return nil, "", classify(err)
	}
	reviews, err := f.Reader.ListReviewsReport(ctx, repo, number)
	if err != nil {
		return nil, "", classify(err)
	}
	c := contentOf(comments, reviews)
	content, err = c.canonical()
	if err != nil {
		return nil, "", fmt.Errorf("conversation: encode %s: %w", t.NodeID, err)
	}
	fingerprint, err = Fingerprint(c)
	if err != nil {
		return nil, "", fmt.Errorf("conversation: fingerprint %s: %w", t.NodeID, err)
	}
	return content, fingerprint, nil
}

// classify maps a read error onto the fetcher's: a governor refusal is returned
// untouched, a PR that does not exist is groups.ErrGone, anything else is the
// error as it came.
func classify(err error) error {
	switch {
	case errors.Is(err, governor.ErrRefused):
		return err
	case metered.IsNotFound(err):
		return groups.ErrGone
	}
	return err
}

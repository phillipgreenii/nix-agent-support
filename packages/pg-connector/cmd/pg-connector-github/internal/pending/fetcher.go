// Package pending implements the pending entity group: the acting identity's
// PENDING review of a PR. The group is read PER PR (one provider read per
// target), is fresh until pending_ttl or until a review_submit through the
// daemon invalidates it, and is NEVER refreshed in the background (the groups
// package's Rules.Background("pending") is false).
//
//   - Fetcher is the groups.Fetcher; its stored content is the JSON of
//     github.PendingReviewData (Decode reads it back).
//   - Compute builds the review_pending op's result from the stored data with
//     the old backend's rules (cmd/pg-connector-pr-github/internal/review_pending.go).
//   - SidecarView and RegisterFlushes make the group's identity-scoped data
//     flush when the viewer changes.
package pending

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/metered"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// Reader is the provider read the fetcher makes; *github.Provider built by
// metered.NewProvider satisfies it. Every call it makes is admitted and booked
// by the governor through the metered runner, so the fetcher never calls gh
// around the governor.
type Reader interface {
	GetPendingReview(ctx context.Context, repo string, number int) (*github.PendingReviewData, error)
}

var _ Reader = (*github.Provider)(nil)

// Fetcher fetches the pending group; it implements groups.Fetcher.
type Fetcher struct {
	// Reader reads the pending review of one PR; it MUST be a Provider over a
	// metered runner (metered.NewProvider).
	Reader Reader
}

var _ groups.Fetcher = (*Fetcher)(nil)

// Group implements groups.Fetcher.
func (f *Fetcher) Group() string { return groups.Pending }

// MaxBatch implements groups.Fetcher: the group is read per PR.
func (f *Fetcher) MaxBatch() int { return 1 }

// Spend implements groups.Fetcher: one GraphQL point per target, as an
// estimate (the response's own cost is what the metered runner books).
func (f *Fetcher) Spend(class governor.Class, n int) governor.Request {
	return governor.Request{Bucket: governor.BucketGraphQL, Class: class, Cost: int64(max(n, 1))}
}

// Fetch implements groups.Fetcher: one provider read per target, in order. Each
// Outcome carries the stamps the target had before the call. A PR GitHub no
// longer finds is groups.ErrGone for that target; any other failure, a governor
// refusal included, is the error as it came. Once the governor refuses a read,
// or the context is done, the targets after it carry that error without a call.
func (f *Fetcher) Fetch(ctx context.Context, call ratefeed.Meta, targets []groups.Target) []groups.Outcome {
	out := make([]groups.Outcome, len(targets))
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
		c := call
		c.Group, c.BatchSize = groups.Pending, 1
		data, err := f.Reader.GetPendingReview(metered.WithCall(ctx, c), repoOf(t), t.Ref.Number)
		switch {
		case err != nil:
			if errors.Is(err, governor.ErrRefused) {
				stop = err
				out[i].Err = err
			} else if metered.IsNotFound(err) {
				out[i].Err = groups.ErrGone
			} else {
				out[i].Err = err
			}
			continue
		case data == nil:
			out[i].Err = fmt.Errorf("pending: the read of %s#%d returned no data", repoOf(t), t.Ref.Number)
			continue
		}
		content, err := json.Marshal(data)
		if err != nil {
			out[i].Err = fmt.Errorf("pending: encode %s: %w", t.NodeID, err)
			continue
		}
		out[i].Content = content
		out[i].Fingerprint = groups.Fingerprint(content)
	}
	return out
}

func repoOf(t groups.Target) string { return t.Ref.Owner + "/" + t.Ref.Repo }

// Decode reads the stored content of a pending group row.
func Decode(content []byte) (*github.PendingReviewData, error) {
	var d github.PendingReviewData
	if err := json.Unmarshal(content, &d); err != nil {
		return nil, fmt.Errorf("pending: decode stored group: %w", err)
	}
	return &d, nil
}

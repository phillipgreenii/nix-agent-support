package detail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

const (
	// MaxBatch is the most node ids one call carries. The Phase 1 spike measured
	// 1 point per call for 1 to 100 ids, but HTTP 502 at 74 ids (1 of 2 runs) and
	// at 100 (2 of 2): call reliability, not points, is the limit. A batch size
	// above it is lowered to it.
	MaxBatch = 50
	// DefaultBatch is the batch size when Fetcher.BatchSize is not set.
	DefaultBatch = 25
)

// document is the `nodes(ids:)` query. Changing it changes what a call costs and
// how reliably it answers at MaxBatch ids: re-measure first (TestDocument_Pinned
// fails on any change so that is not skipped).
//
// requestedReviewer is a union: a User, Bot or Mannequin has a login, a Team has
// a slug. A node that is not a pull request answers `{}`, and one that no longer
// resolves answers null; id tells the two real answers apart from those.
var document = `
query($ids: [ID!]!) {
  rateLimit { cost remaining resetAt }
  nodes(ids: $ids) {
    ... on PullRequest {
      id mergeStateStatus merged additions deletions changedFiles
      reviewRequests(first: 100) {
        nodes {
          requestedReviewer {
            ... on User { login }
            ... on Bot { login }
            ... on Mannequin { login }
            ... on Team { slug }
          }
        }
      }
    }
  }
}
`

// Fetcher fetches the detail group; it implements groups.Fetcher.
type Fetcher struct {
	// GH runs `gh <args...>`; *github.CLI satisfies it. It SHOULD NOT retry
	// transient failures itself: this fetcher retries (each attempt admitted by
	// the governor) and needs the first 502 or 504 of a batch to fall back on.
	GH ratefeed.Runner
	// Governor admits each call; Feed books its spend and reading.
	Governor *governor.Governor
	Feed     *ratefeed.Feed
	// BatchSize caps the ids per call; zero or less means DefaultBatch, and
	// anything above MaxBatch is lowered to it.
	BatchSize int
	// Retry bounds the retry of a transient failure of one call; nil means
	// github.DefaultRetryPolicy (three attempts in all, 500 ms then 1 s).
	Retry *github.RetryPolicy
}

var _ groups.Fetcher = (*Fetcher)(nil)

// Group implements groups.Fetcher.
func (f *Fetcher) Group() string { return groups.Detail }

// MaxBatch implements groups.Fetcher: BatchSize, capped at the package MaxBatch.
func (f *Fetcher) MaxBatch() int {
	n := f.BatchSize
	if n <= 0 {
		n = DefaultBatch
	}
	return min(n, MaxBatch)
}

// Spend implements groups.Fetcher: one GraphQL point per call, whatever n is
// (up to MaxBatch).
func (f *Fetcher) Spend(class governor.Class, _ int) governor.Request {
	return governor.Request{Bucket: governor.BucketGraphQL, Class: class, Cost: 1}
}

// Fetch implements groups.Fetcher. It fetches the targets in calls of at most
// MaxBatch() ids, in order, and returns one Outcome per target. Every Outcome
// carries the stamps the target had before the call (if the head moved during
// the call, the row is stale at once against the newer summary). The cost of a
// call is on the Outcome of the first target it served, so the sum over the
// Outcomes is what the fetch spent.
//
// A batch that answers HTTP 502 or 504 is fetched again one PR per call. Any
// other failure fails every target of the batch with the error as it came (a
// rate-limit answer included: the engine classifies it) and a *governor.Refusal
// when the governor declined. A node that no longer resolves is groups.ErrGone
// for its target only.
func (f *Fetcher) Fetch(ctx context.Context, call ratefeed.Meta, targets []groups.Target) []groups.Outcome {
	out := make([]groups.Outcome, len(targets))
	size := f.MaxBatch()
	for start := 0; start < len(targets); start += size {
		end := min(start+size, len(targets))
		f.fetchBatch(ctx, call, targets[start:end], out[start:end])
	}
	for i := range out {
		out[i].Stamps = targets[i].Stamps
	}
	return out
}

// fetchBatch fills out (one slot per target) for one batch of at most MaxBatch
// targets.
func (f *Fetcher) fetchBatch(ctx context.Context, call ratefeed.Meta, batch []groups.Target, out []groups.Outcome) {
	nodes, cost, err := f.call(ctx, call, ids(batch))
	out[0].Cost += cost
	if err == nil {
		fill(batch, out, nodes)
		return
	}
	if len(batch) > 1 && gateway(err) {
		f.fetchEach(ctx, call, batch, out)
		return
	}
	for i := range out {
		out[i].Err = err
	}
}

// fetchEach is the fallback for a batch that answered 502 or 504: one call per
// PR, each its own admission. Once admission is refused or the context is done
// the rest of the batch carries that error without a call.
func (f *Fetcher) fetchEach(ctx context.Context, call ratefeed.Meta, batch []groups.Target, out []groups.Outcome) {
	var stop error
	for i, t := range batch {
		if stop == nil {
			stop = ctx.Err()
		}
		if stop != nil {
			out[i].Err = stop
			continue
		}
		nodes, cost, err := f.call(ctx, call, []string{t.NodeID})
		out[i].Cost += cost
		if err != nil {
			out[i].Err = err
			if errors.Is(err, governor.ErrRefused) {
				stop = err
			}
			continue
		}
		fill(batch[i:i+1], out[i:i+1], nodes)
	}
}

func ids(batch []groups.Target) []string {
	out := make([]string, len(batch))
	for i, t := range batch {
		out[i] = t.NodeID
	}
	return out
}

// fill turns the answered nodes into outcomes; nodes has one entry per target.
func fill(batch []groups.Target, out []groups.Outcome, nodes []*wireNode) {
	for i := range batch {
		d, ok := nodes[i].detail()
		if !ok {
			out[i].Err = groups.ErrGone
			continue
		}
		content, err := d.canonical()
		if err != nil {
			out[i].Err = fmt.Errorf("detail: encode %s: %w", batch[i].NodeID, err)
			continue
		}
		out[i].Content = content
		out[i].Fingerprint = Fingerprint(d)
	}
}

type wireNode struct {
	ID               string `json:"id"`
	MergeStateStatus string `json:"mergeStateStatus"`
	Merged           bool   `json:"merged"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
	ChangedFiles     int    `json:"changedFiles"`
	ReviewRequests   struct {
		Nodes []struct {
			// RequestedReviewer is null when the requested account is gone.
			RequestedReviewer *struct {
				Login string `json:"login"`
				Slug  string `json:"slug"`
			} `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
}

type wireResponse struct {
	Data struct {
		Nodes []*wireNode `json:"nodes"`
	} `json:"data"`
	Errors []struct {
		Type string `json:"type"`
	} `json:"errors"`
}

// onlyNotFound reports whether the response carries errors and every one is a
// NOT_FOUND: the answer for a node id that no longer resolves, which gh also
// reports as a failed exit. The node itself is null in the data.
func (r wireResponse) onlyNotFound() bool {
	if len(r.Errors) == 0 {
		return false
	}
	for _, e := range r.Errors {
		if e.Type != "NOT_FOUND" {
			return false
		}
	}
	return true
}

// detail converts the node; ok is false for a node that is not a pull request
// (null, or an object without an id).
func (n *wireNode) detail() (d Detail, ok bool) {
	if n == nil || n.ID == "" {
		return Detail{}, false
	}
	d = Detail{
		Merged: n.Merged, MergeStateStatus: n.MergeStateStatus,
		Additions: n.Additions, Deletions: n.Deletions, ChangedFiles: n.ChangedFiles,
		ReviewRequests: []string{},
	}
	for _, rn := range n.ReviewRequests.Nodes {
		switch r := rn.RequestedReviewer; {
		case r == nil:
		case r.Login != "":
			d.ReviewRequests = append(d.ReviewRequests, r.Login)
		case r.Slug != "":
			d.ReviewRequests = append(d.ReviewRequests, r.Slug)
		}
	}
	return d, true
}

// errBadAnswer marks an answer gh printed that is not what was asked for; it is
// never retried.
var errBadAnswer = errors.New("detail: unusable answer")

// call makes the GraphQL call for ids, retrying a transient failure, and returns
// one node per id, the points the attempts spent (as the responses reported
// them) and the error. Every attempt is admitted by the governor on its own.
func (f *Fetcher) call(ctx context.Context, call ratefeed.Meta, ids []string) ([]*wireNode, int64, error) {
	policy := github.DefaultRetryPolicy()
	if f.Retry != nil {
		policy = *f.Retry
	}
	var total int64
	for attempt := 1; ; attempt++ {
		nodes, cost, err := f.once(ctx, call, ids)
		total += cost
		if err == nil {
			return nodes, total, nil
		}
		// A multi-id batch that answers 502 or 504 is not retried as a batch: the
		// caller falls back to one call per PR.
		batchGateway := len(ids) > 1 && gateway(err)
		if attempt >= policy.MaxAttempts || batchGateway || errors.Is(err, errBadAnswer) ||
			errors.Is(err, governor.ErrRefused) || !github.IsTransient(ctx, err) {
			return nil, total, err
		}
		delay := time.Duration(0)
		if policy.Backoff != nil {
			delay = policy.Backoff(attempt)
		}
		if !fits(ctx, delay, policy.MinRetryWindow) {
			return nil, total, err
		}
		if serr := sleep(ctx, policy, delay); serr != nil {
			return nil, total, err
		}
	}
}

// once makes one admitted GraphQL call.
func (f *Fetcher) once(ctx context.Context, call ratefeed.Meta, ids []string) ([]*wireNode, int64, error) {
	// A batch of 1 to MaxBatch ids is 1 point (see MaxBatch).
	tk, err := f.Governor.Admit(f.Spend(call.Class, len(ids)))
	if err != nil {
		return nil, 0, err
	}
	args := []string{"api", "graphql", "-F", "query=" + document}
	for _, id := range ids {
		args = append(args, "-f", "ids[]="+id)
	}
	body, runErr := f.GH.Run(ctx, args...)
	call.Group, call.BatchSize = groups.Detail, len(ids)
	_ = f.Feed.SettleGraphQL(tk, call, body, runErr)
	var cost int64
	if rd, found, perr := ratefeed.ParseGraphQL(body); perr == nil && found {
		cost = rd.Cost
	}
	var resp wireResponse
	jsonErr := json.Unmarshal(body, &resp)
	switch {
	case runErr != nil && (jsonErr != nil || !resp.onlyNotFound()):
		return nil, cost, runErr
	case runErr == nil && jsonErr != nil:
		return nil, cost, fmt.Errorf("%w: parse nodes response: %w", errBadAnswer, jsonErr)
	}
	if len(resp.Data.Nodes) != len(ids) {
		return nil, cost, fmt.Errorf("%w: asked for %d nodes, got %d", errBadAnswer, len(ids), len(resp.Data.Nodes))
	}
	return resp.Data.Nodes, cost, nil
}

// gateway reports whether err is gh's HTTP 502 or 504 answer.
func gateway(err error) bool {
	msg := strings.ToLower(github.ErrorText(err))
	return strings.Contains(msg, "http 502") || strings.Contains(msg, "http 504")
}

// fits reports whether a retry started after delay still leaves minWindow before
// the context's deadline; a context without one always fits.
func fits(ctx context.Context, delay, minWindow time.Duration) bool {
	dl, ok := ctx.Deadline()
	if !ok || minWindow <= 0 {
		return true
	}
	return time.Until(dl) >= delay+minWindow
}

// sleep waits d, or returns ctx.Err() as soon as ctx is done.
func sleep(ctx context.Context, p github.RetryPolicy, d time.Duration) error {
	if p.Sleep != nil {
		return p.Sleep(ctx, d)
	}
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

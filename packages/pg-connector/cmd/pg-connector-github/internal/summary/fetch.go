package summary

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// MaxBatch is the most node ids one call carries. It is the points boundary of
// this document: a batch of 1 to 74 ids costs 1 point, 75 to 100 cost 2 (the
// Phase 1 spike's result, still true with the added fields). Config may lower
// the batch size; it never raises it past this.
const MaxBatch = 74

// document is the `nodes(ids:)` query: the batched search field set, plus the
// head and base branch names and the base commit. Changing it changes the cost
// boundary behind MaxBatch: re-measure with rateLimit(dryRun: true) at 74 and
// 75 ids first (TestDocument_Pinned fails on any change so that is not skipped).
var document = `
query($ids: [ID!]!) {
  rateLimit { cost remaining resetAt }
  nodes(ids: $ids) {
    ... on PullRequest {
` + github.BatchedPRFields + `        headRefName baseRefName baseRefOid
    }
  }
}
`

// Fetcher fetches the summary group.
type Fetcher struct {
	// GH runs `gh <args...>`; *github.CLI satisfies it.
	GH ratefeed.Runner
	// Governor admits each call; Feed books its spend and reading.
	Governor *governor.Governor
	Feed     *ratefeed.Feed
	// BatchSize caps the ids per call; zero means config.DefaultBatchSize, and
	// anything above MaxBatch is lowered to it.
	BatchSize int
}

func (f *Fetcher) batchSize() int {
	n := f.BatchSize
	if n <= 0 {
		n = config.DefaultBatchSize
	}
	return min(n, MaxBatch)
}

// Fetch fetches the summary group of every node id, at most batchSize ids per
// call, in order. The result has one Summary per id, in the same order; a node
// GitHub did not return as a pull request is a Summary with Gone set. The
// returned cost is the points the calls spent, as each response reported it. It
// admits every call as call.Class (call.Purpose is what the budget row records;
// its Group and BatchSize are filled in here).
//
// On failure it returns the summaries of the calls that finished before it,
// with the cost they spent, and the error: a *governor.Refusal when admission
// was declined (nothing ran for that call), otherwise gh's or the parser's.
func (f *Fetcher) Fetch(ctx context.Context, call ratefeed.Meta, nodeIDs []string) ([]Summary, int64, error) {
	var (
		out  = make([]Summary, 0, len(nodeIDs))
		cost int64
		size = f.batchSize()
	)
	for start := 0; start < len(nodeIDs); start += size {
		ids := nodeIDs[start:min(start+size, len(nodeIDs))]
		got, c, err := f.fetchBatch(ctx, call, ids)
		cost += c
		if err != nil {
			return out, cost, err
		}
		out = append(out, got...)
	}
	return out, cost, nil
}

type wireNode struct {
	ID             string `json:"id"`
	Number         int    `json:"number"`
	Title          string `json:"title"`
	URL            string `json:"url"`
	State          string `json:"state"`
	Body           string `json:"body"`
	IsDraft        bool   `json:"isDraft"`
	UpdatedAt      string `json:"updatedAt"`
	ReviewDecision string `json:"reviewDecision"`
	Mergeable      string `json:"mergeable"`
	Author         *struct {
		Login string `json:"login"`
	} `json:"author"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Labels struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Comments struct {
		TotalCount int `json:"totalCount"`
	} `json:"comments"`
	Reviews struct {
		TotalCount int `json:"totalCount"`
	} `json:"reviews"`
	ReviewThreads struct {
		TotalCount int `json:"totalCount"`
	} `json:"reviewThreads"`
	HeadRefOid  string `json:"headRefOid"`
	HeadRefName string `json:"headRefName"`
	BaseRefName string `json:"baseRefName"`
	BaseRefOid  string `json:"baseRefOid"`
	Commits     struct {
		Nodes []struct {
			Commit struct {
				// A commit with no checks answers statusCheckRollup as null,
				// which decodes to the zero state.
				StatusCheckRollup struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
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

func (n *wireNode) summary(id string) Summary {
	if n == nil || n.Number == 0 {
		return Summary{NodeID: id, Gone: true}
	}
	s := Summary{
		NodeID: n.ID, Repo: n.Repository.NameWithOwner, Number: n.Number, Title: n.Title, URL: n.URL,
		State: n.State, Body: n.Body, IsDraft: n.IsDraft, UpdatedAt: n.UpdatedAt,
		ReviewDecision: n.ReviewDecision, Mergeable: n.Mergeable,
		LabelCount: n.Labels.TotalCount, CommentCount: n.Comments.TotalCount,
		ReviewCount: n.Reviews.TotalCount, ReviewThreadCount: n.ReviewThreads.TotalCount,
		HeadRefOid: n.HeadRefOid, HeadRefName: n.HeadRefName,
		BaseRefName: n.BaseRefName, BaseRefOid: n.BaseRefOid,
	}
	if s.NodeID == "" {
		s.NodeID = id
	}
	if n.Author != nil {
		s.Author = n.Author.Login
	}
	s.Labels = make([]string, 0, len(n.Labels.Nodes))
	for _, l := range n.Labels.Nodes {
		s.Labels = append(s.Labels, l.Name)
	}
	if len(n.Commits.Nodes) > 0 {
		s.ChecksState = n.Commits.Nodes[0].Commit.StatusCheckRollup.State
	}
	return s
}

func (f *Fetcher) fetchBatch(ctx context.Context, call ratefeed.Meta, ids []string) ([]Summary, int64, error) {
	// A batch of 1 to MaxBatch ids is 1 point (see MaxBatch).
	tk, err := f.Governor.Admit(governor.Request{Bucket: governor.BucketGraphQL, Class: call.Class, Cost: 1})
	if err != nil {
		return nil, 0, err
	}
	args := []string{"api", "graphql", "-F", "query=" + document}
	for _, id := range ids {
		args = append(args, "-f", "ids[]="+id)
	}
	body, runErr := f.GH.Run(ctx, args...)
	call.Group, call.BatchSize = GroupName, len(ids)
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
		return nil, cost, fmt.Errorf("summary: parse nodes response: %w", jsonErr)
	}
	if len(resp.Data.Nodes) != len(ids) {
		return nil, cost, fmt.Errorf("summary: asked for %d nodes, got %d", len(ids), len(resp.Data.Nodes))
	}
	out := make([]Summary, len(ids))
	for i, n := range resp.Data.Nodes {
		out[i] = n.summary(ids[i])
	}
	return out, cost, nil
}

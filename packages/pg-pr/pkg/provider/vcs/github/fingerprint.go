package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// fingerprintQuery is the slim sibling of enrichedPRsQuery: no node bodies,
// only change-detection fields + updatedAt. $after drives pagination (null on
// the first page).
const fingerprintQuery = `query($search: String!, $after: String) {
  rateLimit { cost remaining }
  search(query: $search, type: ISSUE, first: 100, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes {
      ... on PullRequest {
        number
        updatedAt
        isDraft
        state
        author { __typename login }
        repository { nameWithOwner }
        commits(last: 1) { nodes { commit { oid statusCheckRollup { state } } } }
        reviews { totalCount }
        comments { totalCount }
        reviewThreads { totalCount }
      }
    }
  }
}`

// maxFingerprintPages caps pagination so a pathological roster can't loop
// forever; hitting it sets Truncated.
const maxFingerprintPages = 20

type ghFingerprintResponse struct {
	Data struct {
		RateLimit struct {
			Cost      int `json:"cost"`
			Remaining int `json:"remaining"`
		} `json:"rateLimit"`
		Search struct {
			PageInfo ghPageInfo `json:"pageInfo"`
			Nodes    []ghFPNode `json:"nodes"`
		} `json:"search"`
	} `json:"data"`
	Errors []ghGraphQLError `json:"errors"`
}

type ghFPNode struct {
	Number     int     `json:"number"`
	UpdatedAt  string  `json:"updatedAt"`
	IsDraft    bool    `json:"isDraft"`
	State      string  `json:"state"`
	Author     *ghUser `json:"author"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				OID               string `json:"oid"`
				StatusCheckRollup *struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	Reviews struct {
		TotalCount int `json:"totalCount"`
	} `json:"reviews"`
	Comments struct {
		TotalCount int `json:"totalCount"`
	} `json:"comments"`
	ReviewThreads struct {
		TotalCount int `json:"totalCount"`
	} `json:"reviewThreads"`
}

// parseFingerprints decodes one page. Returns the page's PRs+rate info, the
// endCursor, whether another page exists, and any error.
func parseFingerprints(raw []byte) (vcs.FingerprintResult, string, bool, error) {
	var resp ghFingerprintResponse
	if e := json.Unmarshal(raw, &resp); e != nil {
		return vcs.FingerprintResult{}, "", false, fmt.Errorf("github: parse fingerprint response: %w", e)
	}
	if len(resp.Errors) > 0 {
		return vcs.FingerprintResult{}, "", false, fmt.Errorf("github: GraphQL error: %s", resp.Errors[0].Message)
	}
	res := vcs.FingerprintResult{
		RateCost: resp.Data.RateLimit.Cost,
		RateLeft: resp.Data.RateLimit.Remaining,
		PRs:      make([]vcs.PRFingerprint, 0, len(resp.Data.Search.Nodes)),
	}
	for _, n := range resp.Data.Search.Nodes {
		if n.Number == 0 {
			continue // non-PullRequest node
		}
		fp := vcs.PRFingerprint{
			Repo:              n.Repository.NameWithOwner,
			Number:            n.Number,
			Author:            n.Author.canonicalLogin(),
			IsDraft:           n.IsDraft,
			State:             strings.ToLower(n.State),
			UpdatedAt:         n.UpdatedAt,
			ReviewCount:       n.Reviews.TotalCount,
			CommentCount:      n.Comments.TotalCount,
			ReviewThreadCount: n.ReviewThreads.TotalCount,
		}
		if len(n.Commits.Nodes) > 0 {
			fp.HeadOID = n.Commits.Nodes[0].Commit.OID
			if r := n.Commits.Nodes[0].Commit.StatusCheckRollup; r != nil {
				fp.StatusRollup = r.State
			}
		}
		res.PRs = append(res.PRs, fp)
	}
	return res, resp.Data.Search.PageInfo.EndCursor, resp.Data.Search.PageInfo.HasNextPage, nil
}

// FingerprintPRs runs the fingerprint search, paginating until the roster is
// complete or maxFingerprintPages is hit (Truncated=true). RateCost/RateLeft
// reflect the last page fetched.
func (p *Provider) FingerprintPRs(ctx context.Context, searchQuery string) (vcs.FingerprintResult, error) {
	if strings.TrimSpace(searchQuery) == "" {
		return vcs.FingerprintResult{}, fmt.Errorf("github: search query required for FingerprintPRs")
	}
	var acc vcs.FingerprintResult
	cursor := ""
	for page := 0; ; page++ {
		args := []string{"api", "graphql", "-F", "search=" + searchQuery, "-F", "query=@-"}
		if cursor != "" {
			args = append(args, "-F", "after="+cursor)
		}
		raw, err := p.gh.RunStdin(ctx, []byte(fingerprintQuery), args...)
		if err != nil {
			return vcs.FingerprintResult{}, fmt.Errorf("github: gh api graphql (fingerprint): %w", err)
		}
		pageRes, next, more, err := parseFingerprints(raw)
		if err != nil {
			return vcs.FingerprintResult{}, err
		}
		acc.PRs = append(acc.PRs, pageRes.PRs...)
		acc.RateCost = pageRes.RateCost
		acc.RateLeft = pageRes.RateLeft
		if !more {
			break
		}
		if page+1 >= maxFingerprintPages {
			acc.Truncated = true
			break
		}
		cursor = next
	}
	return acc, nil
}

// Compile-time check that *Provider satisfies vcs.FingerprintProvider.
var _ vcs.FingerprintProvider = (*Provider)(nil)

package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// PageSize is the first: argument of the membership-only document. The 2026-10-05
// design's "Measured cost per call shape" measured both first: 50 and first: 100
// at 1 point per page for a membership-only selection (a node id, the number
// and the repository name); 100 is the larger page, so a full listing is the
// fewest calls.
const PageSize = 100

// SearchResultLimit is GitHub's cap on the results of one search: a query that
// matches more is cut off at this many, so its listing is not complete.
const SearchResultLimit = 1000

// maxPages is how many pages the limit allows at PageSize.
const maxPages = SearchResultLimit / PageSize

// membershipDocument is the membership-only GraphQL form of a query: only what
// names a member (its node id, the PR number and the repository), so a page
// costs 1 point however many members it lists. It selects no field of a PR's
// summary: those are fetched, batched, through the summary group. Changing the
// selection changes the measured cost, so TestMembershipDocument_Pinned fails on
// any change: re-measure with rateLimit(dryRun: true) before editing.
var membershipDocument = `
query($q: String!, $after: String) {
  rateLimit { cost remaining resetAt }
  search(query: $q, type: ISSUE, first: 100, after: $after) {
    issueCount
    pageInfo { hasNextPage endCursor }
    nodes {
      ... on PullRequest { id number repository { nameWithOwner } }
    }
  }
}
`

// member is one listed PR.
type member struct {
	nodeID string
	ref    entity.Ref
}

// listing is what running a query returned: its members in the order GitHub
// listed them (the expressions of a multi-expression query in turn, a member
// that several of them match kept at its first position) and whether the list
// is the whole result.
type listing struct {
	members  []member
	complete bool
}

type wireSearch struct {
	Data struct {
		Search *struct {
			IssueCount int `json:"issueCount"`
			PageInfo   struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []struct {
				ID         string `json:"id"`
				Number     int    `json:"number"`
				Repository struct {
					NameWithOwner string `json:"nameWithOwner"`
				} `json:"repository"`
			} `json:"nodes"`
		} `json:"search"`
	} `json:"data"`
	Errors []struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"errors"`
}

// list runs every expression of a query (with key's arguments appended as
// qualifiers) and unions the members. Each page is one admitted, metered call
// of the given class. The listing is complete only when every expression's
// pages were read to the end and none matched more than SearchResultLimit
// results (GitHub then answers the first SearchResultLimit and says nothing
// else, so a listing past the limit is flagged by its issueCount).
func (r *Runner) list(ctx context.Context, key Key, exprs []string, call ratefeed.Meta) (listing, error) {
	out := listing{complete: true}
	seen := map[string]bool{}
	for _, expr := range exprs {
		q := "is:pr " + strings.TrimSpace(expr) + key.qualifiers()
		after := ""
		for page := 1; ; page++ {
			resp, err := r.searchPage(ctx, call, q, after)
			if err != nil {
				return listing{}, err
			}
			s := resp.Data.Search
			if page == 1 && s.IssueCount > SearchResultLimit {
				out.complete = false
			}
			for _, n := range s.Nodes {
				if n.Number == 0 || n.ID == "" {
					continue // not a pull request
				}
				ref, err := entity.NewRef(n.Repository.NameWithOwner, n.Number)
				if err != nil {
					return listing{}, fmt.Errorf("query %q: %w", key.Name, err)
				}
				if seen[n.ID] {
					continue
				}
				seen[n.ID] = true
				out.members = append(out.members, member{nodeID: n.ID, ref: ref})
			}
			if !s.PageInfo.HasNextPage || s.PageInfo.EndCursor == "" {
				break
			}
			if page >= maxPages {
				out.complete = false // more pages than the limit lets us read
				break
			}
			after = s.PageInfo.EndCursor
		}
	}
	return out, nil
}

// searchPage admits, runs and books one page of the membership document and
// parses its answer.
func (r *Runner) searchPage(ctx context.Context, call ratefeed.Meta, q, after string) (wireSearch, error) {
	// A page is 1 point (see PageSize).
	tk, err := r.cfg.Governor.Admit(governor.Request{Bucket: governor.BucketGraphQL, Class: call.Class, Cost: 1})
	if err != nil {
		return wireSearch{}, err
	}
	args := []string{"api", "graphql", "-F", "query=" + membershipDocument, "-f", "q=" + q}
	if after != "" {
		args = append(args, "-f", "after="+after)
	}
	body, runErr := r.cfg.GH.Run(ctx, args...)
	call.Group = Group
	var resp wireSearch
	jsonErr := json.Unmarshal(body, &resp)
	if jsonErr == nil && resp.Data.Search != nil {
		call.BatchSize = len(resp.Data.Search.Nodes)
	}
	_ = r.cfg.Feed.SettleGraphQL(tk, call, body, runErr)
	r.observe(runErr)
	switch {
	case runErr != nil:
		return wireSearch{}, runErr
	case jsonErr != nil:
		return wireSearch{}, fmt.Errorf("query: parse search response: %w", jsonErr)
	case resp.Data.Search == nil:
		return wireSearch{}, errors.New("query: search response carried no result" + firstError(resp))
	}
	return resp, nil
}

func firstError(r wireSearch) string {
	if len(r.Errors) == 0 {
		return ""
	}
	if r.Errors[0].Message != "" {
		return ": " + r.Errors[0].Message
	}
	return ": " + r.Errors[0].Type
}

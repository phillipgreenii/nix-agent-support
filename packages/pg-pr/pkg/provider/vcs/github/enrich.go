// Package github — bulk PR enrichment via a single GraphQL search query.
//
// EnrichedPRs collapses the per-PR REST fan-out the sync snapshot loop
// previously made (6 author-list calls + ~3 enrichment calls per PR)
// into one search { ... nodes { ... on PullRequest } } query per repo.
// Replaces ~78 REST calls/tick with one GraphQL call (~53 GraphQL
// points) on a 24-PR repo; latency drops from ~100s of sequential gh
// shell-outs to ~13s for one round-trip.
//
// Pagination: the inner connections (reviews, comments, reviewThreads,
// thread.comments, statusCheckRollup.contexts) are fetched with
// first:30. Workspaces at the documented pg-pr scale (≤50 PRs) never
// hit those caps in sampled data, but when they do, the returned
// EnrichedPR.Truncated lists the affected dimensions so the sync engine
// can fall back to per-PR REST calls for the outlier.
//
// Node-budget math: 50 PRs × (1 + 30 reviews + 30 comments + 30
// threads × (1 + 30 thread-comments) + 30 CI contexts) ≈ 51k nodes,
// well under GitHub's 500k limit.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// enrichedPRsQuery is the GraphQL search query EnrichedPRs runs once
// per repo per tick. See package comment for node-budget math.
//
// Top-level comments are ordered by UPDATED_AT DESC so the steady-state
// case (≤30 comments since last tick) fits in one page. Review threads
// and thread.comments have no orderBy in GitHub's schema, so they
// always paginate from creation-time.
const enrichedPRsQuery = `query($search: String!) {
  rateLimit { cost remaining resetAt }
  search(query: $search, type: ISSUE, first: 50) {
    issueCount
    pageInfo { hasNextPage endCursor }
    nodes {
      ... on PullRequest {
        number
        title
        url
        author { __typename login }
        baseRefName
        headRefName
        isDraft
        state
        merged
        additions
        deletions
        changedFiles
        repository { nameWithOwner }
        reviews(first: 30) {
          totalCount
          pageInfo { hasNextPage }
          nodes {
            id
            state
            author { __typename login }
            body
            submittedAt
          }
        }
        comments(first: 30, orderBy: { field: UPDATED_AT, direction: DESC }) {
          totalCount
          pageInfo { hasNextPage }
          nodes {
            id
            author { __typename login }
            authorAssociation
            body
            createdAt
          }
        }
        reviewThreads(first: 30) {
          totalCount
          pageInfo { hasNextPage }
          nodes {
            id
            isResolved
            isOutdated
            comments(first: 30) {
              totalCount
              pageInfo { hasNextPage }
              nodes {
                id
                author { __typename login }
                authorAssociation
                body
                path
                originalLine
                line
                createdAt
                isMinimized
                minimizedReason
                originalCommit { oid }
              }
            }
          }
        }
        body
        labels(first: 20) { totalCount pageInfo { hasNextPage } nodes { name } }
        files(first: 100) { totalCount pageInfo { hasNextPage } nodes { path } }
        commits(last: 20) {
          totalCount
          pageInfo { hasNextPage }
          nodes {
            commit {
              oid
              message
              statusCheckRollup {
                state
                contexts(first: 30) {
                  totalCount
                  pageInfo { hasNextPage }
                  nodes {
                    __typename
                    ... on CheckRun {
                      id
                      name
                      status
                      conclusion
                      detailsUrl
                    }
                    ... on StatusContext {
                      id
                      context
                      state
                      targetUrl
                    }
                  }
                }
              }
            }
          }
        }
      }
    }
  }
}`

// EnrichedPRs returns every open PR matching searchQuery in repo,
// bundled with its reviews, top-level comments, review-thread comments,
// and CI status. One gh GraphQL call regardless of PR count.
//
// searchQuery is a GitHub-search-style string and is passed verbatim to
// the GraphQL `search` query argument. Callers should already include a
// `repo:<repo>` clause for clarity — the repo parameter is informational
// (for error messages) and is not appended to the query.
func (p *Provider) EnrichedPRs(ctx context.Context, repo, searchQuery string) ([]vcs.EnrichedPR, error) {
	if strings.TrimSpace(searchQuery) == "" {
		return nil, fmt.Errorf("github: search query required for EnrichedPRs")
	}
	// The GraphQL query is fed via stdin (`-F query=@-`); search is passed
	// as a string variable so multi-author search strings don't fight
	// gh's shell-arg parsing.
	raw, err := p.gh.RunStdin(ctx, []byte(enrichedPRsQuery),
		"api", "graphql",
		"-F", "search="+searchQuery,
		"-F", "query=@-",
	)
	if err != nil {
		return nil, fmt.Errorf("github: gh api graphql for %s: %w", repo, err)
	}
	return parseEnrichedPRs(raw, repo)
}

// ghGraphQLResponse is the envelope shape returned by gh api graphql.
type ghGraphQLResponse struct {
	Data struct {
		RateLimit struct {
			Cost      int    `json:"cost"`
			Remaining int    `json:"remaining"`
			ResetAt   string `json:"resetAt"`
		} `json:"rateLimit"`
		Search struct {
			IssueCount int        `json:"issueCount"`
			PageInfo   ghPageInfo `json:"pageInfo"`
			Nodes      []ghPRNode `json:"nodes"`
		} `json:"search"`
	} `json:"data"`
	Errors []ghGraphQLError `json:"errors"`
}

// ghGraphQLError mirrors the error envelope GitHub returns on partial
// failures. The presence of an `errors` array — even with `data` set —
// is treated as a failure so the caller knows to fall back.
type ghGraphQLError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type ghPageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type ghPRNode struct {
	Number       int     `json:"number"`
	Title        string  `json:"title"`
	URL          string  `json:"url"`
	Author       *ghUser `json:"author"`
	BaseRefName  string  `json:"baseRefName"`
	HeadRefName  string  `json:"headRefName"`
	IsDraft      bool    `json:"isDraft"`
	State        string  `json:"state"`
	Merged       bool    `json:"merged"`
	Additions    int     `json:"additions"`
	Deletions    int     `json:"deletions"`
	ChangedFiles int     `json:"changedFiles"`
	Repository   struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Body   string `json:"body"`
	Labels struct {
		TotalCount int        `json:"totalCount"`
		PageInfo   ghPageInfo `json:"pageInfo"`
		Nodes      []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Files struct {
		TotalCount int        `json:"totalCount"`
		PageInfo   ghPageInfo `json:"pageInfo"`
		Nodes      []struct {
			Path string `json:"path"`
		} `json:"nodes"`
	} `json:"files"`
	Reviews       ghReviewsConn       `json:"reviews"`
	Comments      ghCommentsConn      `json:"comments"`
	ReviewThreads ghReviewThreadsConn `json:"reviewThreads"`
	Commits       struct {
		TotalCount int        `json:"totalCount"`
		PageInfo   ghPageInfo `json:"pageInfo"`
		Nodes      []struct {
			Commit struct {
				OID               string `json:"oid"`
				Message           string `json:"message"`
				StatusCheckRollup *struct {
					State    string         `json:"state"`
					Contexts ghContextsConn `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

type ghUser struct {
	Typename string `json:"__typename"`
	Login    string `json:"login"`
}

// canonicalLogin returns the author identifier in the form the REST APIs
// use, so fingerprints generated from GraphQL match the existing
// REST-derived ones. Without this, bot logins come back as "claude"
// instead of "claude[bot]" and every bot comment dedups against nothing.
func (u *ghUser) canonicalLogin() string {
	if u == nil {
		return ""
	}
	if u.Typename == "Bot" && !strings.HasSuffix(u.Login, "[bot]") {
		return u.Login + "[bot]"
	}
	return u.Login
}

type ghReviewsConn struct {
	TotalCount int        `json:"totalCount"`
	PageInfo   ghPageInfo `json:"pageInfo"`
	Nodes      []struct {
		ID          string  `json:"id"`
		State       string  `json:"state"`
		Author      *ghUser `json:"author"`
		Body        string  `json:"body"`
		SubmittedAt string  `json:"submittedAt"`
	} `json:"nodes"`
}

type ghCommentsConn struct {
	TotalCount int        `json:"totalCount"`
	PageInfo   ghPageInfo `json:"pageInfo"`
	Nodes      []struct {
		ID                string  `json:"id"`
		Author            *ghUser `json:"author"`
		AuthorAssociation string  `json:"authorAssociation"`
		Body              string  `json:"body"`
		CreatedAt         string  `json:"createdAt"`
	} `json:"nodes"`
}

type ghReviewThreadsConn struct {
	TotalCount int        `json:"totalCount"`
	PageInfo   ghPageInfo `json:"pageInfo"`
	Nodes      []struct {
		ID         string `json:"id"`
		IsResolved bool   `json:"isResolved"`
		IsOutdated bool   `json:"isOutdated"`
		Comments   struct {
			TotalCount int        `json:"totalCount"`
			PageInfo   ghPageInfo `json:"pageInfo"`
			Nodes      []struct {
				ID                string  `json:"id"`
				Author            *ghUser `json:"author"`
				AuthorAssociation string  `json:"authorAssociation"`
				Body              string  `json:"body"`
				Path              string  `json:"path"`
				OriginalLine      int     `json:"originalLine"`
				Line              int     `json:"line"`
				CreatedAt         string  `json:"createdAt"`
				IsMinimized       bool    `json:"isMinimized"`
				MinimizedReason   string  `json:"minimizedReason"`
				OriginalCommit    *struct {
					OID string `json:"oid"`
				} `json:"originalCommit"`
			} `json:"nodes"`
		} `json:"comments"`
	} `json:"nodes"`
}

type ghContextsConn struct {
	TotalCount int        `json:"totalCount"`
	PageInfo   ghPageInfo `json:"pageInfo"`
	Nodes      []struct {
		Typename string `json:"__typename"`
		// CheckRun fields
		ID         string `json:"id"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		DetailsURL string `json:"detailsUrl"`
		// StatusContext fields
		Context   string `json:"context"`
		State     string `json:"state"`
		TargetURL string `json:"targetUrl"`
	} `json:"nodes"`
}

// parseEnrichedPRs decodes a gh GraphQL response and maps it into
// vcs.EnrichedPR shapes. Surfaces gh's `errors` envelope as a hard
// failure so callers fall back to REST.
func parseEnrichedPRs(raw []byte, repo string) ([]vcs.EnrichedPR, error) {
	var resp ghGraphQLResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("github: parse GraphQL response for %s: %w", repo, err)
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("github: GraphQL error for %s: %s", repo, resp.Errors[0].Message)
	}
	out := make([]vcs.EnrichedPR, 0, len(resp.Data.Search.Nodes))
	for _, n := range resp.Data.Search.Nodes {
		if n.Number == 0 {
			continue // skip non-PullRequest nodes
		}
		ep := vcs.EnrichedPR{PR: prFromGHNode(n, repo)}
		ep.Reviews = reviewsFromGHNode(n)
		ep.Comments = commentsFromGHNode(n)
		ep.CIRuns = ciRunsFromGHNode(n)
		for _, f := range n.Files.Nodes {
			ep.Files = append(ep.Files, f.Path)
		}
		for _, c := range n.Commits.Nodes {
			ep.Commits = append(ep.Commits, c.Commit.Message)
		}
		ep.Truncated = truncationFlags(n)
		out = append(out, ep)
	}
	return out, nil
}

func prFromGHNode(n ghPRNode, repo string) api.PR {
	author := n.Author.canonicalLogin()
	owner := n.Repository.NameWithOwner
	if owner == "" {
		owner = repo
	}
	// With commits(last: 20), nodes are in ascending chronological order;
	// the last element is the newest (head) commit.
	var headSHA string
	if len(n.Commits.Nodes) > 0 {
		headSHA = n.Commits.Nodes[len(n.Commits.Nodes)-1].Commit.OID
	}
	pr := api.PR{
		Repo:         owner,
		Number:       n.Number,
		Title:        n.Title,
		State:        strings.ToLower(n.State),
		Branch:       n.HeadRefName,
		Base:         n.BaseRefName,
		Author:       author,
		URL:          n.URL,
		Draft:        n.IsDraft,
		Merged:       n.Merged,
		Additions:    n.Additions,
		Deletions:    n.Deletions,
		ChangedFiles: n.ChangedFiles,
		HeadSHA:      headSHA,
		Body:         n.Body,
	}
	for _, l := range n.Labels.Nodes {
		pr.Labels = append(pr.Labels, l.Name)
	}
	return pr
}

func reviewsFromGHNode(n ghPRNode) []api.Review {
	if len(n.Reviews.Nodes) == 0 {
		return nil
	}
	out := make([]api.Review, 0, len(n.Reviews.Nodes))
	for _, r := range n.Reviews.Nodes {
		out = append(out, api.Review{
			ID:     r.ID,
			Author: r.Author.canonicalLogin(),
			State:  r.State,
			Body:   r.Body,
		})
	}
	return out
}

// commentsFromGHNode collapses both top-level issue comments and
// review-thread inline comments into one []api.Comment. Thread comments
// carry Path/Line/ThreadID (the thread's node id, not the comment's —
// fixing the long-standing REST shim that used the comment id instead).
func commentsFromGHNode(n ghPRNode) []api.Comment {
	var out []api.Comment
	for _, c := range n.Comments.Nodes {
		out = append(out, api.Comment{
			ID:         c.ID,
			Author:     c.Author.canonicalLogin(),
			AuthorRole: strings.ToLower(c.AuthorAssociation),
			Body:       c.Body,
			CreatedAt:  c.CreatedAt,
		})
	}
	for _, t := range n.ReviewThreads.Nodes {
		for _, c := range t.Comments.Nodes {
			line := c.Line
			if line == 0 {
				line = c.OriginalLine
			}
			var originalCommitOID string
			if c.OriginalCommit != nil {
				originalCommitOID = c.OriginalCommit.OID
			}
			out = append(out, api.Comment{
				ID:                c.ID,
				Author:            c.Author.canonicalLogin(),
				AuthorRole:        strings.ToLower(c.AuthorAssociation),
				Body:              c.Body,
				CreatedAt:         c.CreatedAt,
				Path:              c.Path,
				Line:              line,
				ThreadID:          t.ID,
				Resolved:          t.IsResolved,
				ThreadIsOutdated:  t.IsOutdated,
				IsMinimized:       c.IsMinimized,
				MinimizedReason:   c.MinimizedReason,
				OriginalCommitOID: originalCommitOID,
			})
		}
	}
	return out
}

// ciRunsFromGHNode flattens the last commit's statusCheckRollup into
// []api.CIRun. Both CheckRun (Actions/native) and StatusContext (old
// commit-status API) nodes are normalized to the same shape so the
// snapshot's rollupCI computation works unchanged.
//
// HeadSHA is set from the commit OID that owns the statusCheckRollup —
// this is the PR's current head commit, which is the SHA all the CI
// contexts in the rollup were evaluated against.
func ciRunsFromGHNode(n ghPRNode) []api.CIRun {
	// With commits(last: 20), nodes are in ascending chronological order;
	// the last element is the newest (head) commit where CI runs live.
	if len(n.Commits.Nodes) == 0 || n.Commits.Nodes[len(n.Commits.Nodes)-1].Commit.StatusCheckRollup == nil {
		return nil
	}
	headSHA := n.Commits.Nodes[len(n.Commits.Nodes)-1].Commit.OID
	rollup := n.Commits.Nodes[len(n.Commits.Nodes)-1].Commit.StatusCheckRollup
	out := make([]api.CIRun, 0, len(rollup.Contexts.Nodes))
	for _, c := range rollup.Contexts.Nodes {
		switch c.Typename {
		case "CheckRun":
			out = append(out, api.CIRun{
				ID:         c.ID,
				Name:       c.Name,
				Status:     strings.ToLower(c.Status),
				Conclusion: strings.ToLower(c.Conclusion),
				URL:        c.DetailsURL,
				Provider:   "github-actions",
				HeadSHA:    headSHA,
			})
		case "StatusContext":
			// StatusContext has no separate status/conclusion split —
			// `state` is the rolled-up outcome (SUCCESS/FAILURE/PENDING).
			// Map to the same conclusion-string vocabulary the rest of
			// pg-pr uses; "completed" stays consistent with CheckRun.
			out = append(out, api.CIRun{
				ID:         c.ID,
				Name:       c.Context,
				Status:     "completed",
				Conclusion: strings.ToLower(c.State),
				URL:        c.TargetURL,
				Provider:   "github-status",
				HeadSHA:    headSHA,
			})
		}
	}
	return out
}

// truncationFlags reports which embedded connections had hasNextPage=true.
// An empty result means everything fit; non-empty means the sync engine
// should fall back to REST for that PR if it needs complete data.
func truncationFlags(n ghPRNode) []string {
	var flags []string
	if n.Reviews.PageInfo.HasNextPage {
		flags = append(flags, "reviews")
	}
	if n.Comments.PageInfo.HasNextPage {
		flags = append(flags, "comments")
	}
	if n.ReviewThreads.PageInfo.HasNextPage {
		flags = append(flags, "reviewThreads")
	}
	for _, t := range n.ReviewThreads.Nodes {
		if t.Comments.PageInfo.HasNextPage {
			flags = append(flags, "threadComments")
			break
		}
	}
	if len(n.Commits.Nodes) > 0 && n.Commits.Nodes[len(n.Commits.Nodes)-1].Commit.StatusCheckRollup != nil &&
		n.Commits.Nodes[len(n.Commits.Nodes)-1].Commit.StatusCheckRollup.Contexts.PageInfo.HasNextPage {
		flags = append(flags, "ciContexts")
	}
	if n.Files.PageInfo.HasNextPage {
		flags = append(flags, "files")
	}
	if n.Commits.PageInfo.HasNextPage {
		flags = append(flags, "commits")
	}
	if n.Labels.PageInfo.HasNextPage {
		flags = append(flags, "labels")
	}
	return flags
}

// Compile-time check that *Provider satisfies vcs.EnrichedPRsProvider.
var _ vcs.EnrichedPRsProvider = (*Provider)(nil)

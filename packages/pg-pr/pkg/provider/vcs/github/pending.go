package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// pendingReviewByViewerQuery lists the PENDING (unsubmitted) reviews on a PR
// alongside the authenticated viewer's login. GitHub's REST `reviews` field
// (used by ListReviews via `gh pr view --json reviews`) surfaces only SUBMITTED
// reviews, so a PENDING review is invisible there — this GraphQL read is the
// only way to detect an existing draft review by the viewer (design §2.6, Q2).
var pendingReviewByViewerQuery = `query($owner: String!, $name: String!, $number: Int!) {
  viewer { login }
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviews(first: 50, states: [PENDING]) {
        nodes {
          author { login }
          state
        }
      }
    }
  }
}`

// ghPendingReviewResponse is the envelope for pendingReviewByViewerQuery.
type ghPendingReviewResponse struct {
	Data struct {
		Viewer struct {
			Login string `json:"login"`
		} `json:"viewer"`
		Repository struct {
			PullRequest struct {
				Reviews struct {
					Nodes []struct {
						Author struct {
							Login string `json:"login"`
						} `json:"author"`
						State string `json:"state"`
					} `json:"nodes"`
				} `json:"reviews"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
	Errors []ghGraphQLError `json:"errors"`
}

// HasPendingReviewByViewer reports whether the authenticated viewer already has
// a PENDING (unsubmitted) review on this PR.
//
// It runs the pendingReviewByViewerQuery GraphQL read and returns true iff some
// review node is authored by the viewer AND is in the PENDING state. The state
// check is defensive: the query already filters states:[PENDING], but re-asserting
// it here means a schema/filter regression can never turn a submitted review into
// a false skip.
//
// The teammate-PR sink (pg2-4c5i.35) calls this before posting so it never
// clobbers a pending review a human has started editing.
func (p *Provider) HasPendingReviewByViewer(ctx context.Context, repo string, number int) (bool, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return false, fmt.Errorf("github: invalid repo %q (want owner/name)", repo)
	}
	if number <= 0 {
		return false, fmt.Errorf("github: invalid PR number %d", number)
	}

	raw, err := p.gh.RunStdin(ctx, []byte(pendingReviewByViewerQuery),
		"api", "graphql",
		"-F", "owner="+owner,
		"-F", "name="+name,
		"-F", fmt.Sprintf("number=%d", number),
		"-F", "query=@-",
	)
	if err != nil {
		return false, fmt.Errorf("github: gh api graphql (pending reviews %s#%d): %w", repo, number, err)
	}

	var resp ghPendingReviewResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return false, fmt.Errorf("github: parse pending-review GraphQL response for %s#%d: %w", repo, number, err)
	}
	if len(resp.Errors) > 0 {
		return false, fmt.Errorf("github: GraphQL error for %s#%d: %s", repo, number, resp.Errors[0].Message)
	}

	viewer := resp.Data.Viewer.Login
	for _, r := range resp.Data.Repository.PullRequest.Reviews.Nodes {
		if r.State == "PENDING" && r.Author.Login == viewer && viewer != "" {
			return true, nil
		}
	}
	return false, nil
}

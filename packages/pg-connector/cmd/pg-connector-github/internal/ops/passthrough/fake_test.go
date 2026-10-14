package passthrough

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/api"
)

// fakeGH is a minimal GitHub double so these tests never spawn a gh subprocess.
// Unset fields answer empty; the viewer defaults to "me".
type fakeGH struct {
	// searchFn backs SearchPRs (the search op).
	searchFn func(ctx context.Context, query string) ([]api.PR, error)
	// searchActivityFn backs SearchPRsActivity (list_activity's searches).
	searchActivityFn func(ctx context.Context, query string, limit int) ([]api.PR, error)

	viewerLogin    string
	viewerLoginErr error

	// getPRFn answers GetPR per repo/number (list_activity's merge times).
	getPRFn func(ctx context.Context, repo string, number int) (*api.PR, error)
	// reviewsSubmittedFn backs ListReviewsSubmitted (the pr.reviewed kind).
	reviewsSubmittedFn func(ctx context.Context, repo string, number int) ([]api.Review, error)
	// commentsFn backs ListComments (the pr.commented kind).
	commentsFn func(ctx context.Context, repo string, number int) ([]api.Comment, error)
}

var _ GitHub = (*fakeGH)(nil)

// newBackend is the backend over gh.
func newBackend(gh GitHub) *backend { return &backend{gh: gh} }

func (f *fakeGH) SearchPRs(ctx context.Context, query string) ([]api.PR, error) {
	if f.searchFn != nil {
		return f.searchFn(ctx, query)
	}
	return nil, nil
}

func (f *fakeGH) SearchPRsActivity(ctx context.Context, query string, limit int) ([]api.PR, error) {
	if f.searchActivityFn != nil {
		return f.searchActivityFn(ctx, query, limit)
	}
	return nil, nil
}

func (f *fakeGH) ViewerLogin(context.Context) (string, error) {
	if f.viewerLoginErr != nil {
		return "", f.viewerLoginErr
	}
	if f.viewerLogin != "" {
		return f.viewerLogin, nil
	}
	return "me", nil
}

func (f *fakeGH) GetPR(ctx context.Context, repo string, number int) (*api.PR, error) {
	if f.getPRFn != nil {
		return f.getPRFn(ctx, repo, number)
	}
	return &api.PR{Repo: repo, Number: number}, nil
}

func (f *fakeGH) ListReviewsSubmitted(ctx context.Context, repo string, number int) ([]api.Review, error) {
	if f.reviewsSubmittedFn != nil {
		return f.reviewsSubmittedFn(ctx, repo, number)
	}
	return nil, nil
}

func (f *fakeGH) ListComments(ctx context.Context, repo string, number int) ([]api.Comment, error) {
	if f.commentsFn != nil {
		return f.commentsFn(ctx, repo, number)
	}
	return nil, nil
}

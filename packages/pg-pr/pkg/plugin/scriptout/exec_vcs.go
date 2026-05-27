package scriptout

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// execVCSProvider wraps a provider binary that speaks the scriptout
// protocol and exposes it as a vcs.Provider.
type execVCSProvider struct {
	binary string
}

// NewExecVCSProvider returns a vcs.Provider backed by the named binary.
// One exec per call; one JSON request/response per invocation.
func NewExecVCSProvider(binary string) vcs.Provider {
	return &execVCSProvider{binary: binary}
}

func (e *execVCSProvider) GetPR(ctx context.Context, repo string, number int) (*api.PR, error) {
	args := struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
	}{Repo: repo, Number: number}
	raw, err := invokeWithArgs(ctx, e.binary, OpGetPR, args)
	if err != nil {
		return nil, err
	}
	var out api.PR
	if err := unmarshalInto(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (e *execVCSProvider) ListMyPRs(ctx context.Context, repo string) ([]api.PR, error) {
	args := struct {
		Repo string `json:"repo"`
	}{Repo: repo}
	raw, err := invokeWithArgs(ctx, e.binary, OpListMyPRs, args)
	if err != nil {
		return nil, err
	}
	var out []api.PR
	if err := unmarshalInto(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (e *execVCSProvider) ListTeamPRs(ctx context.Context, repo string, members []string) ([]api.PR, error) {
	args := struct {
		Repo    string   `json:"repo"`
		Members []string `json:"members"`
	}{Repo: repo, Members: members}
	raw, err := invokeWithArgs(ctx, e.binary, OpListTeamPRs, args)
	if err != nil {
		return nil, err
	}
	var out []api.PR
	if err := unmarshalInto(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (e *execVCSProvider) CreatePR(ctx context.Context, repo string, draft bool, title, body, branch, base string, reviewers, labels []string) (*api.PR, error) {
	args := CreatePRArgs{
		Repo:      repo,
		Draft:     draft,
		Title:     title,
		Body:      body,
		Branch:    branch,
		Base:      base,
		Reviewers: reviewers,
		Labels:    labels,
	}
	raw, err := invokeWithArgs(ctx, e.binary, OpCreatePR, args)
	if err != nil {
		return nil, err
	}
	var out api.PR
	if err := unmarshalInto(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (e *execVCSProvider) UpdatePR(ctx context.Context, repo string, number int, body string) error {
	args := struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
		Body   string `json:"body"`
	}{Repo: repo, Number: number, Body: body}
	_, err := invokeWithArgs(ctx, e.binary, OpUpdatePR, args)
	return err
}

func (e *execVCSProvider) SetDraft(ctx context.Context, repo string, number int, draft bool) error {
	args := struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
		Draft  bool   `json:"draft"`
	}{Repo: repo, Number: number, Draft: draft}
	_, err := invokeWithArgs(ctx, e.binary, OpSetDraft, args)
	return err
}

func (e *execVCSProvider) SetAutomerge(ctx context.Context, repo string, number int, enabled bool) error {
	args := struct {
		Repo    string `json:"repo"`
		Number  int    `json:"number"`
		Enabled bool   `json:"enabled"`
	}{Repo: repo, Number: number, Enabled: enabled}
	_, err := invokeWithArgs(ctx, e.binary, OpSetAutomerge, args)
	return err
}

func (e *execVCSProvider) Merge(ctx context.Context, repo string, number int) error {
	args := struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
	}{Repo: repo, Number: number}
	_, err := invokeWithArgs(ctx, e.binary, OpMerge, args)
	return err
}

func (e *execVCSProvider) Close(ctx context.Context, repo string, number int) error {
	args := struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
	}{Repo: repo, Number: number}
	_, err := invokeWithArgs(ctx, e.binary, OpClose, args)
	return err
}

func (e *execVCSProvider) ListComments(ctx context.Context, repo string, number int) ([]api.Comment, error) {
	args := struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
	}{Repo: repo, Number: number}
	raw, err := invokeWithArgs(ctx, e.binary, OpListComments, args)
	if err != nil {
		return nil, err
	}
	var out []api.Comment
	if err := unmarshalInto(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (e *execVCSProvider) AddComment(ctx context.Context, repo string, number int, body string) (*api.Comment, error) {
	args := struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
		Body   string `json:"body"`
	}{Repo: repo, Number: number, Body: body}
	raw, err := invokeWithArgs(ctx, e.binary, OpAddComment, args)
	if err != nil {
		return nil, err
	}
	var out api.Comment
	if err := unmarshalInto(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (e *execVCSProvider) ReplyToThread(ctx context.Context, repo string, threadID, body string) (*api.Comment, error) {
	args := struct {
		Repo     string `json:"repo"`
		ThreadID string `json:"thread_id"`
		Body     string `json:"body"`
	}{Repo: repo, ThreadID: threadID, Body: body}
	raw, err := invokeWithArgs(ctx, e.binary, OpReplyToThread, args)
	if err != nil {
		return nil, err
	}
	var out api.Comment
	if err := unmarshalInto(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (e *execVCSProvider) ResolveThread(ctx context.Context, repo string, threadID string) error {
	args := struct {
		Repo     string `json:"repo"`
		ThreadID string `json:"thread_id"`
	}{Repo: repo, ThreadID: threadID}
	_, err := invokeWithArgs(ctx, e.binary, OpResolveThread, args)
	return err
}

func (e *execVCSProvider) PostReview(ctx context.Context, repo string, number int, body string, comments []api.Comment) (*api.Review, error) {
	args := PostReviewArgs{Repo: repo, Number: number, Body: body, Comments: comments}
	raw, err := invokeWithArgs(ctx, e.binary, OpPostReview, args)
	if err != nil {
		return nil, err
	}
	var out api.Review
	if err := unmarshalInto(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (e *execVCSProvider) ListReviews(ctx context.Context, repo string, number int) ([]api.Review, error) {
	args := struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
	}{Repo: repo, Number: number}
	raw, err := invokeWithArgs(ctx, e.binary, OpListReviews, args)
	if err != nil {
		return nil, err
	}
	var out []api.Review
	if err := unmarshalInto(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

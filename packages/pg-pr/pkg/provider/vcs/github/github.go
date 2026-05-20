// Package github is the builtin GitHub VCS provider for pg-pr.
//
// Phase 1 implements the read paths the sync engine depends on:
// GetPR, ListMyPRs, and ListTeamPRs. All shell out to the `gh` CLI for its
// authentication and rate-limit handling. The CLI invocation layer is
// abstracted by ghRunner so tests can inject canned JSON without spawning
// real subprocesses.
//
// The other interface methods remain `errStub` and will be implemented in
// later phases as the corresponding sync features land.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// Provider is the builtin GitHub VCS provider.
type Provider struct {
	gh ghRunner
}

// New constructs a GitHub VCS provider backed by the gh CLI on PATH.
func New() *Provider {
	return &Provider{gh: &cliGHRunner{}}
}

// NewWithRunner constructs a Provider with an injected ghRunner — used by
// tests to feed canned JSON.
func NewWithRunner(r ghRunner) *Provider {
	return &Provider{gh: r}
}

// ghRunner abstracts the `gh` CLI. Implementations return stdout bytes.
type ghRunner interface {
	Run(ctx context.Context, args ...string) (stdout []byte, err error)
	// RunStdin invokes gh with the given args while feeding stdin to the
	// subprocess. Used by write paths that POST JSON via `--input -`.
	RunStdin(ctx context.Context, stdin []byte, args ...string) (stdout []byte, err error)
}

// cliGHRunner is the production runner that invokes the real `gh` binary.
type cliGHRunner struct{}

func (cliGHRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	return cliGHRunner{}.RunStdin(ctx, nil, args...)
}

func (cliGHRunner) RunStdin(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.Bytes(), fmt.Errorf("gh %s: %w: %s",
				strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return stdout.Bytes(), fmt.Errorf("gh %s: %w (is gh on PATH?)",
			strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// errStub marks methods that are not implemented in Phase 1.
var errStub = errors.New("github vcs: not implemented (Phase 1 stub)")

// Common JSON field set requested from gh for PR-list endpoints.
var prListFields = "number,title,headRefName,baseRefName,url,author,isDraft,state,mergedAt,closedAt"

// ghPR is the JSON shape returned by `gh pr list/view --json prListFields`.
type ghPR struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	HeadRefName string `json:"headRefName"`
	BaseRefName string `json:"baseRefName"`
	URL         string `json:"url"`
	Author      struct {
		Login string `json:"login"`
		Name  string `json:"name"`
	} `json:"author"`
	IsDraft  bool   `json:"isDraft"`
	State    string `json:"state"`
	MergedAt string `json:"mergedAt"`
	ClosedAt string `json:"closedAt"`
}

func (p ghPR) toAPI(repo string) api.PR {
	return api.PR{
		Repo:   repo,
		Number: p.Number,
		State:  strings.ToLower(p.State),
		Branch: p.HeadRefName,
		Base:   p.BaseRefName,
		Author: p.Author.Login,
		URL:    p.URL,
		Draft:  p.IsDraft,
		Merged: p.MergedAt != "",
	}
}

// GetPR fetches a single PR's metadata.
func (p *Provider) GetPR(ctx context.Context, repo string, number int) (*api.PR, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if number <= 0 {
		return nil, fmt.Errorf("github: invalid PR number %d", number)
	}
	args := []string{
		"pr", "view", fmt.Sprintf("%d", number),
		"--repo", repo,
		"--json", prListFields,
	}
	raw, err := p.gh.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var pr ghPR
	if err := json.Unmarshal(raw, &pr); err != nil {
		return nil, fmt.Errorf("github: parse gh pr view JSON: %w", err)
	}
	out := pr.toAPI(repo)
	return &out, nil
}

// ListMyPRs returns open PRs authored by the configured self_login.
func (p *Provider) ListMyPRs(ctx context.Context, repo string) ([]api.PR, error) {
	return p.listForAuthor(ctx, repo, "@me")
}

// ListTeamPRs returns open PRs authored by any of the given members.
// Falls back to multiple `--author=<login>` invocations and merges by
// number; gh's `pr list` does not natively support OR on author.
func (p *Provider) ListTeamPRs(ctx context.Context, repo string, members []string) ([]api.PR, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	seen := map[int]struct{}{}
	out := make([]api.PR, 0)
	for _, m := range members {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		prs, err := p.listForAuthor(ctx, repo, m)
		if err != nil {
			return nil, fmt.Errorf("github: list team prs (author=%s): %w", m, err)
		}
		for _, pr := range prs {
			if _, dup := seen[pr.Number]; dup {
				continue
			}
			seen[pr.Number] = struct{}{}
			out = append(out, pr)
		}
	}
	return out, nil
}

// listForAuthor is the shared author-filter implementation.
func (p *Provider) listForAuthor(ctx context.Context, repo, author string) ([]api.PR, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	args := []string{
		"pr", "list",
		"--repo", repo,
		"--state", "open",
		"--author", author,
		"--json", prListFields,
		"--limit", "100",
	}
	raw, err := p.gh.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var prs []ghPR
	if err := json.Unmarshal(raw, &prs); err != nil {
		return nil, fmt.Errorf("github: parse gh pr list JSON: %w", err)
	}
	out := make([]api.PR, 0, len(prs))
	for _, p := range prs {
		out = append(out, p.toAPI(repo))
	}
	return out, nil
}

func validateRepo(repo string) error {
	if repo == "" {
		return errors.New("github: repo is required")
	}
	if !strings.Contains(repo, "/") {
		return fmt.Errorf("github: repo %q is not in owner/name form", repo)
	}
	return nil
}

// ---------------------------------------------------------------------
// Phase-deferred stubs (write paths + unused reads).
// ---------------------------------------------------------------------

func (p *Provider) CreatePR(context.Context, string, bool, string, string, string, string) (*api.PR, error) {
	return nil, errStub
}
func (p *Provider) UpdatePR(context.Context, string, int, string) error   { return errStub }
func (p *Provider) SetDraft(context.Context, string, int, bool) error     { return errStub }
func (p *Provider) SetAutomerge(context.Context, string, int, bool) error { return errStub }
func (p *Provider) Merge(context.Context, string, int) error              { return errStub }
func (p *Provider) Close(context.Context, string, int) error              { return errStub }

// ghIssueComment is the JSON shape returned by the issue-comments endpoint
// (top-level PR comments).
type ghIssueComment struct {
	NodeID string `json:"node_id"`
	Body   string `json:"body"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
	AuthorAssociation string `json:"author_association"`
}

// ghReviewComment is the JSON shape returned by the pulls comments endpoint
// (review-thread / inline file comments).
type ghReviewComment struct {
	NodeID string `json:"node_id"`
	Body   string `json:"body"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
	Path                string `json:"path"`
	Line                int    `json:"line"`
	OriginalLine        int    `json:"original_line"`
	PullRequestReviewID int64  `json:"pull_request_review_id"`
	InReplyToID         int64  `json:"in_reply_to_id"`
	AuthorAssociation   string `json:"author_association"`
}

// ListComments returns all PR comments (top-level + inline review-thread).
// Top-level (issue) comments are tagged with empty Path/Line; inline
// comments carry their Path / Line / ThreadID.
func (p *Provider) ListComments(ctx context.Context, repo string, number int) ([]api.Comment, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if number <= 0 {
		return nil, fmt.Errorf("github: invalid PR number %d", number)
	}

	out := make([]api.Comment, 0)

	// 1. Top-level PR comments (the "issue" comment endpoint).
	issueRaw, err := p.gh.Run(ctx,
		"api",
		fmt.Sprintf("repos/%s/issues/%d/comments", repo, number),
		"--paginate",
	)
	if err != nil {
		return nil, fmt.Errorf("github: list issue comments: %w", err)
	}
	if len(bytes.TrimSpace(issueRaw)) > 0 {
		var ics []ghIssueComment
		if err := json.Unmarshal(issueRaw, &ics); err != nil {
			return nil, fmt.Errorf("github: parse issue-comments JSON: %w", err)
		}
		for _, c := range ics {
			out = append(out, api.Comment{
				ID:         c.NodeID,
				Author:     c.User.Login,
				AuthorRole: strings.ToLower(c.AuthorAssociation),
				Body:       c.Body,
			})
		}
	}

	// 2. Inline / review-thread comments (the "pulls comments" endpoint).
	reviewRaw, err := p.gh.Run(ctx,
		"api",
		fmt.Sprintf("repos/%s/pulls/%d/comments", repo, number),
		"--paginate",
	)
	if err != nil {
		return nil, fmt.Errorf("github: list review comments: %w", err)
	}
	if len(bytes.TrimSpace(reviewRaw)) > 0 {
		var rcs []ghReviewComment
		if err := json.Unmarshal(reviewRaw, &rcs); err != nil {
			return nil, fmt.Errorf("github: parse review-comments JSON: %w", err)
		}
		for _, c := range rcs {
			line := c.Line
			if line == 0 {
				line = c.OriginalLine
			}
			// ThreadID: NodeID for the root comment of a thread; for replies,
			// gh exposes only `in_reply_to_id` (numeric). We use the NodeID
			// uniformly — Phase 3 will refine when resolveThread mutation
			// requires the actual review_thread node id.
			out = append(out, api.Comment{
				ID:         c.NodeID,
				Author:     c.User.Login,
				AuthorRole: strings.ToLower(c.AuthorAssociation),
				Body:       c.Body,
				Path:       c.Path,
				Line:       line,
				ThreadID:   c.NodeID,
			})
		}
	}

	return out, nil
}

// AddComment posts a top-level PR comment via the gh CLI.
//
// Phase 2: minimal implementation using the `repos/.../issues/<n>/comments`
// REST endpoint. The returned api.Comment only carries the new comment's
// NodeID and Body; richer fields land in Phase 3.
func (p *Provider) AddComment(ctx context.Context, repo string, number int, body string) (*api.Comment, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if number <= 0 {
		return nil, fmt.Errorf("github: invalid PR number %d", number)
	}
	if strings.TrimSpace(body) == "" {
		return nil, errors.New("github: comment body is empty")
	}
	raw, err := p.gh.Run(ctx,
		"api",
		fmt.Sprintf("repos/%s/issues/%d/comments", repo, number),
		"--method", "POST",
		"-f", fmt.Sprintf("body=%s", body),
	)
	if err != nil {
		return nil, fmt.Errorf("github: add comment: %w", err)
	}
	var c ghIssueComment
	if err := json.Unmarshal(raw, &c); err != nil {
		// Some gh versions return empty stdout on success; treat as
		// fire-and-forget rather than a hard error.
		return &api.Comment{Body: body}, nil
	}
	return &api.Comment{
		ID:     c.NodeID,
		Author: c.User.Login,
		Body:   c.Body,
	}, nil
}

// ReplyToThread is deferred to Phase 3. The GraphQL `addPullRequestReviewThreadReply`
// mutation requires the review_thread node id, which our Phase 2 ListComments does
// not yet plumb through. Returning ErrNotImplemented keeps the surface honest.
func (p *Provider) ReplyToThread(context.Context, string, string, string) (*api.Comment, error) {
	return nil, fmt.Errorf("github: ReplyToThread: %w (lands in Phase 3 with the GraphQL thread plumbing)", vcs.ErrNotImplemented)
}

// ResolveThread is deferred to Phase 3 for the same reason as ReplyToThread:
// the GraphQL `resolveReviewThread` mutation needs the review_thread node id.
func (p *Provider) ResolveThread(context.Context, string, string) error {
	return fmt.Errorf("github: ResolveThread: %w (lands in Phase 3 with the GraphQL thread plumbing)", vcs.ErrNotImplemented)
}

// reviewComment is the on-wire shape sent inside POST /reviews's `comments[]`.
type reviewComment struct {
	Path        string `json:"path"`
	Body        string `json:"body"`
	Line        int    `json:"line,omitempty"`
	StartLine   int    `json:"start_line,omitempty"`
	Side        string `json:"side,omitempty"`
	SubjectType string `json:"subject_type,omitempty"`
}

// PostReview creates a pending PR review with optional comments.
//
// The wire format mirrors GitHub's review-create REST endpoint
// (`POST repos/.../pulls/<n>/reviews`). `event` is left unspecified so the
// review is created in PENDING state — agents/humans submit explicitly.
//
// Phase 2: comments without a Path become PR-level (subject_type=file when
// Path is set + Line empty; PR-level when Path empty). The caller already
// dedups against existing review-comments before calling.
func (p *Provider) PostReview(ctx context.Context, repo string, number int, body string, comments []api.Comment) (*api.Review, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if number <= 0 {
		return nil, fmt.Errorf("github: invalid PR number %d", number)
	}

	rcs := make([]reviewComment, 0, len(comments))
	for _, c := range comments {
		if c.Path == "" {
			// PR-level: fold into review body below.
			if body != "" {
				body += "\n\n"
			}
			body += c.Body
			continue
		}
		rc := reviewComment{Path: c.Path, Body: c.Body}
		if c.Line > 0 {
			rc.Line = c.Line
			rc.Side = "RIGHT"
		} else {
			rc.SubjectType = "file"
		}
		rcs = append(rcs, rc)
	}

	payload := map[string]any{}
	if body != "" {
		payload["body"] = body
	}
	if len(rcs) > 0 {
		payload["comments"] = rcs
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("github: marshal review payload: %w", err)
	}

	raw, err := p.gh.RunStdin(ctx, payloadJSON,
		"api",
		fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, number),
		"--method", "POST",
		"--input", "-",
	)
	if err != nil {
		return nil, fmt.Errorf("github: post review: %w", err)
	}
	var resp struct {
		NodeID string `json:"node_id"`
		State  string `json:"state"`
		Body   string `json:"body"`
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &resp)
	}
	return &api.Review{
		ID:    resp.NodeID,
		State: strings.ToLower(resp.State),
		Body:  resp.Body,
	}, nil
}

// Compile-time check that Provider satisfies vcs.Provider.
var _ vcs.Provider = (*Provider)(nil)

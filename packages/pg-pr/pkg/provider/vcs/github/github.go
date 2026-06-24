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
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// Provider is the builtin GitHub VCS provider.
type Provider struct {
	gh ghRunner
}

// New constructs a GitHub VCS provider backed by the gh CLI on PATH.
func New() *Provider {
	return &Provider{gh: &cliGHRunner{src: defaultTokenSource()}}
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

// cliGHRunner is the production runner that invokes the real `gh` binary. It
// resolves a GitHub token once (lazily, success-cached) via its TokenSource and
// injects GH_TOKEN into every child env so gh never reads the macOS keychain at
// runtime — fixing intermittent 401s from concurrent keychain reads under the
// launchd agent.
type cliGHRunner struct {
	src  TokenSource
	mu   sync.Mutex
	tok  string
	have bool
}

// token returns the resolved token, resolving (and caching) it at most once.
// Failures are NOT cached so a transient resolution error can be retried.
func (r *cliGHRunner) token(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.have {
		return r.tok, nil
	}
	t, err := r.src.Token(ctx)
	if err != nil {
		return "", err // do NOT cache failure
	}
	r.tok, r.have = t, true
	return t, nil
}

func (r *cliGHRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	return r.RunStdin(ctx, nil, args...)
}

func (r *cliGHRunner) RunStdin(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	tok, err := r.token(ctx)
	if err != nil {
		return nil, fmt.Errorf("gh %s: %w", strings.Join(args, " "), errors.Join(ErrGHAuthInvalid, err))
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Env = envWithGHToken(os.Environ(), tok)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			st := strings.TrimSpace(stderr.String())
			if isAuthFailure(exitErr.ExitCode(), st) {
				return stdout.Bytes(), fmt.Errorf("gh %s: %s: %w", strings.Join(args, " "), st, ErrGHAuthInvalid)
			}
			return stdout.Bytes(), fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, st)
		}
		return stdout.Bytes(), fmt.Errorf("gh %s: %w (is gh on PATH?)", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// errStub marks methods that are not implemented in Phase 1.
//
// Phase 3 retired this — all Provider methods are now wired. Kept here as a
// sentinel so old tests asserting against it still compile until they are
// rewritten.
var errStub = errors.New("github vcs: not implemented")

// Common JSON field set requested from gh for PR-list endpoints.
var prListFields = "number,title,headRefName,headRefOid,baseRefName,url,author,isDraft,state,mergedAt,closedAt,additions,deletions,changedFiles,body,labels"

// ghPR is the JSON shape returned by `gh pr list/view --json prListFields`.
type ghPR struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	HeadRefName string `json:"headRefName"`
	HeadRefOid  string `json:"headRefOid"`
	BaseRefName string `json:"baseRefName"`
	URL         string `json:"url"`
	Author      struct {
		Login string `json:"login"`
		Name  string `json:"name"`
	} `json:"author"`
	IsDraft      bool   `json:"isDraft"`
	State        string `json:"state"`
	MergedAt     string `json:"mergedAt"`
	ClosedAt     string `json:"closedAt"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
	ChangedFiles int    `json:"changedFiles"`
	Body         string `json:"body"`
	Labels       []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (p ghPR) toAPI(repo string) api.PR {
	out := api.PR{
		Repo:         repo,
		Number:       p.Number,
		Title:        p.Title,
		State:        strings.ToLower(p.State),
		Branch:       p.HeadRefName,
		Base:         p.BaseRefName,
		Author:       p.Author.Login,
		URL:          p.URL,
		Draft:        p.IsDraft,
		Merged:       p.MergedAt != "",
		Additions:    p.Additions,
		Deletions:    p.Deletions,
		ChangedFiles: p.ChangedFiles,
		HeadSHA:      p.HeadRefOid,
		Body:         p.Body,
	}
	for _, l := range p.Labels {
		out.Labels = append(out.Labels, l.Name)
	}
	return out
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
// Write paths (Phase 3).
// ---------------------------------------------------------------------

// CreatePR opens a new pull request via `gh pr create`. The body is fed on
// stdin (via `--body-file -`) so multi-line bodies work without quoting
// headaches. Returns the freshly created PR shape. reviewers and labels
// are pushed via gh's `--reviewer` and `--label` flags (gh accepts each
// repeated for multiple values).
func (p *Provider) CreatePR(ctx context.Context, repo string, draft bool, title, body, branch, base string, reviewers, labels []string) (*api.PR, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if strings.TrimSpace(title) == "" {
		return nil, errors.New("github: PR title is required")
	}
	if strings.TrimSpace(branch) == "" {
		return nil, errors.New("github: PR head branch is required")
	}
	if strings.TrimSpace(base) == "" {
		return nil, errors.New("github: PR base branch is required")
	}
	args := []string{
		"pr", "create",
		"--repo", repo,
		"--title", title,
		"--body-file", "-",
		"--head", branch,
		"--base", base,
	}
	if draft {
		args = append(args, "--draft")
	}
	for _, r := range reviewers {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		args = append(args, "--reviewer", r)
	}
	for _, l := range labels {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		args = append(args, "--label", l)
	}
	out, err := p.gh.RunStdin(ctx, []byte(body), args...)
	if err != nil {
		return nil, fmt.Errorf("github: create PR: %w", err)
	}
	// `gh pr create` prints the new PR URL on stdout. Parse the trailing
	// number out of it; fall back to a follow-up `pr view` if parsing fails.
	url := strings.TrimSpace(string(out))
	num, perr := parsePRNumberFromURL(url)
	if perr != nil || num == 0 {
		// As a fallback, try to discover the most recent PR for the branch.
		return p.lookupPRByBranch(ctx, repo, branch, url)
	}
	pr, err := p.GetPR(ctx, repo, num)
	if err != nil {
		// Return what we know if GetPR fails.
		return &api.PR{Repo: repo, Number: num, URL: url, Branch: branch, Base: base, Draft: draft}, nil
	}
	return pr, nil
}

// parsePRNumberFromURL extracts the trailing /pull/<n> number from a gh PR
// URL. Returns (0, error) when the URL is empty or doesn't match.
func parsePRNumberFromURL(url string) (int, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return 0, errors.New("empty url")
	}
	idx := strings.LastIndex(url, "/")
	if idx < 0 || idx == len(url)-1 {
		return 0, fmt.Errorf("unrecognized PR URL %q", url)
	}
	var n int
	if _, err := fmt.Sscanf(url[idx+1:], "%d", &n); err != nil {
		return 0, err
	}
	return n, nil
}

// lookupPRByBranch is the fallback used when parsing a PR number from the
// `gh pr create` stdout fails.
func (p *Provider) lookupPRByBranch(ctx context.Context, repo, branch, url string) (*api.PR, error) {
	args := []string{
		"pr", "list",
		"--repo", repo,
		"--head", branch,
		"--state", "open",
		"--json", prListFields,
		"--limit", "1",
	}
	raw, err := p.gh.Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("github: lookup created PR: %w", err)
	}
	var prs []ghPR
	if err := json.Unmarshal(raw, &prs); err != nil {
		return nil, fmt.Errorf("github: parse pr list JSON: %w", err)
	}
	if len(prs) == 0 {
		return &api.PR{Repo: repo, URL: url, Branch: branch}, nil
	}
	out := prs[0].toAPI(repo)
	return &out, nil
}

// UpdatePR edits the PR body via `gh pr edit --body-file -`.
func (p *Provider) UpdatePR(ctx context.Context, repo string, number int, body string) error {
	if err := validateRepo(repo); err != nil {
		return err
	}
	if number <= 0 {
		return fmt.Errorf("github: invalid PR number %d", number)
	}
	_, err := p.gh.RunStdin(ctx, []byte(body),
		"pr", "edit", fmt.Sprintf("%d", number),
		"--repo", repo,
		"--body-file", "-",
	)
	if err != nil {
		return fmt.Errorf("github: update PR: %w", err)
	}
	return nil
}

// SetDraft toggles a PR's draft state via `gh pr ready` (mark ready) or
// `gh pr ready --undo` (convert back to draft).
func (p *Provider) SetDraft(ctx context.Context, repo string, number int, draft bool) error {
	if err := validateRepo(repo); err != nil {
		return err
	}
	if number <= 0 {
		return fmt.Errorf("github: invalid PR number %d", number)
	}
	args := []string{
		"pr", "ready", fmt.Sprintf("%d", number),
		"--repo", repo,
	}
	if draft {
		args = append(args, "--undo")
	}
	if _, err := p.gh.Run(ctx, args...); err != nil {
		return fmt.Errorf("github: set draft=%v: %w", draft, err)
	}
	return nil
}

// SetAutomerge enables or disables PR automerge via `gh pr merge --auto` or
// `gh pr merge --disable-auto`.
func (p *Provider) SetAutomerge(ctx context.Context, repo string, number int, enabled bool) error {
	if err := validateRepo(repo); err != nil {
		return err
	}
	if number <= 0 {
		return fmt.Errorf("github: invalid PR number %d", number)
	}
	args := []string{
		"pr", "merge", fmt.Sprintf("%d", number),
		"--repo", repo,
	}
	if enabled {
		args = append(args, "--auto", "--squash")
	} else {
		args = append(args, "--disable-auto")
	}
	if _, err := p.gh.Run(ctx, args...); err != nil {
		return fmt.Errorf("github: set automerge=%v: %w", enabled, err)
	}
	return nil
}

// Merge merges the PR immediately. Phase 3 defaults to squash; a future
// phase can plumb the strategy through config.
func (p *Provider) Merge(ctx context.Context, repo string, number int) error {
	if err := validateRepo(repo); err != nil {
		return err
	}
	if number <= 0 {
		return fmt.Errorf("github: invalid PR number %d", number)
	}
	if _, err := p.gh.Run(ctx,
		"pr", "merge", fmt.Sprintf("%d", number),
		"--repo", repo,
		"--squash",
	); err != nil {
		return fmt.Errorf("github: merge PR: %w", err)
	}
	return nil
}

// Close closes a PR without merging via `gh pr close`.
func (p *Provider) Close(ctx context.Context, repo string, number int) error {
	if err := validateRepo(repo); err != nil {
		return err
	}
	if number <= 0 {
		return fmt.Errorf("github: invalid PR number %d", number)
	}
	if _, err := p.gh.Run(ctx,
		"pr", "close", fmt.Sprintf("%d", number),
		"--repo", repo,
	); err != nil {
		return fmt.Errorf("github: close PR: %w", err)
	}
	return nil
}

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

// addPullRequestReviewThreadReplyMutation is the GraphQL mutation used by
// ReplyToThread. The thread node id is fed in as a -F field; the reply body
// is passed via stdin (-F body=@-).
const addPullRequestReviewThreadReplyMutation = `
mutation($threadId: ID!, $body: String!) {
  addPullRequestReviewThreadReply(input: {pullRequestReviewThreadId: $threadId, body: $body}) {
    comment {
      id
      body
      author { login }
    }
  }
}
`

// resolveReviewThreadMutation marks a review thread as resolved.
const resolveReviewThreadMutation = `
mutation($threadId: ID!) {
  resolveReviewThread(input: {threadId: $threadId}) {
    thread { id isResolved }
  }
}
`

// ReplyToThread posts a reply on an existing review thread via GraphQL.
// threadID is the GitHub review-thread node id (PRRT_…).
func (p *Provider) ReplyToThread(ctx context.Context, repo, threadID, body string) (*api.Comment, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if strings.TrimSpace(threadID) == "" {
		return nil, errors.New("github: thread id is required")
	}
	if strings.TrimSpace(body) == "" {
		return nil, errors.New("github: reply body is empty")
	}
	args := []string{
		"api", "graphql",
		"-F", "query=" + addPullRequestReviewThreadReplyMutation,
		"-F", "threadId=" + threadID,
		"-F", "body=" + body,
	}
	raw, err := p.gh.Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("github: reply to thread: %w", err)
	}
	var resp struct {
		Data struct {
			AddPullRequestReviewThreadReply struct {
				Comment struct {
					ID     string `json:"id"`
					Body   string `json:"body"`
					Author struct {
						Login string `json:"login"`
					} `json:"author"`
				} `json:"comment"`
			} `json:"addPullRequestReviewThreadReply"`
		} `json:"data"`
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &resp)
	}
	c := resp.Data.AddPullRequestReviewThreadReply.Comment
	return &api.Comment{
		ID:       c.ID,
		Author:   c.Author.Login,
		Body:     c.Body,
		ThreadID: threadID,
	}, nil
}

// minimizeCommentMutation hides a comment with the given classifier
// (OUTDATED|RESOLVED|OFF_TOPIC|SPAM|ABUSE|DUPLICATE). Mirrors
// resolveReviewThreadMutation.
const minimizeCommentMutation = `
mutation($id: ID!, $classifier: ReportedContentClassifiers!) {
  minimizeComment(input: {subjectId: $id, classifier: $classifier}) {
    minimizedComment { isMinimized }
  }
}
`

// MinimizeComment marks a comment minimized with the given classifier. nodeID is
// the comment's GraphQL node id.
func (p *Provider) MinimizeComment(ctx context.Context, nodeID, classifier string) error {
	args := []string{
		"api", "graphql",
		"-F", "query=" + minimizeCommentMutation,
		"-f", "id=" + nodeID,
		"-f", "classifier=" + classifier,
	}
	if _, err := p.gh.Run(ctx, args...); err != nil {
		return fmt.Errorf("github: minimize comment: %w", err)
	}
	return nil
}

// ResolveThread marks a review thread as resolved.
func (p *Provider) ResolveThread(ctx context.Context, repo, threadID string) error {
	if err := validateRepo(repo); err != nil {
		return err
	}
	if strings.TrimSpace(threadID) == "" {
		return errors.New("github: thread id is required")
	}
	args := []string{
		"api", "graphql",
		"-F", "query=" + resolveReviewThreadMutation,
		"-F", "threadId=" + threadID,
	}
	if _, err := p.gh.Run(ctx, args...); err != nil {
		return fmt.Errorf("github: resolve thread: %w", err)
	}
	return nil
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

// ghReviewEntry is the JSON shape of each element in the `reviews` array
// returned by `gh pr view --json reviews`.
type ghReviewEntry struct {
	ID     int64 `json:"id"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	State string `json:"state"`
	Body  string `json:"body"`
}

// ListReviews fetches the review summaries for a PR. State is one of
// APPROVED, CHANGES_REQUESTED, COMMENTED. Body is the review-summary text;
// Comments is left empty — inline comments are fetched via ListComments.
func (p *Provider) ListReviews(ctx context.Context, repo string, number int) ([]api.Review, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if number <= 0 {
		return nil, fmt.Errorf("github: invalid PR number %d", number)
	}
	raw, err := p.gh.Run(ctx,
		"pr", "view", fmt.Sprintf("%d", number),
		"--repo", repo,
		"--json", "reviews",
	)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Reviews []ghReviewEntry `json:"reviews"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("github: parse gh pr view reviews JSON: %w", err)
	}
	out := make([]api.Review, 0, len(envelope.Reviews))
	for _, r := range envelope.Reviews {
		out = append(out, api.Review{
			ID:     fmt.Sprintf("%d", r.ID),
			Author: r.Author.Login,
			State:  r.State,
			Body:   r.Body,
		})
	}
	return out, nil
}

// CheckAuth verifies the resolved token works with one cheap authenticated
// GraphQL call. errors.Is(err, ErrGHAuthInvalid) distinguishes a bad token
// from a transient/network failure.
func (p *Provider) CheckAuth(ctx context.Context) error {
	_, err := p.gh.Run(ctx, "api", "graphql", "-f", "query={ viewer { login } }")
	return err
}

// Compile-time check that Provider satisfies vcs.Provider.
var _ vcs.Provider = (*Provider)(nil)

// Compile-time check that Provider satisfies vcs.AuthChecker.
var _ vcs.AuthChecker = (*Provider)(nil)

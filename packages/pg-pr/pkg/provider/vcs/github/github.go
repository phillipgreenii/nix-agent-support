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
}

// cliGHRunner is the production runner that invokes the real `gh` binary.
type cliGHRunner struct{}

func (cliGHRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
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
func (p *Provider) ListComments(context.Context, string, int) ([]api.Comment, error) {
	return nil, errStub
}
func (p *Provider) AddComment(context.Context, string, int, string) (*api.Comment, error) {
	return nil, errStub
}
func (p *Provider) ReplyToThread(context.Context, string, string, string) (*api.Comment, error) {
	return nil, errStub
}
func (p *Provider) ResolveThread(context.Context, string, string) error { return errStub }
func (p *Provider) PostReview(context.Context, string, int, string, []api.Comment) (*api.Review, error) {
	return nil, errStub
}

// Compile-time check that Provider satisfies vcs.Provider.
var _ vcs.Provider = (*Provider)(nil)

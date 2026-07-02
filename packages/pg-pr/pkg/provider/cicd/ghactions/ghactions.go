// Package ghactions is the builtin GitHub Actions CICD provider for pg-pr.
//
// Phase 3 implementation: ListRuns/GetLogs/RerunFailed shell out to the
// `gh` CLI for its authentication and rate-limit handling. The CLI layer
// is abstracted by ghRunner so tests can inject canned JSON without
// spawning real subprocesses.
//
// ListRuns is PR-scoped. Because `gh run list` filters by branch (not PR),
// the provider needs a way to resolve PR # → head branch. The PRResolver
// dependency is injectable; production wiring passes the github VCS
// provider.
package ghactions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/cicd"
)

// ProviderName is the registered name of the github-actions provider.
const ProviderName = "github-actions"

// PRResolver resolves a PR number to its head branch. The github VCS
// provider satisfies this via GetPR; tests may inject a stub.
type PRResolver interface {
	PRHeadBranch(ctx context.Context, repo string, number int) (string, error)
}

// ghRunner abstracts the gh CLI for tests.
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

// Provider is the builtin github-actions CICD provider.
type Provider struct {
	gh ghRunner
	pr PRResolver
}

// New constructs a Provider backed by the gh CLI on PATH. The PRResolver
// is nil here; callers that need PR-scoped ListRuns must use NewWithDeps.
func New() *Provider {
	return &Provider{gh: cliGHRunner{}}
}

// NewWithDeps constructs a Provider with injected dependencies (used in
// tests and to wire the github VCS provider as the PRResolver in
// production).
func NewWithDeps(gh ghRunner, pr PRResolver) *Provider {
	if gh == nil {
		gh = cliGHRunner{}
	}
	return &Provider{gh: gh, pr: pr}
}

// SetPRResolver installs a PRResolver after construction. Used by the CLI
// wiring to break the circular dependency between cicd and vcs providers.
func (p *Provider) SetPRResolver(pr PRResolver) {
	p.pr = pr
}

// ghRun is the JSON shape returned by `gh run list --json …`.
type ghRun struct {
	DatabaseID int64  `json:"databaseId"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
	HeadBranch string `json:"headBranch"`
	HeadSHA    string `json:"headSha"`
}

func (r ghRun) toAPI() api.CIRun {
	return api.CIRun{
		ID:         fmt.Sprintf("%d", r.DatabaseID),
		Name:       r.Name,
		Status:     strings.ToLower(r.Status),
		Conclusion: strings.ToLower(r.Conclusion),
		URL:        r.URL,
		Provider:   ProviderName,
		HeadSHA:    r.HeadSHA,
	}
}

// runListFields is the JSON projection requested from gh.
const runListFields = "databaseId,name,status,conclusion,url,headBranch,headSha"

// ListRuns enumerates workflow runs for the PR's head branch.
//
// gh's `run list` does not natively filter by PR, so we resolve PR # →
// head branch via the PRResolver and delegate to ListRunsByBranch.
// Callers that already know the head branch should call ListRunsByBranch
// directly to avoid the extra `gh pr view` round-trip.
func (p *Provider) ListRuns(ctx context.Context, repo string, prNumber int) ([]api.CIRun, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if prNumber <= 0 {
		return nil, fmt.Errorf("github-actions: invalid PR number %d", prNumber)
	}
	if p.pr == nil {
		return nil, errors.New("github-actions: PRResolver not set; call SetPRResolver before ListRuns")
	}
	branch, err := p.pr.PRHeadBranch(ctx, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("github-actions: resolve PR head branch: %w", err)
	}
	if branch == "" {
		return nil, fmt.Errorf("github-actions: empty head branch for %s#%d", repo, prNumber)
	}
	return p.ListRunsByBranch(ctx, repo, branch)
}

// ListRunsByBranch enumerates workflow runs for a known head branch.
// Skips the PRResolver hop entirely; callers (e.g. the sync daemon, which
// has the head branch from `gh pr list --json headRefName`) should prefer
// this over ListRuns to halve their gh call count.
func (p *Provider) ListRunsByBranch(ctx context.Context, repo, branch string) ([]api.CIRun, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if strings.TrimSpace(branch) == "" {
		return nil, fmt.Errorf("github-actions: branch is required")
	}
	raw, err := p.gh.Run(
		ctx,
		"run", "list",
		"--repo", repo,
		"--branch", branch,
		"--json", runListFields,
		"--limit", "100",
	)
	if err != nil {
		return nil, fmt.Errorf("github-actions: list runs: %w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var runs []ghRun
	if err := json.Unmarshal(raw, &runs); err != nil {
		return nil, fmt.Errorf("github-actions: parse runs JSON: %w", err)
	}
	out := make([]api.CIRun, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.toAPI())
	}
	return out, nil
}

// GetLogs fetches the run's log bundle as a plain byte slice.
func (p *Provider) GetLogs(ctx context.Context, runID string) ([]byte, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, errors.New("github-actions: run ID is required")
	}
	raw, err := p.gh.Run(ctx, "run", "view", runID, "--log")
	if err != nil {
		return nil, fmt.Errorf("github-actions: get logs: %w", err)
	}
	return raw, nil
}

// RerunFailed rer-uns the latest failed workflow run for a PR. We pick the
// most recent run with conclusion=failure across the PR's head branch and
// invoke `gh run rerun <id> --failed`.
func (p *Provider) RerunFailed(ctx context.Context, repo string, prNumber int) error {
	runs, err := p.ListRuns(ctx, repo, prNumber)
	if err != nil {
		return err
	}
	// ListRuns is most-recent-first per gh's default ordering.
	var target string
	for _, r := range runs {
		if r.Conclusion == "failure" {
			target = r.ID
			break
		}
	}
	if target == "" {
		return fmt.Errorf("github-actions: no failed runs to rerun for %s#%d", repo, prNumber)
	}
	if _, err := p.gh.Run(ctx, "run", "rerun", target, "--failed"); err != nil {
		return fmt.Errorf("github-actions: rerun failed: %w", err)
	}
	return nil
}

func validateRepo(repo string) error {
	if repo == "" {
		return errors.New("github-actions: repo is required")
	}
	if !strings.Contains(repo, "/") {
		return fmt.Errorf("github-actions: repo %q is not in owner/name form", repo)
	}
	return nil
}

var _ cicd.Provider = (*Provider)(nil)

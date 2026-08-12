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
//
// This package imports the github VCS provider package for ONE thing: its
// token-protected gh gateway (github.CLI), so this provider cannot exec an
// unauthenticated gh. That is a compile-time dependency on the gateway only —
// the PROVIDER relationship stays inverted through the injectable PRResolver
// (SetPRResolver below), and pkg/provider/vcs/github imports nothing from
// pkg/provider/cicd, so there is no import cycle.
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
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs/github"
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

// ghCommander builds a token-protected `gh` command. *github.CLI is the
// production implementation; tests inject a fake. The interface is declared
// here (rather than imported) so this package depends only on the one method it
// needs.
type ghCommander interface {
	Command(ctx context.Context, args ...string) (*exec.Cmd, error)
}

// cliGHRunner is the production runner that invokes the real `gh` binary. It
// never execs gh itself: the command is built by a token-protected commander
// (bead pg2-ilzq9), so a missing/expired credential fails fast with an
// ErrGHAuthInvalid-wrapped error instead of letting an unauthenticated gh open
// its own interactive login.
type cliGHRunner struct{ gh ghCommander }

// defaultGHCommander is the shared gateway used when no commander is injected.
// Construction does no I/O; the token resolves lazily and is cached.
var defaultGHCommander ghCommander = github.NewCLI()

// commander returns the injected commander, defaulting to the shared
// token-protected gateway so a zero-value runner is still safe.
func (r cliGHRunner) commander() ghCommander {
	if r.gh != nil {
		return r.gh
	}
	return defaultGHCommander
}

func (r cliGHRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	cmd, err := r.commander().Command(ctx, args...)
	if err != nil {
		// Token resolution failed: no process was created, so gh was never
		// executed. The error already wraps ErrGHAuthInvalid and names
		// `gh auth login`.
		return nil, fmt.Errorf("github-actions: %w", err)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			st := strings.TrimSpace(stderr.String())
			if github.IsAuthFailure(exitErr.ExitCode(), st) {
				return stdout.Bytes(), fmt.Errorf("gh %s: %s: run `gh auth login`: %w",
					strings.Join(args, " "), st, github.ErrGHAuthInvalid)
			}
			return stdout.Bytes(), fmt.Errorf("gh %s: %w: %s",
				strings.Join(args, " "), err, st)
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
	return &Provider{gh: cliGHRunner{gh: github.NewCLI()}}
}

// NewWithDeps constructs a Provider with injected dependencies (used in
// tests and to wire the github VCS provider as the PRResolver in
// production).
func NewWithDeps(gh ghRunner, pr PRResolver) *Provider {
	if gh == nil {
		gh = cliGHRunner{gh: github.NewCLI()}
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

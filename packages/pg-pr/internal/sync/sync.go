// Package sync is the pg-pr sync engine.
//
// Phase 1 scope: enumerate watched PRs per repo (configured self + team
// members), upsert one merge-request bead per PR, and close merge-request
// beads whose upstream PR is no longer in the watched set. No feedback or
// processing-cycle beads in this phase — those land in Phase 3.
//
// The engine is dependency-injected via the Deps struct so callers (tests,
// CLI wiring) can compose their own VCS providers and bd clients.
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// VCSProvider is the subset of pkg/provider/vcs.Provider the sync engine
// uses. Defined locally so the engine can be tested without importing the
// full interface.
type VCSProvider interface {
	GetPR(ctx context.Context, repo string, number int) (*api.PR, error)
	ListMyPRs(ctx context.Context, repo string) ([]api.PR, error)
	ListTeamPRs(ctx context.Context, repo string, members []string) ([]api.PR, error)
}

// BeadClient is the subset of *pkg/beads.Client the sync engine uses.
type BeadClient interface {
	EnsureMergeRequest(ctx context.Context, title string, fields beads.MergeRequestFields) (id string, alreadyClosed bool, err error)
	CloseMergeRequest(ctx context.Context, id, reason string) error
	ListMergeRequests(ctx context.Context, includeClosed bool) ([]beads.MergeRequest, error)
}

// Deps bundles the engine's dependencies.
type Deps struct {
	// Cfg is the loaded pg-pr config. Required.
	Cfg *config.Config

	// VCS maps a vcs name (e.g., "github") to a Provider. The default
	// engine wiring registers "github"; callers may inject mocks here.
	VCS map[string]VCSProvider

	// Beads is the bd client. Required.
	Beads BeadClient

	// StateDir overrides the default state directory
	// ($XDG_STATE_HOME/pg-pr or ~/.local/state/pg-pr). Tests inject this.
	StateDir string

	// Now is the clock. Defaults to time.Now (UTC) when nil.
	Now func() time.Time
}

// Engine carries the configured dependencies for a series of sync calls.
type Engine struct {
	deps Deps
}

// New constructs an Engine. Returns an error if required deps are missing.
func New(d Deps) (*Engine, error) {
	if d.Cfg == nil {
		return nil, errors.New("sync: cfg required")
	}
	if d.Beads == nil {
		return nil, errors.New("sync: beads client required")
	}
	if len(d.VCS) == 0 {
		return nil, errors.New("sync: at least one VCS provider required")
	}
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Engine{deps: d}, nil
}

// Summary is the result of a Sync call. Suitable for `--json` output.
type Summary struct {
	StartedAt    time.Time      `json:"started_at"`
	FinishedAt   time.Time      `json:"finished_at"`
	Repos        []RepoSummary  `json:"repos"`
	TotalPRs     int            `json:"total_prs"`
	BeadsCreated int            `json:"beads_created"`
	BeadsUpdated int            `json:"beads_updated"`
	BeadsClosed  int            `json:"beads_closed"`
	Errors       []SummaryError `json:"errors,omitempty"`
}

// RepoSummary is the per-repo slice of Summary.
type RepoSummary struct {
	Repo  string `json:"repo"`
	PRs   int    `json:"prs"`
	Error string `json:"error,omitempty"`
}

// SummaryError captures one repo's failure for the aggregated error path.
type SummaryError struct {
	Repo    string `json:"repo"`
	Message string `json:"message"`
}

// Sync runs one full sync cycle across all configured repos.
//
// Semantics:
//   - Each repo is sync'd independently. Errors are accumulated; partial
//     success is allowed.
//   - PRs are deduped by (repo, pr_number). Each is upserted to a
//     merge-request bead.
//   - After all repos succeed, beads whose (repo, pr_number) is no longer
//     in the watched set are closed with reason "upstream-not-watched".
//     Beads for a repo whose sync failed are NOT closed (we have no
//     authoritative view).
//   - The aggregate error is non-nil if any repo failed; the Summary is
//     still populated.
func (e *Engine) Sync(ctx context.Context) (*Summary, error) {
	summary := &Summary{StartedAt: e.deps.Now()}
	repoStates := map[string]repoState{}
	prevState, _ := loadState(e.stateFile())

	// Authoritative set of (repo, pr_number) observed across this sync.
	observed := map[prKey]api.PR{}
	// Repos for which we got a fully successful enumeration; only these
	// participate in upstream-not-watched closure.
	healthyRepos := map[string]bool{}

	for _, rcfg := range e.deps.Cfg.Repos {
		rs := RepoSummary{Repo: rcfg.Remote}
		state := repoState{LastSyncedAt: e.deps.Now().UTC().Format(time.RFC3339)}

		prs, err := e.enumerate(ctx, rcfg)
		if err != nil {
			rs.Error = err.Error()
			state.LastError = &repoErr{Code: "enum_failed", Message: err.Error()}
			summary.Errors = append(summary.Errors, SummaryError{Repo: rcfg.Remote, Message: err.Error()})
			summary.Repos = append(summary.Repos, rs)
			repoStates[rcfg.Remote] = state
			continue
		}
		for _, pr := range prs {
			observed[prKey{Repo: rcfg.Remote, Number: pr.Number}] = pr
		}
		rs.PRs = len(prs)
		summary.Repos = append(summary.Repos, rs)
		summary.TotalPRs += len(prs)
		healthyRepos[rcfg.Remote] = true
		repoStates[rcfg.Remote] = state
	}

	// Upsert beads for each observed PR. Track whether the call was a
	// create or an update by comparing pre/post existence.
	preExisting, _ := e.listExistingByKey(ctx)
	for key, pr := range observed {
		fields := beads.MergeRequestFields{
			Repo:         key.Repo,
			PRNumber:     pr.Number,
			State:        stateForPR(pr),
			Branch:       pr.Branch,
			Base:         pr.Base,
			Author:       pr.Author,
			URL:          pr.URL,
			LastSyncedAt: e.deps.Now().UTC().Format(time.RFC3339),
			Draft:        pr.Draft,
		}
		_, alreadyClosed, err := e.deps.Beads.EnsureMergeRequest(ctx, pr.URL, fields)
		if err != nil {
			summary.Errors = append(summary.Errors, SummaryError{
				Repo:    key.Repo,
				Message: fmt.Sprintf("PR #%d: %v", pr.Number, err),
			})
			continue
		}
		if alreadyClosed {
			continue
		}
		if _, was := preExisting[key]; was {
			summary.BeadsUpdated++
		} else {
			summary.BeadsCreated++
		}
	}

	// Close beads whose PR is no longer in the observed set, but only
	// among repos that synced successfully. This prevents us from closing
	// beads in a repo whose sync failed (we'd have no authoritative view).
	all, err := e.deps.Beads.ListMergeRequests(ctx, false /* open only */)
	if err != nil {
		summary.Errors = append(summary.Errors, SummaryError{
			Repo:    "(bd)",
			Message: fmt.Sprintf("list open beads: %v", err),
		})
	} else {
		for _, mr := range all {
			if !healthyRepos[mr.Fields.Repo] {
				continue
			}
			k := prKey{Repo: mr.Fields.Repo, Number: mr.Fields.PRNumber}
			if _, watched := observed[k]; watched {
				continue
			}
			if err := e.deps.Beads.CloseMergeRequest(ctx, mr.ID, "upstream-not-watched"); err != nil {
				summary.Errors = append(summary.Errors, SummaryError{
					Repo:    mr.Fields.Repo,
					Message: fmt.Sprintf("close stale bead %s: %v", mr.ID, err),
				})
				continue
			}
			summary.BeadsClosed++
		}
	}

	// Persist per-repo state, preserving entries for repos we didn't try
	// this run.
	mergedState := prevState
	if mergedState.Repos == nil {
		mergedState.Repos = map[string]repoState{}
	}
	maps.Copy(mergedState.Repos, repoStates)
	if err := saveState(e.stateFile(), mergedState); err != nil {
		summary.Errors = append(summary.Errors, SummaryError{
			Repo:    "(state)",
			Message: fmt.Sprintf("save state file: %v", err),
		})
	}

	summary.FinishedAt = e.deps.Now()
	if len(summary.Errors) > 0 {
		return summary, fmt.Errorf("sync: %d error(s) (see Summary.Errors)", len(summary.Errors))
	}
	return summary, nil
}

// SyncPR refreshes a single PR. The repo identifier MUST be in the configured
// list (the engine can only sync repos with VCS provider config).
func (e *Engine) SyncPR(ctx context.Context, repo string, number int) (*Summary, error) {
	if repo == "" || number <= 0 {
		return nil, errors.New("sync: repo and PR number required")
	}
	rcfg, err := e.repoConfig(repo)
	if err != nil {
		return nil, err
	}
	provider, err := e.providerFor(rcfg)
	if err != nil {
		return nil, err
	}

	summary := &Summary{StartedAt: e.deps.Now()}
	pr, err := provider.GetPR(ctx, repo, number)
	if err != nil {
		summary.FinishedAt = e.deps.Now()
		summary.Errors = append(summary.Errors, SummaryError{
			Repo: repo, Message: fmt.Sprintf("PR #%d: %v", number, err),
		})
		return summary, fmt.Errorf("sync PR: %w", err)
	}

	fields := beads.MergeRequestFields{
		Repo:         repo,
		PRNumber:     pr.Number,
		State:        stateForPR(*pr),
		Branch:       pr.Branch,
		Base:         pr.Base,
		Author:       pr.Author,
		URL:          pr.URL,
		LastSyncedAt: e.deps.Now().UTC().Format(time.RFC3339),
		Draft:        pr.Draft,
	}

	// If upstream is closed/merged, close the bead instead of upserting an
	// open one.
	if pr.State == "closed" || pr.State == "merged" || pr.Merged {
		existing, err := e.findBeadByPR(ctx, repo, pr.Number)
		if err != nil {
			return summary, err
		}
		if existing != nil {
			reason := "upstream-" + pr.State
			if pr.Merged {
				reason = "upstream-merged"
			}
			if err := e.deps.Beads.CloseMergeRequest(ctx, existing.ID, reason); err != nil {
				summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: err.Error()})
				summary.FinishedAt = e.deps.Now()
				return summary, err
			}
			summary.BeadsClosed = 1
		}
		summary.Repos = []RepoSummary{{Repo: repo, PRs: 1}}
		summary.TotalPRs = 1
		summary.FinishedAt = e.deps.Now()
		return summary, nil
	}

	_, alreadyClosed, err := e.deps.Beads.EnsureMergeRequest(ctx, pr.URL, fields)
	if err != nil {
		summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: err.Error()})
		summary.FinishedAt = e.deps.Now()
		return summary, err
	}
	if !alreadyClosed {
		// Updated or created — we don't differentiate here for the single-PR
		// path.
		summary.BeadsUpdated = 1
	}
	summary.Repos = []RepoSummary{{Repo: repo, PRs: 1}}
	summary.TotalPRs = 1
	summary.FinishedAt = e.deps.Now()
	return summary, nil
}

// enumerate lists watched PRs for a single repo. Phase 1: self + team
// members. (watch_labels is not yet honored — design notes it but Phase 1
// only needs author-based selection to be useful.)
func (e *Engine) enumerate(ctx context.Context, rcfg config.RepoConfig) ([]api.PR, error) {
	provider, err := e.providerFor(rcfg)
	if err != nil {
		return nil, err
	}
	seen := map[int]struct{}{}
	out := make([]api.PR, 0)

	myPRs, err := provider.ListMyPRs(ctx, rcfg.Remote)
	if err != nil {
		return nil, fmt.Errorf("list my PRs: %w", err)
	}
	for _, pr := range myPRs {
		if _, dup := seen[pr.Number]; !dup {
			seen[pr.Number] = struct{}{}
			out = append(out, pr)
		}
	}

	if len(rcfg.TeamMembers) > 0 {
		teamPRs, err := provider.ListTeamPRs(ctx, rcfg.Remote, rcfg.TeamMembers)
		if err != nil {
			return nil, fmt.Errorf("list team PRs: %w", err)
		}
		for _, pr := range teamPRs {
			if _, dup := seen[pr.Number]; !dup {
				seen[pr.Number] = struct{}{}
				out = append(out, pr)
			}
		}
	}
	return out, nil
}

// providerFor returns the configured VCS provider for a repo. Defaults to
// "github" when rcfg.VCS is unset.
func (e *Engine) providerFor(rcfg config.RepoConfig) (VCSProvider, error) {
	name := rcfg.VCS
	if name == "" {
		name = "github"
	}
	p, ok := e.deps.VCS[name]
	if !ok {
		return nil, fmt.Errorf("sync: no VCS provider registered for %q (repo %q)", name, rcfg.Remote)
	}
	return p, nil
}

// repoConfig finds the config entry for a repo by remote.
func (e *Engine) repoConfig(remote string) (config.RepoConfig, error) {
	for _, r := range e.deps.Cfg.Repos {
		if r.Remote == remote {
			return r, nil
		}
	}
	return config.RepoConfig{}, fmt.Errorf("sync: repo %q not in config", remote)
}

// listExistingByKey indexes all open merge-request beads by (repo, pr_number).
func (e *Engine) listExistingByKey(ctx context.Context) (map[prKey]beads.MergeRequest, error) {
	all, err := e.deps.Beads.ListMergeRequests(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make(map[prKey]beads.MergeRequest, len(all))
	for _, mr := range all {
		out[prKey{Repo: mr.Fields.Repo, Number: mr.Fields.PRNumber}] = mr
	}
	return out, nil
}

// findBeadByPR returns the open or closed merge-request bead for a given
// (repo, pr_number) or nil if not found.
func (e *Engine) findBeadByPR(ctx context.Context, repo string, pr int) (*beads.MergeRequest, error) {
	all, err := e.deps.Beads.ListMergeRequests(ctx, true)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Fields.Repo == repo && all[i].Fields.PRNumber == pr {
			return &all[i], nil
		}
	}
	return nil, nil
}

// stateForPR derives the bead state value from an api.PR.
func stateForPR(pr api.PR) string {
	if pr.Merged {
		return "merged"
	}
	switch strings.ToLower(pr.State) {
	case "open":
		if pr.Draft {
			return "draft"
		}
		return "open"
	case "closed":
		return "closed"
	case "merged":
		return "merged"
	default:
		return strings.ToLower(pr.State)
	}
}

// ---------------------------------------------------------------------
// repoState: $XDG_STATE_HOME/pg-pr/repo-state.json
// ---------------------------------------------------------------------

type prKey struct {
	Repo   string
	Number int
}

type stateFile struct {
	Repos map[string]repoState `json:"repos"`
}

type repoState struct {
	LastSyncedAt string   `json:"last_synced_at"`
	LastError    *repoErr `json:"last_error,omitempty"`
}

type repoErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// stateFile returns the path to the persistent state file.
func (e *Engine) stateFile() string {
	if e.deps.StateDir != "" {
		return filepath.Join(e.deps.StateDir, "repo-state.json")
	}
	return defaultStateFile()
}

func defaultStateFile() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "pg-pr", "repo-state.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "pg-pr", "repo-state.json")
}

func loadState(path string) (stateFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return stateFile{Repos: map[string]repoState{}}, err
	}
	var sf stateFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return stateFile{Repos: map[string]repoState{}}, err
	}
	if sf.Repos == nil {
		sf.Repos = map[string]repoState{}
	}
	return sf, nil
}

func saveState(path string, sf stateFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ---------------------------------------------------------------------
// Package-level convenience wrappers.
// ---------------------------------------------------------------------

// Sync runs one full sync cycle using a Default engine constructed from the
// current process environment. Returns ErrNoConfig wrapped from config.Load
// if no config exists.
func Sync(ctx context.Context) (*Summary, error) {
	return nil, errors.New("sync: top-level Sync() requires explicit Deps; use Engine.Sync()")
}

// SyncPR is the package-level entry point. Production callers wire deps
// themselves via New; this helper exists to keep the legacy Phase 0
// signature compiling.
func SyncPR(_ context.Context, _ string, _ int) error {
	return errors.New("sync: top-level SyncPR() requires explicit Deps; use Engine.SyncPR()")
}

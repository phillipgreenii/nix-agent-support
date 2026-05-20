// Package sync is the pg-pr sync engine.
//
// Phase 1 scope: enumerate watched PRs per repo (configured self + team
// members), upsert one merge-request bead per PR, and close merge-request
// beads whose upstream PR is no longer in the watched set.
//
// Phase 3 extends the engine with:
//
//   - Processing-cycle bead creation per (repo, pr) when new feedback
//     arrives.
//   - Feedback bead creation per upstream event (comment thread, CI
//     failure, review thread), with fingerprint-based dedup.
//   - Feedback close on upstream resolution (resolved-upstream).
//   - CI-loop escalation: increments a counter when consecutive cycles
//     close with only CI-failure feedback; crosses the configured threshold
//     to escalate via `bd human`.
//   - Draft auto-promote: when a PR is in draft state and all CI runs are
//     green, mark it ready.
//   - Cascade-on-close: when the upstream PR closes/merges, close the
//     merge-request bead and all its descendants.
//
// The engine is dependency-injected via the Deps struct so callers (tests,
// CLI wiring) can compose their own VCS providers and bd clients.
package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// CommentReader is the optional subset of VCSProvider the Phase 3 feedback
// pipeline uses. A VCSProvider that also implements CommentReader will be
// queried for comments; otherwise the feedback pipeline silently skips
// comment-based events.
type CommentReader interface {
	ListComments(ctx context.Context, repo string, number int) ([]api.Comment, error)
}

// DraftToggler is the optional subset of VCSProvider needed for the draft
// auto-promote feature.
type DraftToggler interface {
	SetDraft(ctx context.Context, repo string, number int, draft bool) error
}

// ThreadReplier is the optional subset of VCSProvider used by the B3
// reply-sync pass. A provider that implements ThreadReplier can post
// queued reply_draft bodies back to upstream review/comment threads.
type ThreadReplier interface {
	ReplyToThread(ctx context.Context, repo string, threadID, body string) (*api.Comment, error)
}

// CICDProvider is the subset of pkg/provider/cicd.Provider the sync engine
// uses to fetch CI runs.
type CICDProvider interface {
	ListRuns(ctx context.Context, repo string, prNumber int) ([]api.CIRun, error)
}

// BeadClient is the subset of *pkg/beads.Client the sync engine uses.
type BeadClient interface {
	EnsureMergeRequest(ctx context.Context, title string, fields beads.MergeRequestFields) (id string, alreadyClosed bool, err error)
	UpdateMergeRequest(ctx context.Context, id string, fields beads.MergeRequestFields) error
	CloseMergeRequest(ctx context.Context, id, reason string) error
	ListMergeRequests(ctx context.Context, includeClosed bool) ([]beads.MergeRequest, error)
	GetMergeRequest(ctx context.Context, id string) (*beads.MergeRequest, error)

	CreateProcessingCycle(ctx context.Context, prBeadID, title string) (string, error)
	FindOpenProcessingCycle(ctx context.Context, prBeadID string) (string, bool, error)
	CloseProcessingCycle(ctx context.Context, id, reason string) error
	ListChildrenOfPR(ctx context.Context, prBeadID string) ([]string, error)

	CreateFeedback(ctx context.Context, in beads.CreateFeedbackInput) (string, error)
	MarkFeedbackResolvedUpstream(ctx context.Context, id string) error
	ListFeedback(ctx context.Context, cycleID string, includeClosed bool) ([]beads.Feedback, error)
	FindFeedbackByFingerprint(ctx context.Context, cycleID, fingerprint string) (*beads.Feedback, error)
	CloseFeedback(ctx context.Context, id, reason string) error

	// Reply pipeline (B3).
	ListFeedbackPendingReply(ctx context.Context) ([]beads.Feedback, error)
	SetResponseID(ctx context.Context, id, responseID string) error
	FindMergeRequestForFeedback(ctx context.Context, feedbackID string) (*beads.MergeRequest, error)
}

// Deps bundles the engine's dependencies.
type Deps struct {
	// Cfg is the loaded pg-pr config. Required.
	Cfg *config.Config

	// VCS maps a vcs name (e.g., "github") to a Provider. The default
	// engine wiring registers "github"; callers may inject mocks here.
	VCS map[string]VCSProvider

	// CICD maps a cicd name (e.g., "github-actions") to a Provider. Phase 3:
	// providers listed in a repo's `cicd:` config block are queried for runs;
	// when no provider is configured, the engine silently skips CI feedback.
	CICD map[string]CICDProvider

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
	StartedAt       time.Time      `json:"started_at"`
	FinishedAt      time.Time      `json:"finished_at"`
	Repos           []RepoSummary  `json:"repos"`
	TotalPRs        int            `json:"total_prs"`
	BeadsCreated    int            `json:"beads_created"`
	BeadsUpdated    int            `json:"beads_updated"`
	BeadsClosed     int            `json:"beads_closed"`
	FeedbackCreated int            `json:"feedback_created,omitempty"`
	FeedbackClosed  int            `json:"feedback_closed,omitempty"`
	CyclesCreated   int            `json:"cycles_created,omitempty"`
	DraftPromoted   int            `json:"draft_promoted,omitempty"`
	Escalated       int            `json:"escalated,omitempty"`
	RepliesPosted   int            `json:"replies_posted,omitempty"`
	Errors          []SummaryError `json:"errors,omitempty"`
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
		prBeadID, alreadyClosed, err := e.deps.Beads.EnsureMergeRequest(ctx, pr.URL, fields)
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

		// Phase 3: drive feedback + draft auto-promote pipelines for the PR.
		if err := e.processFeedback(ctx, key.Repo, pr, prBeadID, summary); err != nil {
			summary.Errors = append(summary.Errors, SummaryError{
				Repo:    key.Repo,
				Message: fmt.Sprintf("PR #%d feedback: %v", pr.Number, err),
			})
		}
		if err := e.maybePromoteDraft(ctx, key.Repo, pr, prBeadID, summary); err != nil {
			summary.Errors = append(summary.Errors, SummaryError{
				Repo:    key.Repo,
				Message: fmt.Sprintf("PR #%d draft-promote: %v", pr.Number, err),
			})
		}
	}

	// Phase 6 B3: process queued replies (LLM stored reply_draft; we post +
	// record response_id). Runs once per healthy repo — the helper filters
	// feedback beads to only those whose merge-request belongs to that repo.
	for repo := range healthyRepos {
		rcfg, err := e.repoConfig(repo)
		if err != nil {
			continue
		}
		if err := e.processReplyDrafts(ctx, rcfg, summary); err != nil {
			summary.Errors = append(summary.Errors, SummaryError{
				Repo:    repo,
				Message: fmt.Sprintf("reply pipeline: %v", err),
			})
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
			// Cascade: close all descendants (processing-cycles, feedback,
			// actions) with reason pr-closed.
			e.cascadeClose(ctx, mr.ID, "pr-closed", summary)
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
	// open one and cascade-close its descendants.
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
			e.cascadeClose(ctx, existing.ID, "pr-closed", summary)
		}
		summary.Repos = []RepoSummary{{Repo: repo, PRs: 1}}
		summary.TotalPRs = 1
		summary.FinishedAt = e.deps.Now()
		return summary, nil
	}

	prBeadID, alreadyClosed, err := e.deps.Beads.EnsureMergeRequest(ctx, pr.URL, fields)
	if err != nil {
		summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: err.Error()})
		summary.FinishedAt = e.deps.Now()
		return summary, err
	}
	if !alreadyClosed {
		summary.BeadsUpdated = 1
		// Phase 3: feedback + draft pipelines.
		if err := e.processFeedback(ctx, repo, *pr, prBeadID, summary); err != nil {
			summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: err.Error()})
		}
		if err := e.maybePromoteDraft(ctx, repo, *pr, prBeadID, summary); err != nil {
			summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: err.Error()})
		}
		// Phase 6 B3: post queued replies for feedback beads under this repo.
		if err := e.processReplyDrafts(ctx, rcfg, summary); err != nil {
			summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: err.Error()})
		}
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
// Phase 3: feedback + processing-cycle + draft auto-promote pipelines.
// ---------------------------------------------------------------------

// processFeedback inspects upstream events for a PR and ensures the bd
// feedback beads (and a parent processing-cycle bead) reflect them.
//
// Workflow:
//
//  1. Collect events from configured providers (comments, CI runs).
//  2. For each event, compute a stable fingerprint. Dedup against any
//     feedback bead already created under any cycle for this PR.
//  3. New events: ensure a processing-cycle exists, create the feedback
//     bead under it. CI events whose conclusion changed from failure →
//     success close the matching feedback with reason resolved-upstream.
//
// Returns the first error encountered after best-effort processing.
func (e *Engine) processFeedback(ctx context.Context, repo string, pr api.PR, prBeadID string, summary *Summary) error {
	if prBeadID == "" {
		return nil
	}
	rcfg, err := e.repoConfig(repo)
	if err != nil {
		// Not in config (single-PR ad-hoc) — feedback pipeline is repo-driven.
		return nil
	}

	// Gather events.
	var events []feedbackEvent
	provider, _ := e.providerFor(rcfg)
	if reader, ok := provider.(CommentReader); ok {
		comments, err := reader.ListComments(ctx, repo, pr.Number)
		if err != nil {
			return fmt.Errorf("list comments: %w", err)
		}
		for _, c := range comments {
			events = append(events, commentEvent(c))
		}
	}
	for _, cicdName := range rcfg.CICD {
		cp, ok := e.deps.CICD[cicdName]
		if !ok {
			continue
		}
		runs, err := cp.ListRuns(ctx, repo, pr.Number)
		if err != nil {
			// CI errors are non-fatal — the rest of the cycle should still
			// progress.
			continue
		}
		for _, r := range runs {
			events = append(events, ciRunEvent(r))
		}
	}

	if len(events) == 0 {
		return nil
	}

	// Find or create the active processing-cycle for new feedback.
	cycleID, found, err := e.deps.Beads.FindOpenProcessingCycle(ctx, prBeadID)
	if err != nil {
		return fmt.Errorf("find processing-cycle: %w", err)
	}

	// First pass: handle CI events whose conclusion is success and close
	// any matching prior ci-failure feedback (resolved-upstream).
	if found {
		open, err := e.deps.Beads.ListFeedback(ctx, cycleID, false)
		if err == nil {
			closedSet := map[string]bool{}
			for _, ev := range events {
				if ev.kind != beads.FeedbackKindCIFailure || ev.ciConclusion != "success" {
					continue
				}
				for _, fb := range open {
					if closedSet[fb.ID] {
						continue
					}
					// Match by the CI run's "name" carried as external_id
					// or by fingerprint stem.
					if fb.Fields.ExternalID != "" && fb.Fields.ExternalID == ev.externalID {
						_ = e.deps.Beads.MarkFeedbackResolvedUpstream(ctx, fb.ID)
						summary.FeedbackClosed++
						closedSet[fb.ID] = true
					}
				}
			}
		}
	}

	// Second pass: create new feedback beads for net-new events.
	for _, ev := range events {
		// Skip CI success-events (they only close prior failures).
		if ev.kind == beads.FeedbackKindCIFailure && ev.ciConclusion != "failure" {
			continue
		}
		// Dedup: if a feedback with this fingerprint already exists under
		// any cycle for this PR, skip.
		existing, err := e.findFeedbackForPR(ctx, prBeadID, ev.fingerprint)
		if err != nil {
			continue
		}
		if existing != nil {
			continue
		}

		if !found {
			// Lazily create a processing-cycle on first new feedback.
			id, err := e.deps.Beads.CreateProcessingCycle(ctx, prBeadID, fmt.Sprintf("%s#%d", repo, pr.Number))
			if err != nil {
				return fmt.Errorf("create processing-cycle: %w", err)
			}
			cycleID = id
			found = true
			summary.CyclesCreated++
		}

		if _, err := e.deps.Beads.CreateFeedback(ctx, beads.CreateFeedbackInput{
			ProcessingCycleID: cycleID,
			Kind:              ev.kind,
			ExternalID:        ev.externalID,
			Fingerprint:       ev.fingerprint,
			AuthorRole:        ev.authorRole,
			Title:             ev.title,
			Body:              ev.body,
		}); err != nil {
			return fmt.Errorf("create feedback: %w", err)
		}
		summary.FeedbackCreated++
	}
	return nil
}

// findFeedbackForPR searches every processing-cycle under prBeadID for a
// feedback bead with the given fingerprint. Returns nil when fingerprint
// is empty or no match is found.
func (e *Engine) findFeedbackForPR(ctx context.Context, prBeadID, fingerprint string) (*beads.Feedback, error) {
	if fingerprint == "" {
		return nil, nil
	}
	// We don't currently index cycles per PR cheaply; brute-force list
	// feedback under any cycle linked from prBeadID. ListChildrenOfPR
	// returns processing-cycles + action beads — we check each.
	children, err := e.deps.Beads.ListChildrenOfPR(ctx, prBeadID)
	if err != nil {
		return nil, err
	}
	for _, childID := range children {
		fb, err := e.deps.Beads.FindFeedbackByFingerprint(ctx, childID, fingerprint)
		if err != nil {
			continue
		}
		if fb != nil {
			return fb, nil
		}
	}
	return nil, nil
}

// maybePromoteDraft inspects the PR's draft state and, when all CI runs
// are green, promotes the PR to ready.
func (e *Engine) maybePromoteDraft(ctx context.Context, repo string, pr api.PR, prBeadID string, summary *Summary) error {
	if !pr.Draft {
		return nil
	}
	rcfg, err := e.repoConfig(repo)
	if err != nil {
		return nil
	}
	if len(rcfg.CICD) == 0 {
		return nil
	}
	for _, cicdName := range rcfg.CICD {
		cp, ok := e.deps.CICD[cicdName]
		if !ok {
			continue
		}
		runs, err := cp.ListRuns(ctx, repo, pr.Number)
		if err != nil {
			return nil
		}
		if !allRunsSuccessful(runs) {
			return nil
		}
	}
	provider, _ := e.providerFor(rcfg)
	dt, ok := provider.(DraftToggler)
	if !ok {
		return nil
	}
	if err := dt.SetDraft(ctx, repo, pr.Number, false); err != nil {
		return fmt.Errorf("set-draft=false: %w", err)
	}
	// Persist the new state on the merge-request bead.
	_ = e.deps.Beads.UpdateMergeRequest(ctx, prBeadID, beads.MergeRequestFields{
		Repo:     repo,
		PRNumber: pr.Number,
		State:    "open",
	})
	summary.DraftPromoted++
	return nil
}

// processReplyDrafts iterates feedback beads whose LLM-authored reply_draft
// is queued for posting and posts each via vcs.ReplyToThread. The returned
// upstream comment id is stored back on the bead as response_id, which is
// the idempotency marker — subsequent sync passes skip beads whose
// response_id is set.
//
// Scope: only feedback beads whose enclosing merge-request belongs to rcfg
// are processed by this call. Other repos handle their own beads in their
// own loop iteration.
//
// Per-bead failures (VCS errors, walker errors, missing external_id) are
// recorded into summary.Errors but do not abort the loop — next sync
// retries because response_id is still empty.
func (e *Engine) processReplyDrafts(ctx context.Context, rcfg config.RepoConfig, summary *Summary) error {
	provider, err := e.providerFor(rcfg)
	if err != nil {
		return nil
	}
	replier, ok := provider.(ThreadReplier)
	if !ok {
		// No reply capability — skip silently (e.g., a Phase 0 stub provider).
		return nil
	}
	pending, err := e.deps.Beads.ListFeedbackPendingReply(ctx)
	if err != nil {
		return fmt.Errorf("list pending replies: %w", err)
	}
	for _, fb := range pending {
		// Walk up: feedback → processing-cycle → merge-request.
		mr, err := e.deps.Beads.FindMergeRequestForFeedback(ctx, fb.ID)
		if err != nil {
			summary.Errors = append(summary.Errors, SummaryError{
				Repo:    rcfg.Remote,
				Message: fmt.Sprintf("reply %s: find merge-request: %v", fb.ID, err),
			})
			continue
		}
		if mr == nil {
			// Orphan feedback — no merge-request anchor. Skip silently;
			// another component should clean this up.
			continue
		}
		// Scope to current repo — other repos will handle their own beads.
		if mr.Fields.Repo != rcfg.Remote {
			continue
		}

		// Only comment- and review-thread feedback can be replied to.
		switch fb.Fields.Kind {
		case string(beads.FeedbackKindCommentThread), string(beads.FeedbackKindReviewThread):
			// supported
		default:
			summary.Errors = append(summary.Errors, SummaryError{
				Repo:    rcfg.Remote,
				Message: fmt.Sprintf("reply %s: cannot reply to %s", fb.ID, fb.Fields.Kind),
			})
			continue
		}

		if fb.Fields.ExternalID == "" {
			summary.Errors = append(summary.Errors, SummaryError{
				Repo:    rcfg.Remote,
				Message: fmt.Sprintf("reply %s: missing external_id (thread id)", fb.ID),
			})
			continue
		}

		resp, err := replier.ReplyToThread(ctx, mr.Fields.Repo, fb.Fields.ExternalID, fb.Fields.ReplyDraft)
		if err != nil {
			summary.Errors = append(summary.Errors, SummaryError{
				Repo:    rcfg.Remote,
				Message: fmt.Sprintf("reply %s: ReplyToThread: %v", fb.ID, err),
			})
			continue
		}
		var respID string
		if resp != nil {
			respID = resp.ID
		}
		if respID == "" {
			// VCS returned nil/empty id — without an idempotency marker we
			// would re-post next sync. Record an error and move on.
			summary.Errors = append(summary.Errors, SummaryError{
				Repo:    rcfg.Remote,
				Message: fmt.Sprintf("reply %s: ReplyToThread returned empty response id", fb.ID),
			})
			continue
		}
		if err := e.deps.Beads.SetResponseID(ctx, fb.ID, respID); err != nil {
			summary.Errors = append(summary.Errors, SummaryError{
				Repo:    rcfg.Remote,
				Message: fmt.Sprintf("reply %s: SetResponseID: %v", fb.ID, err),
			})
			continue
		}
		summary.RepliesPosted++
	}
	return nil
}

// allRunsSuccessful returns true when there is at least one run and every
// completed run has conclusion=success.
func allRunsSuccessful(runs []api.CIRun) bool {
	if len(runs) == 0 {
		return false
	}
	for _, r := range runs {
		if r.Status != "completed" {
			return false
		}
		if r.Conclusion != "success" {
			return false
		}
	}
	return true
}

// cascadeClose closes all descendants of prBeadID with the given reason.
// Errors are absorbed into summary.Errors; the cascade is best-effort.
func (e *Engine) cascadeClose(ctx context.Context, prBeadID, reason string, summary *Summary) {
	children, err := e.deps.Beads.ListChildrenOfPR(ctx, prBeadID)
	if err != nil {
		return
	}
	for _, childID := range children {
		// Close as a feedback bead first (works for feedback bd type) — if
		// that fails because the bead is a task or action, fall back to a
		// generic close via the same wrapper.
		_ = e.deps.Beads.CloseFeedback(ctx, childID, reason)
		_ = e.deps.Beads.CloseProcessingCycle(ctx, childID, reason)
		summary.BeadsClosed++
	}
}

// ---------------------------------------------------------------------
// Feedback event normalization
// ---------------------------------------------------------------------

// feedbackEvent is the engine's normalized view of an upstream signal.
type feedbackEvent struct {
	kind         beads.FeedbackKind
	externalID   string
	fingerprint  string
	authorRole   beads.AuthorRole
	title        string
	body         string
	ciConclusion string // populated only for CI events
}

// commentEvent converts an api.Comment into a feedbackEvent. The fingerprint
// covers the (author, path, line, body) tuple so the same comment posted on
// different lines still dedups per-line.
func commentEvent(c api.Comment) feedbackEvent {
	kind := beads.FeedbackKindCommentThread
	if c.Path != "" || c.Line > 0 || c.ThreadID != "" {
		kind = beads.FeedbackKindReviewThread
	}
	fingerprint := fingerprintOf("comment", c.Author, c.Path, fmt.Sprintf("%d", c.Line), c.Body)
	role := commentAuthorRole(c)
	title := strings.TrimSpace(strings.SplitN(c.Body, "\n", 2)[0])
	if title == "" {
		title = "comment from " + c.Author
	}
	return feedbackEvent{
		kind:        kind,
		externalID:  c.ID,
		fingerprint: fingerprint,
		authorRole:  role,
		title:       title,
		body:        c.Body,
	}
}

// commentAuthorRole maps GitHub's author_association string to our
// AuthorRole enum. "MEMBER" / "OWNER" / "COLLABORATOR" → team_member;
// "FIRST_TIMER" / "NONE" → org_member; bots are detected by [bot] suffix.
func commentAuthorRole(c api.Comment) beads.AuthorRole {
	if strings.HasSuffix(c.Author, "[bot]") {
		return beads.AuthorRoleBot
	}
	switch strings.ToLower(c.AuthorRole) {
	case "member", "owner", "collaborator":
		return beads.AuthorRoleTeamMember
	case "first_timer", "first_time_contributor", "none", "contributor":
		return beads.AuthorRoleOrgMember
	default:
		return beads.AuthorRoleOrgMember
	}
}

// ciRunEvent converts an api.CIRun into a feedbackEvent. The fingerprint
// covers (name, conclusion) so a workflow that flips green → red → green
// surfaces a fresh feedback bead each time.
func ciRunEvent(r api.CIRun) feedbackEvent {
	fingerprint := fingerprintOf("ci", r.Provider, r.Name, r.Conclusion)
	title := fmt.Sprintf("CI %s: %s", r.Conclusion, r.Name)
	return feedbackEvent{
		kind:         beads.FeedbackKindCIFailure,
		externalID:   r.ID,
		fingerprint:  fingerprint,
		authorRole:   beads.AuthorRoleSelf,
		title:        title,
		body:         fmt.Sprintf("%s run %q concluded with %q (%s)", r.Provider, r.Name, r.Conclusion, r.URL),
		ciConclusion: r.Conclusion,
	}
}

// fingerprintOf builds a stable sha256 fingerprint from the given parts.
func fingerprintOf(parts ...string) string {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
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

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

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/telemetry"
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

// ReviewLister is the optional subset of VCSProvider the dashboard snapshot
// builder uses. A VCSProvider that also implements ReviewLister is queried
// for reviews; otherwise the snapshot's approval classification proceeds
// with an empty review set.
type ReviewLister interface {
	ListReviews(ctx context.Context, repo string, number int) ([]api.Review, error)
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

// CICDBranchLister is an optional capability for CICD providers that can
// list runs by an already-known head branch, skipping the PR → branch
// resolution call. Implemented by ghactions.Provider. The sync engine
// prefers this over ListRuns when the head branch is already populated
// on api.PR (which it is for every github-listed PR).
type CICDBranchLister interface {
	ListRunsByBranch(ctx context.Context, repo, branch string) ([]api.CIRun, error)
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

	// Snapshot, when non-nil, populates a dashboard snapshot at the end of
	// each successful Sync iteration. Nil disables snapshot building.
	Snapshot *snapshot.Store

	// AgentRegistry classifies reviewers/comment authors. Required when
	// Snapshot is non-nil; ignored otherwise.
	AgentRegistry *agentregistry.Registry

	// SyncInterval is the daemon's tick interval, exposed verbatim on the
	// snapshot so the dashboard can compute staleness. Zero when not in
	// daemon mode (snapshot may still be written, with sync_interval_seconds
	// reading 0).
	SyncInterval time.Duration
}

// Engine carries the configured dependencies for a series of sync calls.
type Engine struct {
	deps Deps
}

// New constructs an Engine. Returns an error if required deps are missing.
//
// When d.Beads is non-nil it acts as a test override: every per-repo bd
// operation routes through that single client (useful for tests that share
// an in-memory bd). When d.Beads is nil, the engine constructs a per-repo
// Client via beads.NewClientForRepo(rcfg.Path) before each repo's
// operations, so writes land in the monorepo's own .beads/ workspace.
func New(d Deps) (*Engine, error) {
	if d.Cfg == nil {
		return nil, errors.New("sync: cfg required")
	}
	if len(d.VCS) == 0 {
		return nil, errors.New("sync: at least one VCS provider required")
	}
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Engine{deps: d}, nil
}

// bdClientFor returns the BeadClient the engine should use for operations
// against the given repo's workspace.
//
//   - If Deps.Beads is set (test injection or callers that want a shared
//     client), it is returned unchanged.
//   - Otherwise a fresh beads.Client is constructed via NewClientForRepo,
//     using rcfg.Path as the bd cwd so bd discovers that monorepo's
//     `.beads/` workspace.
//
// Construction is cheap (no I/O until a bd command is issued), so it's
// safe to call per repo iteration.
func (e *Engine) bdClientFor(rcfg config.RepoConfig) BeadClient {
	if e.deps.Beads != nil {
		return e.deps.Beads
	}
	return beads.NewClientForRepo(rcfg.Path)
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
	// Cache per-repo bd clients so we construct one per monorepo workspace
	// and reuse it across the PR-upsert + close loops. Each Client wraps a
	// CLIRunner with Dir=rcfg.Path, so bd discovers that monorepo's own
	// .beads/ workspace.
	repoClients := map[string]BeadClient{}
	// Per-repo (repo, pr_number) index of pre-existing open merge-request
	// beads — used to distinguish creates vs updates per-repo so the
	// summary counters stay accurate even when each workspace holds its
	// own beads.
	repoPreExisting := map[string]map[prKey]beads.MergeRequest{}
	// Per-repo config lookup, populated alongside repoClients so downstream
	// passes (replies + close-stale) don't have to re-look-up.
	repoCfgs := map[string]config.RepoConfig{}

	for _, rcfg := range e.deps.Cfg.Repos {
		func() {
			repoCtx, repoSpan := startRepoSpan(ctx, rcfg.Remote)
			defer repoSpan.End()

			rs := RepoSummary{Repo: rcfg.Remote}
			state := repoState{LastSyncedAt: e.deps.Now().UTC().Format(time.RFC3339)}

			prs, err := e.enumerate(repoCtx, rcfg)
			if err != nil {
				recordSpanErr(repoSpan, err)
				rs.Error = err.Error()
				state.LastError = &repoErr{Code: "enum_failed", Message: err.Error()}
				summary.Errors = append(summary.Errors, SummaryError{Repo: rcfg.Remote, Message: err.Error()})
				summary.Repos = append(summary.Repos, rs)
				repoStates[rcfg.Remote] = state
				return
			}
			for _, pr := range prs {
				observed[prKey{Repo: rcfg.Remote, Number: pr.Number}] = pr
			}
			rs.PRs = len(prs)
			summary.Repos = append(summary.Repos, rs)
			summary.TotalPRs += len(prs)
			healthyRepos[rcfg.Remote] = true
			repoStates[rcfg.Remote] = state
			// Build the per-repo bd client now that we know this repo will
			// participate in the rest of the pipeline.
			bdc := e.bdClientFor(rcfg)
			repoClients[rcfg.Remote] = bdc
			repoCfgs[rcfg.Remote] = rcfg
			// Pre-index existing open beads for this repo so the
			// create-vs-update counters reflect this workspace only.
			if pre, perr := e.listExistingByKey(repoCtx, bdc); perr == nil {
				repoPreExisting[rcfg.Remote] = pre
			}
		}()
	}

	// Upsert beads for each observed PR. Each PR is dispatched against its
	// own monorepo's bd client.
	for key, pr := range observed {
		func() {
			prCtx, prSpan := startPRSpan(ctx, key.Repo, pr.Number, pr.Author)
			defer prSpan.End()
			startedAt := e.deps.Now()
			defer func() {
				telemetry.SyncPRDuration.
					WithLabelValues(key.Repo).
					Observe(e.deps.Now().Sub(startedAt).Seconds())
			}()

			bdc := repoClients[key.Repo]
			if bdc == nil {
				// Defensive: a repo can only land in `observed` after its
				// client was registered, but guard anyway.
				return
			}

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
			bdCtx, bdSpan := startBeadsSpan(prCtx, "EnsureMergeRequest", key.Repo, pr.Number)
			prBeadID, alreadyClosed, err := bdc.EnsureMergeRequest(bdCtx, pr.URL, fields)
			recordSpanErr(bdSpan, err)
			bdSpan.End()
			if err != nil {
				telemetry.SyncErrorsTotal.WithLabelValues(key.Repo).Inc()
				recordSpanErr(prSpan, err)
				summary.Errors = append(summary.Errors, SummaryError{
					Repo:    key.Repo,
					Message: fmt.Sprintf("PR #%d: %v", pr.Number, err),
				})
				return
			}
			if alreadyClosed {
				return
			}
			if _, was := repoPreExisting[key.Repo][key]; was {
				summary.BeadsUpdated++
			} else {
				summary.BeadsCreated++
			}

			// Phase 3: drive feedback + draft auto-promote pipelines for the PR.
			if err := e.processFeedback(prCtx, bdc, key.Repo, pr, prBeadID, summary); err != nil {
				telemetry.SyncErrorsTotal.WithLabelValues(key.Repo).Inc()
				recordSpanErr(prSpan, err)
				summary.Errors = append(summary.Errors, SummaryError{
					Repo:    key.Repo,
					Message: fmt.Sprintf("PR #%d feedback: %v", pr.Number, err),
				})
			}
			if err := e.maybePromoteDraft(prCtx, bdc, key.Repo, pr, prBeadID, summary); err != nil {
				telemetry.SyncErrorsTotal.WithLabelValues(key.Repo).Inc()
				recordSpanErr(prSpan, err)
				summary.Errors = append(summary.Errors, SummaryError{
					Repo:    key.Repo,
					Message: fmt.Sprintf("PR #%d draft-promote: %v", pr.Number, err),
				})
			}
		}()
	}

	// Phase 6 B3: process queued replies (LLM stored reply_draft; we post +
	// record response_id). Runs once per healthy repo — the helper filters
	// feedback beads to only those whose merge-request belongs to that repo.
	for repo := range healthyRepos {
		rcfg, ok := repoCfgs[repo]
		if !ok {
			continue
		}
		bdc := repoClients[repo]
		if bdc == nil {
			continue
		}
		if err := e.processReplyDrafts(ctx, bdc, rcfg, summary); err != nil {
			summary.Errors = append(summary.Errors, SummaryError{
				Repo:    repo,
				Message: fmt.Sprintf("reply pipeline: %v", err),
			})
		}
	}

	// Close beads whose PR is no longer in the observed set, but only
	// among repos that synced successfully. This prevents us from closing
	// beads in a repo whose sync failed (we'd have no authoritative view).
	//
	// Each repo's bd workspace is queried independently via its own
	// per-repo client — otherwise we'd miss beads that live in a sibling
	// monorepo's workspace.
	for repo := range healthyRepos {
		bdc := repoClients[repo]
		if bdc == nil {
			continue
		}
		all, err := bdc.ListMergeRequests(ctx, false /* open only */)
		if err != nil {
			summary.Errors = append(summary.Errors, SummaryError{
				Repo:    repo,
				Message: fmt.Sprintf("list open beads: %v", err),
			})
			continue
		}
		for _, mr := range all {
			// Defensive: a repo's workspace should only hold beads tagged
			// with its own remote, but old data may leak. Skip beads that
			// don't match this repo.
			if mr.Fields.Repo != repo {
				continue
			}
			k := prKey{Repo: mr.Fields.Repo, Number: mr.Fields.PRNumber}
			if _, watched := observed[k]; watched {
				continue
			}
			if err := bdc.CloseMergeRequest(ctx, mr.ID, "upstream-not-watched"); err != nil {
				summary.Errors = append(summary.Errors, SummaryError{
					Repo:    mr.Fields.Repo,
					Message: fmt.Sprintf("close stale bead %s: %v", mr.ID, err),
				})
				continue
			}
			summary.BeadsClosed++
			// Cascade: close all descendants (processing-cycles, feedback,
			// actions) with reason pr-closed.
			e.cascadeClose(ctx, bdc, mr.ID, "pr-closed", summary)
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
	// Record last-successful-sync per repo. Only the repos whose
	// enumeration succeeded are eligible — partial-failure repos are
	// skipped so dashboards don't show a misleadingly fresh timestamp.
	for repo := range healthyRepos {
		telemetry.ObserveSyncSuccess(repo, summary.FinishedAt)
	}
	// Dashboard snapshot: gather per-PR extras (reviews, comments, CI runs,
	// bd dep tree) and store. Best-effort — errors during gathering are
	// absorbed so a partial snapshot still lands.
	if e.deps.Snapshot != nil {
		e.buildAndStoreSnapshot(ctx, observed, repoClients)
	}
	if len(summary.Errors) > 0 {
		return summary, fmt.Errorf("sync: %d error(s) (see Summary.Errors)", len(summary.Errors))
	}
	return summary, nil
}

// buildAndStoreSnapshot gathers per-PR extras (reviews, comments, CI runs,
// bd dep tree) and stores a snapshot. Errors during gathering are absorbed
// so partial snapshots are acceptable. When no PRs were observed the
// previous snapshot is preserved (no overwrite).
func (e *Engine) buildAndStoreSnapshot(ctx context.Context, observed map[prKey]api.PR, repoClients map[string]BeadClient) {
	if len(observed) == 0 {
		return
	}
	inputs := make([]snapshot.PRInput, 0, len(observed))
	for key, pr := range observed {
		// Ensure pr.Repo carries the configured remote — VCS providers may
		// omit it on the returned api.PR (the watched-set key holds the
		// authoritative repo identifier).
		if pr.Repo == "" {
			pr.Repo = key.Repo
		}
		in := snapshot.PRInput{PR: pr}
		rcfg, rerr := e.repoConfig(key.Repo)
		if rerr == nil {
			if vp, err := e.providerFor(rcfg); err == nil {
				if rl, ok := vp.(ReviewLister); ok {
					if reviews, rrErr := rl.ListReviews(ctx, key.Repo, pr.Number); rrErr == nil {
						in.Reviews = reviews
					}
				}
				if reader, ok := vp.(CommentReader); ok {
					if comments, cerr := reader.ListComments(ctx, key.Repo, pr.Number); cerr == nil {
						in.Comments = comments
					}
				}
			}
			if cp := e.firstCICDFor(rcfg); cp != nil {
				// Prefer the branch-known path (ghactions.Provider.ListRunsByBranch)
				// when the provider supports it and api.PR carries the head branch —
				// avoids one `gh pr view` per PR per tick.
				if bl, ok := cp.(CICDBranchLister); ok && strings.TrimSpace(pr.Branch) != "" {
					if runs, cerr := bl.ListRunsByBranch(ctx, key.Repo, pr.Branch); cerr == nil {
						in.CIRuns = runs
					}
				} else if runs, cerr := cp.ListRuns(ctx, key.Repo, pr.Number); cerr == nil {
					in.CIRuns = runs
				}
			}
		}
		// bd dep tree via per-repo client. The lookup is best-effort and
		// requires the concrete *beads.Client (test fakes don't implement
		// DepTreeUp); when the assertion fails we just skip deps for this
		// PR.
		bdc := repoClients[key.Repo]
		if bdc == nil {
			bdc = e.bdClientFor(rcfg)
		}
		if c, ok := bdc.(*beads.Client); ok {
			if mr, ferr := c.FindByRepoAndNumber(ctx, key.Repo, pr.Number); ferr == nil && mr != nil {
				if deps, derr := c.DepTreeUp(ctx, mr.ID); derr == nil {
					in.BeadsDeps = deps
				}
			}
		}
		// JIRA — left empty for v1; downstream task wires this from
		// feedback beads.
		inputs = append(inputs, in)
	}

	snap := snapshot.Build(snapshot.BuilderInput{
		GeneratedAt:         e.deps.Now(),
		SyncIntervalSeconds: int(e.deps.SyncInterval.Seconds()),
		Self:                e.deps.Cfg.SelfLogin,
		TeamMembers:         e.allTeamMembers(),
		Registry:            e.deps.AgentRegistry,
		PRs:                 inputs,
	})
	e.deps.Snapshot.Set(snap)
	telemetry.SnapshotPresent.Set(1)
}

// allTeamMembers returns the de-duplicated union of TeamMembers across all
// configured repos. The configured self login is included so a PR authored
// by self is still classified into the mine row (the builder treats self
// and team separately, but having self in the team set is harmless because
// the builder's switch checks self first).
func (e *Engine) allTeamMembers() []string {
	seen := map[string]struct{}{}
	var out []string
	for _, r := range e.deps.Cfg.Repos {
		for _, m := range r.TeamMembers {
			if _, ok := seen[m]; ok {
				continue
			}
			seen[m] = struct{}{}
			out = append(out, m)
		}
	}
	return out
}

// isSelfAuthored reports whether the given GitHub login matches the
// configured SelfLogin. Empty self or empty author → false (assume
// team-mate; do not modify upstream). Centralizes the ownership
// predicate used by sync's upstream-write guards.
func (e *Engine) isSelfAuthored(author string) bool {
	self := e.deps.Cfg.SelfLogin
	return self != "" && author != "" && author == self
}

// firstCICDFor returns the first CICD provider configured for rcfg, or nil
// when none is configured or registered. Matches the lookup pattern used
// by processFeedback.
func (e *Engine) firstCICDFor(rcfg config.RepoConfig) CICDProvider {
	for _, name := range rcfg.CICD {
		if cp, ok := e.deps.CICD[name]; ok {
			return cp
		}
	}
	return nil
}

// SetDashboardStore wires a snapshot Store and the daemon's sync interval
// into the engine. Called by Daemon at startup so snapshot building is
// active for the loop's lifetime. Safe to call only before Sync goroutines
// are active (i.e. once, before the loop starts).
func (e *Engine) SetDashboardStore(store *snapshot.Store, interval time.Duration) {
	e.deps.Snapshot = store
	e.deps.SyncInterval = interval
}

// SetAgentRegistry wires the agent registry onto the engine. Called by CLI
// daemon-mode setup so the snapshot builder can classify approvals. Safe
// to call only before Sync goroutines are active.
func (e *Engine) SetAgentRegistry(reg *agentregistry.Registry) {
	e.deps.AgentRegistry = reg
}

// SyncPR refreshes a single PR. The repo identifier MUST be in the configured
// list (the engine can only sync repos with VCS provider config).
//
// The bd client used here is scoped to the repo's monorepo path so writes
// land in that workspace's `.beads/` — regardless of where pg-pr was invoked
// from. Tests that inject Deps.Beads continue to route through that client.
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
	bdc := e.bdClientFor(rcfg)

	summary := &Summary{StartedAt: e.deps.Now()}
	getCtx, getSpan := startVCSSpan(ctx, "GetPR", repo, number)
	pr, err := provider.GetPR(getCtx, repo, number)
	recordSpanErr(getSpan, err)
	getSpan.End()
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
		existing, err := e.findBeadByPR(ctx, bdc, repo, pr.Number)
		if err != nil {
			return summary, err
		}
		if existing != nil {
			reason := "upstream-" + pr.State
			if pr.Merged {
				reason = "upstream-merged"
			}
			if err := bdc.CloseMergeRequest(ctx, existing.ID, reason); err != nil {
				summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: err.Error()})
				summary.FinishedAt = e.deps.Now()
				return summary, err
			}
			summary.BeadsClosed = 1
			e.cascadeClose(ctx, bdc, existing.ID, "pr-closed", summary)
		}
		summary.Repos = []RepoSummary{{Repo: repo, PRs: 1}}
		summary.TotalPRs = 1
		summary.FinishedAt = e.deps.Now()
		return summary, nil
	}

	prBeadID, alreadyClosed, err := bdc.EnsureMergeRequest(ctx, pr.URL, fields)
	if err != nil {
		summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: err.Error()})
		summary.FinishedAt = e.deps.Now()
		return summary, err
	}
	if !alreadyClosed {
		summary.BeadsUpdated = 1
		// Phase 3: feedback + draft pipelines.
		if err := e.processFeedback(ctx, bdc, repo, *pr, prBeadID, summary); err != nil {
			summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: err.Error()})
		}
		if err := e.maybePromoteDraft(ctx, bdc, repo, *pr, prBeadID, summary); err != nil {
			summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: err.Error()})
		}
		// Phase 6 B3: post queued replies for feedback beads under this repo.
		if err := e.processReplyDrafts(ctx, bdc, rcfg, summary); err != nil {
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

	myCtx, mySpan := startVCSSpan(ctx, "ListMyPRs", rcfg.Remote, 0)
	myPRs, err := provider.ListMyPRs(myCtx, rcfg.Remote)
	recordSpanErr(mySpan, err)
	mySpan.End()
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
		teamCtx, teamSpan := startVCSSpan(ctx, "ListTeamPRs", rcfg.Remote, 0)
		teamPRs, err := provider.ListTeamPRs(teamCtx, rcfg.Remote, rcfg.TeamMembers)
		recordSpanErr(teamSpan, err)
		teamSpan.End()
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

// listExistingByKey indexes all open merge-request beads from the given
// per-repo bd client by (repo, pr_number). Caller-scoped: callers pass the
// bd client whose workspace they want indexed.
func (e *Engine) listExistingByKey(ctx context.Context, bdc BeadClient) (map[prKey]beads.MergeRequest, error) {
	all, err := bdc.ListMergeRequests(ctx, false)
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
// (repo, pr_number) or nil if not found. Caller passes the bd client whose
// workspace should be searched.
func (e *Engine) findBeadByPR(ctx context.Context, bdc BeadClient, repo string, pr int) (*beads.MergeRequest, error) {
	all, err := bdc.ListMergeRequests(ctx, true)
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
//
// bdc is the per-repo bd client used for all feedback / processing-cycle
// operations — bound to the monorepo's bd workspace by the caller.
func (e *Engine) processFeedback(ctx context.Context, bdc BeadClient, repo string, pr api.PR, prBeadID string, summary *Summary) error {
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
		commCtx, commSpan := startVCSSpan(ctx, "ListComments", repo, pr.Number)
		comments, err := reader.ListComments(commCtx, repo, pr.Number)
		recordSpanErr(commSpan, err)
		commSpan.End()
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
		ciCtx, ciSpan := startCICDSpan(ctx, "ListRuns", repo, pr.Number, "")
		runs, err := cp.ListRuns(ciCtx, repo, pr.Number)
		recordSpanErr(ciSpan, err)
		ciSpan.End()
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
	cycleID, found, err := bdc.FindOpenProcessingCycle(ctx, prBeadID)
	if err != nil {
		return fmt.Errorf("find processing-cycle: %w", err)
	}

	// First pass: handle CI events whose conclusion is success and close
	// any matching prior ci-failure feedback (resolved-upstream).
	if found {
		open, err := bdc.ListFeedback(ctx, cycleID, false)
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
						_ = bdc.MarkFeedbackResolvedUpstream(ctx, fb.ID)
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
		existing, err := e.findFeedbackForPR(ctx, bdc, prBeadID, ev.fingerprint)
		if err != nil {
			continue
		}
		if existing != nil {
			continue
		}

		if !found {
			// Lazily create a processing-cycle on first new feedback.
			id, err := bdc.CreateProcessingCycle(ctx, prBeadID, fmt.Sprintf("%s#%d", repo, pr.Number))
			if err != nil {
				return fmt.Errorf("create processing-cycle: %w", err)
			}
			cycleID = id
			found = true
			summary.CyclesCreated++
		}

		bdCtx, bdSpan := startBeadsSpan(ctx, "CreateFeedback", repo, pr.Number)
		_, err = bdc.CreateFeedback(bdCtx, beads.CreateFeedbackInput{
			ProcessingCycleID: cycleID,
			Kind:              ev.kind,
			ExternalID:        ev.externalID,
			Fingerprint:       ev.fingerprint,
			AuthorRole:        ev.authorRole,
			Title:             ev.title,
			Body:              ev.body,
		})
		recordSpanErr(bdSpan, err)
		bdSpan.End()
		if err != nil {
			return fmt.Errorf("create feedback: %w", err)
		}
		summary.FeedbackCreated++
		telemetry.FeedbackCreatedTotal.WithLabelValues(repo, string(ev.kind)).Inc()
	}
	return nil
}

// findFeedbackForPR searches every processing-cycle under prBeadID for a
// feedback bead with the given fingerprint. Returns nil when fingerprint
// is empty or no match is found. Uses the supplied bd client so the search
// targets the right monorepo workspace.
func (e *Engine) findFeedbackForPR(ctx context.Context, bdc BeadClient, prBeadID, fingerprint string) (*beads.Feedback, error) {
	if fingerprint == "" {
		return nil, nil
	}
	// We don't currently index cycles per PR cheaply; brute-force list
	// feedback under any cycle linked from prBeadID. ListChildrenOfPR
	// returns processing-cycles + action beads — we check each.
	children, err := bdc.ListChildrenOfPR(ctx, prBeadID)
	if err != nil {
		return nil, err
	}
	for _, childID := range children {
		fb, err := bdc.FindFeedbackByFingerprint(ctx, childID, fingerprint)
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
// are green, promotes the PR to ready. bdc is the per-repo bd client whose
// workspace holds the merge-request bead being updated.
func (e *Engine) maybePromoteDraft(ctx context.Context, bdc BeadClient, repo string, pr api.PR, prBeadID string, summary *Summary) error {
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
		ciCtx, ciSpan := startCICDSpan(ctx, "ListRuns", repo, pr.Number, "")
		runs, err := cp.ListRuns(ciCtx, repo, pr.Number)
		recordSpanErr(ciSpan, err)
		ciSpan.End()
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
	_ = bdc.UpdateMergeRequest(ctx, prBeadID, beads.MergeRequestFields{
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
//
// bdc is the per-repo bd client whose workspace is queried for pending
// replies. Beads belonging to other repos are filtered out (defensive
// guard in case clients share a workspace).
func (e *Engine) processReplyDrafts(ctx context.Context, bdc BeadClient, rcfg config.RepoConfig, summary *Summary) error {
	provider, err := e.providerFor(rcfg)
	if err != nil {
		return nil
	}
	replier, ok := provider.(ThreadReplier)
	if !ok {
		// No reply capability — skip silently (e.g., a Phase 0 stub provider).
		return nil
	}
	pending, err := bdc.ListFeedbackPendingReply(ctx)
	if err != nil {
		return fmt.Errorf("list pending replies: %w", err)
	}
	for _, fb := range pending {
		// Walk up: feedback → processing-cycle → merge-request.
		mr, err := bdc.FindMergeRequestForFeedback(ctx, fb.ID)
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
		if err := bdc.SetResponseID(ctx, fb.ID, respID); err != nil {
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
// bdc is the per-repo bd client whose workspace holds the descendants.
func (e *Engine) cascadeClose(ctx context.Context, bdc BeadClient, prBeadID, reason string, summary *Summary) {
	children, err := bdc.ListChildrenOfPR(ctx, prBeadID)
	if err != nil {
		return
	}
	for _, childID := range children {
		// Close as a feedback bead first (works for feedback bd type) — if
		// that fails because the bead is a task or action, fall back to a
		// generic close via the same wrapper.
		_ = bdc.CloseFeedback(ctx, childID, reason)
		_ = bdc.CloseProcessingCycle(ctx, childID, reason)
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

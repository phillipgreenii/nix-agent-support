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
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/marker"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/telemetry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
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

	CreateProcessingCycle(ctx context.Context, prBeadID, title string, mine bool) (string, error)
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

	// Store is the SQLite-backed event store. Optional: when nil, outbox
	// flushing is a no-op and no events are persisted. Tests that don't need
	// event plumbing may omit this field.
	Store *store.DB

	// Dispatch is the function used to dispatch outbox events to registered
	// handlers. Optional: when nil (or when Store is nil), flushOutbox is a
	// no-op. Typically set to (*event.Dispatcher).Dispatch.
	Dispatch store.DispatchFunc
}

// Engine carries the configured dependencies for a series of sync calls.
type Engine struct {
	deps Deps
	cfgP atomic.Pointer[config.Config]

	// prevMine / prevTeam hold the previous-tick fingerprint rosters for the
	// daemon's detector (keyed by prKey, value = fingerprint hash). Only the
	// daemon loop goroutine touches them, so no lock is needed.
	prevMine map[prKey]string
	prevTeam map[prKey]string

	// authFailStreak counts consecutive fingerprint ticks whose polls failed
	// with an auth-invalid error. Only the daemon loop goroutine touches it
	// (via fingerprintTick), so no lock is needed. The daemon escalates a
	// restart-to-refresh once it crosses maxAuthFailStreak.
	authFailStreak int

	// humanLabels is the per-repo set of bead IDs carrying the `human` label
	// (repo -> set). Refreshed off the critical path by the daemon's
	// maintenance goroutine (refreshHumanLabels) and read by workers in
	// buildPRInput's cache-less branch. A *map is stored so the read is a
	// single atomic load; the stored map is never mutated after Store.
	humanLabels atomic.Pointer[map[string]map[string]bool]
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
	e := &Engine{deps: d}
	e.cfgP.Store(d.Cfg)
	return e, nil
}

// cfg returns the engine's current config. Reads are atomic so the daemon's
// detector/workers/owner can read while SIGHUP swaps via ReplaceCfg. Falls back
// to the seed deps.Cfg when cfgP was never stored — engines built as struct
// literals (some tests) skip New, and deps.Cfg is never mutated after New so
// the fallback is race-safe.
func (e *Engine) cfg() *config.Config {
	if c := e.cfgP.Load(); c != nil {
		return c
	}
	return e.deps.Cfg
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
	// Warnings are advisory diagnostics about local state that shouldn't
	// exist (e.g. ReplyDraft staged on a team-mate's PR). Unlike Errors,
	// Warnings do not affect SyncErrorsTotal telemetry or repoStates[].LastError.
	Warnings []SummaryError `json:"warnings,omitempty"`
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
	// Per-repo per-PR bulk-fetched enrichment (reviews, comments, CI
	// runs). Populated only when the VCS provider supports
	// EnrichedPRsProvider; downstream readers fall through to per-PR REST
	// calls when an entry is missing.
	enrichByRepo := map[string]map[int]vcs.EnrichedPR{}

	for _, rcfg := range e.cfg().Repos {
		func() {
			repoCtx, repoSpan := startRepoSpan(ctx, rcfg.Remote)
			defer repoSpan.End()

			rs := RepoSummary{Repo: rcfg.Remote}
			state := repoState{LastSyncedAt: e.deps.Now().UTC().Format(time.RFC3339)}

			prs, enriched, err := e.enumerate(repoCtx, rcfg)
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
			if enriched != nil {
				enrichByRepo[rcfg.Remote] = enriched
			}
			// Pre-index existing open beads for this repo so the
			// create-vs-update counters reflect this workspace only.
			if pre, perr := e.listExistingByKey(repoCtx, bdc); perr == nil {
				repoPreExisting[rcfg.Remote] = pre
			}
		}()
	}

	// Partition by ownership BEFORE running per-PR phases. Local-bead
	// phases (EnsureMergeRequest, processFeedback) run for both subsets;
	// upstream-write phases (maybePromoteDraft) only run for mine.
	// Empty Author / empty SelfLogin → treated as team (do not modify
	// upstream). Future write-side phases must consciously consult
	// mineSet — defense against the original bug class.
	mineSet := make(map[prKey]bool, len(observed))
	for key, pr := range observed {
		if e.isSelfAuthored(pr.Author) {
			mineSet[key] = true
		}
	}

	// Bulk-fetch per-repo lookup tables (TickCache) before the per-PR
	// loop. Each cache replaces several bd calls per PR (open processing
	// cycles, feedback-by-cycle, human labels). Test-injected bd clients
	// don't implement *beads.Client, so cacheByRepo only gets entries
	// for real clients — the per-PR helpers fall back to live calls when
	// no cache is present.
	cachesByRepo := make(map[string]*beads.TickCache, len(repoClients))
	for repo, bdc := range repoClients {
		if c, ok := bdc.(*beads.Client); ok {
			cachesByRepo[repo] = c.LoadTickCache(ctx)
		}
	}

	// Upsert beads for each observed PR. Each PR is dispatched against its
	// own monorepo's bd client.
	for key, pr := range observed {
		func() {
			prCtx, prSpan := startPRSpan(ctx, key.Repo, pr.Number, pr.Author)
			defer prSpan.End()
			startedAt := e.deps.Now()
			group := "team"
			if mineSet[key] {
				group = "mine"
			}
			defer func() {
				telemetry.SyncPRDuration.
					WithLabelValues(key.Repo, group).
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

			// Pluck this PR's bulk-fetched VCS data (reviews/comments/CI
			// runs) if the EnrichedPRs path populated the cache; nil means
			// downstream helpers fall back to per-PR REST calls.
			var prEnriched *vcs.EnrichedPR
			if byNum := enrichByRepo[key.Repo]; byNum != nil {
				if ep, ok := byNum[pr.Number]; ok {
					prEnriched = &ep
				}
			}
			// Phase 3: drive feedback + draft auto-promote pipelines for the PR.
			if err := e.processFeedback(prCtx, bdc, cachesByRepo[key.Repo], prEnriched, key.Repo, pr, prBeadID, summary); err != nil {
				telemetry.SyncErrorsTotal.WithLabelValues(key.Repo).Inc()
				recordSpanErr(prSpan, err)
				summary.Errors = append(summary.Errors, SummaryError{
					Repo:    key.Repo,
					Message: fmt.Sprintf("PR #%d feedback: %v", pr.Number, err),
				})
			}
			// Upstream-write phase: only for self-authored PRs.
			// See partition above.
			if mineSet[key] {
				if err := e.maybePromoteDraft(prCtx, bdc, prEnriched, key.Repo, pr, prBeadID, summary); err != nil {
					telemetry.SyncErrorsTotal.WithLabelValues(key.Repo).Inc()
					recordSpanErr(prSpan, err)
					summary.Errors = append(summary.Errors, SummaryError{
						Repo:    key.Repo,
						Message: fmt.Sprintf("PR #%d draft-promote: %v", pr.Number, err),
					})
				}
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
	// Dashboard snapshot: gather per-PR extras (reviews, comments, CI runs,
	// bd dep tree) and store. Best-effort — errors during gathering are
	// absorbed so a partial snapshot still lands.
	if e.deps.Snapshot != nil {
		e.buildAndStoreSnapshot(ctx, observed, repoClients, cachesByRepo, enrichByRepo)
	}
	flushOutbox(ctx, e.deps.Store, e.deps.Dispatch)
	if len(summary.Errors) > 0 {
		return summary, fmt.Errorf("sync: %d error(s) (see Summary.Errors)", len(summary.Errors))
	}
	return summary, nil
}

// buildAndStoreSnapshot gathers per-PR extras (reviews, comments, CI runs,
// bd dep tree) and stores a snapshot. Errors during gathering are absorbed
// so partial snapshots are acceptable. When no PRs were observed the
// previous snapshot is preserved (no overwrite).
//
// cachesByRepo supplies pre-fetched bd lookup tables (human labels,
// merge-request index). enrichByRepo supplies bulk-fetched VCS data
// (reviews, comments, CI runs) when the provider supports GraphQL
// EnrichedPRs. Repos without a cache or enrichment (test injections,
// providers without the capability, GraphQL failures) fall back to
// per-PR live calls.
func (e *Engine) buildAndStoreSnapshot(ctx context.Context, observed map[prKey]api.PR, repoClients map[string]BeadClient, cachesByRepo map[string]*beads.TickCache, enrichByRepo map[string]map[int]vcs.EnrichedPR) {
	if len(observed) == 0 {
		return
	}
	inputs := make([]snapshot.PRInput, 0, len(observed))
	for key, pr := range observed {
		// Resolve the per-PR config the same way as the old inline block:
		// repoConfig(remote) lookup, falling back to an empty RepoConfig
		// (carrying the remote) on miss so buildPRInput still stamps pr.Repo.
		rcfg, rerr := e.repoConfig(key.Repo)
		if rerr != nil {
			rcfg = config.RepoConfig{Remote: key.Repo}
		}
		// Snapshot enrichment: prefer the bulk-fetched data populated by
		// EnrichedPRsProvider; only fall back to per-PR REST calls when
		// the bulk fetch wasn't available (test fakes, GraphQL error,
		// provider without the capability).
		var enriched *vcs.EnrichedPR
		if byNum := enrichByRepo[key.Repo]; byNum != nil {
			if ep, ok := byNum[pr.Number]; ok {
				enriched = &ep
			}
		}
		bdc := repoClients[key.Repo]
		if bdc == nil {
			bdc = e.bdClientFor(rcfg)
		}
		in := e.buildPRInput(ctx, pr, enriched, bdc, cachesByRepo[key.Repo], rcfg, "")
		// JIRA — left empty for v1; downstream task wires this from
		// feedback beads.
		inputs = append(inputs, in)
	}

	snap := snapshot.Build(snapshot.BuilderInput{
		GeneratedAt:         e.deps.Now(),
		SyncIntervalSeconds: int(e.deps.SyncInterval.Seconds()),
		Self:                e.cfg().SelfLogin,
		TeamMembers:         e.allTeamMembers(),
		Registry:            e.deps.AgentRegistry,
		PRs:                 inputs,
	})
	e.deps.Snapshot.Set(snap)
	telemetry.SnapshotPresent.Set(1)
}

// depTreeReader is the subset of *beads.Client that buildPRInput needs for the
// dep-tree + human-label overlay. Asserting an interface (not the concrete
// *beads.Client) keeps buildPRInput unit-testable; the real client satisfies it.
type depTreeReader interface {
	FindByRepoAndNumber(ctx context.Context, repo string, number int) (*beads.MergeRequest, error)
	DepTreeUp(ctx context.Context, rootID string) ([]beads.DepNode, error)
	HumanLabeledBeads(ctx context.Context) (map[string]bool, error)
}

// humanLabelReader is the narrow capability the maintenance goroutine needs to
// pull the workspace's `human`-labeled bead set. The real *beads.Client
// satisfies it; test-injected clients that don't are skipped.
type humanLabelReader interface {
	HumanLabeledBeads(ctx context.Context) (map[string]bool, error)
}

// feedbackSubtreeReader is the narrow capability for reading every feedback
// bead in a PR's recursive parent-child subtree in one scoped bd call. The
// real *beads.Client satisfies it; test fakes that don't are treated as "no
// existing feedback" (empty slice).
type feedbackSubtreeReader interface {
	PRFeedbackInSubtree(ctx context.Context, prBeadID string) ([]beads.Feedback, error)
}

// humanLabelsFor returns the last-pulled `human`-label set for repo, or nil if
// no pull has populated it yet. Safe to call from any goroutine.
func (e *Engine) humanLabelsFor(repo string) map[string]bool {
	m := e.humanLabels.Load()
	if m == nil {
		return nil
	}
	return (*m)[repo]
}

// refreshHumanLabels pulls the `human`-labeled bead set for every configured
// repo and atomically publishes the result for workers to read. A per-repo
// pull error preserves that repo's previous set (no flicker); a repo whose
// client lacks HumanLabeledBeads (test injection) is skipped. Runs on the
// maintenance goroutine only.
func (e *Engine) refreshHumanLabels(ctx context.Context) {
	out := map[string]map[string]bool{}
	for _, rcfg := range e.cfg().Repos {
		reader, ok := e.bdClientFor(rcfg).(humanLabelReader)
		if !ok {
			continue
		}
		set, err := reader.HumanLabeledBeads(ctx)
		if err != nil {
			if prev := e.humanLabelsFor(rcfg.Remote); prev != nil {
				out[rcfg.Remote] = prev
			}
			continue
		}
		out[rcfg.Remote] = set
	}
	e.humanLabels.Store(&out)
}

// enrichOnePR fetches one PR's reviews, comments, and CI runs via focused
// per-PR provider calls and bundles them into a *vcs.EnrichedPR so the feedback
// pipeline (processFeedback / maybePromoteDraft) and the snapshot builder
// (buildPRInput) share a single fetch instead of each issuing its own. CI runs
// come from the first configured CICD provider, preferring the branch-known
// path (matching buildPRInput's existing cache-less CI behavior). Providers
// lacking an optional capability leave the corresponding field empty.
func (e *Engine) enrichOnePR(ctx context.Context, rcfg config.RepoConfig, pr api.PR) *vcs.EnrichedPR {
	if pr.Repo == "" {
		pr.Repo = rcfg.Remote
	}
	out := vcs.EnrichedPR{PR: pr}
	if vp, err := e.providerFor(rcfg); err == nil {
		if rl, ok := vp.(ReviewLister); ok {
			if reviews, rerr := rl.ListReviews(ctx, pr.Repo, pr.Number); rerr == nil {
				out.Reviews = reviews
			}
		}
		if reader, ok := vp.(CommentReader); ok {
			if comments, cerr := reader.ListComments(ctx, pr.Repo, pr.Number); cerr == nil {
				out.Comments = comments
			}
		}
	}
	if cp := e.firstCICDFor(rcfg); cp != nil {
		if bl, ok := cp.(CICDBranchLister); ok && strings.TrimSpace(pr.Branch) != "" {
			if runs, cerr := bl.ListRunsByBranch(ctx, pr.Repo, pr.Branch); cerr == nil {
				out.CIRuns = runs
			}
		} else if runs, cerr := cp.ListRuns(ctx, pr.Repo, pr.Number); cerr == nil {
			out.CIRuns = runs
		}
	}
	return &out
}

// buildPRInput assembles the per-PR snapshot input for one observed PR:
// reviews/comments/CI runs (from bulk enrichment when present, else per-PR
// REST) plus the bd dep tree with `human` labels overlaid.
//
// Inputs:
//   - enriched, when non-nil, supplies reviews/comments/CI runs from the
//     per-repo GraphQL bulk fetch; otherwise the helper falls back to per-PR
//     ReviewLister/CommentReader/CICD calls (providers lacking those optional
//     capabilities are tolerated — those fields stay empty).
//   - bdc is the per-repo bd client. The dep-tree path runs whenever a cache
//     is present OR bdc satisfies depTreeReader (the real *beads.Client does;
//     test fakes may inject it explicitly).
//   - cache, when non-nil (full-sync path), answers the merge-request lookup,
//     dep tree, and `human` label overlay from the per-tick bulk fetch. When
//     nil (daemon per-PR refresh), the `human` overlay is read from the
//     engine's atomic label set (e.humanLabelsFor) — refreshed off the hot
//     path by the maintenance goroutine — so WaitingOnMe does not regress.
//   - knownMRID, when non-empty, is the merge-request bead id the caller
//     already holds; it short-circuits both the cache lookup and the live
//     FindByRepoAndNumber. Pass "" when the id isn't known.
//   - rcfg carries this PR's repo config; its Remote stamps pr.Repo when the
//     VCS provider omitted it.
func (e *Engine) buildPRInput(ctx context.Context, pr api.PR, enriched *vcs.EnrichedPR, bdc BeadClient, cache *beads.TickCache, rcfg config.RepoConfig, knownMRID string) snapshot.PRInput {
	// Ensure pr.Repo carries the configured remote — VCS providers may omit
	// it on the returned api.PR.
	if pr.Repo == "" {
		pr.Repo = rcfg.Remote
	}
	in := snapshot.PRInput{PR: pr}

	// --- reviews/comments/CI runs ---
	if enriched != nil {
		in.Reviews = enriched.Reviews
		in.Comments = enriched.Comments
		in.CIRuns = enriched.CIRuns
	} else {
		if vp, err := e.providerFor(rcfg); err == nil {
			if rl, ok := vp.(ReviewLister); ok {
				if reviews, rrErr := rl.ListReviews(ctx, pr.Repo, pr.Number); rrErr == nil {
					in.Reviews = reviews
				}
			}
			if reader, ok := vp.(CommentReader); ok {
				if comments, cerr := reader.ListComments(ctx, pr.Repo, pr.Number); cerr == nil {
					in.Comments = comments
				}
			}
		}
		if cp := e.firstCICDFor(rcfg); cp != nil {
			// Prefer the branch-known path (ghactions.Provider.ListRunsByBranch)
			// when the provider supports it and api.PR carries the head branch —
			// avoids one `gh pr view` per PR per tick.
			if bl, ok := cp.(CICDBranchLister); ok && strings.TrimSpace(pr.Branch) != "" {
				if runs, cerr := bl.ListRunsByBranch(ctx, pr.Repo, pr.Branch); cerr == nil {
					in.CIRuns = runs
				}
			} else if runs, cerr := cp.ListRuns(ctx, pr.Repo, pr.Number); cerr == nil {
				in.CIRuns = runs
			}
		}
	}

	// --- dep tree + human labels ---
	// The dep path runs whenever a cache is present (full-sync) or bdc
	// satisfies depTreeReader (real *beads.Client, or a test fake). Test
	// fakes that don't implement depTreeReader fall through with no deps.
	reader, hasReader := bdc.(depTreeReader)
	if cache != nil || hasReader {
		var mrID string
		switch {
		case knownMRID != "":
			mrID = knownMRID
		case cache != nil:
			if mr, found := cache.FindMergeRequest(pr.Repo, pr.Number); found {
				mrID = mr.ID
			}
		}
		if mrID == "" && hasReader {
			if mr, ferr := reader.FindByRepoAndNumber(ctx, pr.Repo, pr.Number); ferr == nil && mr != nil {
				mrID = mr.ID
			}
		}
		if mrID != "" {
			// Cached dep tree first; live DepTreeUp only when the
			// workspace-wide bulk fetch wasn't available for this PR.
			// cacheHit distinguishes "cache has this PR (possibly with zero
			// deps) — authoritative, skip the live call" from "cache miss —
			// fall back to DepTreeUp", matching the pre-refactor semantics
			// of cache.DepsUpFor's ok return.
			var deps []beads.DepNode
			cacheHit := false
			if cache != nil {
				if cached, ok := cache.DepsUpFor(mrID); ok {
					deps = cached
					cacheHit = true
				}
			}
			if !cacheHit && hasReader {
				if live, derr := reader.DepTreeUp(ctx, mrID); derr == nil {
					deps = live
				}
			}
			// Human-label overlay: from the cache on the full-sync path
			// (the cache != nil branch wins so HumanLabeledBeads is NOT
			// re-fetched); else fetch it live so the cache-less daemon
			// per-PR refresh path still applies `human`.
			if cache != nil {
				beads.ApplyHumanLabels(deps, cache.HumanLabeled)
			} else {
				beads.ApplyHumanLabels(deps, e.humanLabelsFor(pr.Repo))
			}
			in.BeadsDeps = deps
		}
	}
	return in
}

// allTeamMembers returns the de-duplicated union of TeamMembers across all
// configured repos. The configured self login is included so a PR authored
// by self is still classified into the mine row (the builder treats self
// and team separately, but having self in the team set is harmless because
// the builder's switch checks self first).
func (e *Engine) allTeamMembers() []string {
	seen := map[string]struct{}{}
	var out []string
	for _, r := range e.cfg().Repos {
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
	self := e.cfg().SelfLogin
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

// SetStoreAndDispatch wires the SQLite event store and dispatcher onto the
// engine. Called by CLI setup so the outbox flush runs after each sync
// cycle. Safe to call only before Sync goroutines are active (i.e. once,
// before the loop starts). Both arguments are required — passing nil for
// either is a no-op (flushOutbox nil-guards both).
func (e *Engine) SetStoreAndDispatch(db *store.DB, dispatch store.DispatchFunc) {
	e.deps.Store = db
	e.deps.Dispatch = dispatch
}

// StoreFile returns the path that the CLI should open as the SQLite store.
// Exposed so cmd/pg-pr/sync.go can open the file before calling
// SetStoreAndDispatch, without importing the store package's path logic.
func (e *Engine) StoreFile() string { return e.storeFile() }

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
		flushOutbox(ctx, e.deps.Store, e.deps.Dispatch)
		return summary, nil
	}

	// Open PR: run the shared bead-upsert + feedback + (self) draft-promote
	// pipeline. Per-bead errors are accumulated into summary.Errors so the
	// single-PR command reports partial failures the same way it always did.
	// applyFetchedPR returns early (alreadyClosed) with err==nil when the bead
	// is already closed upstream, leaving BeadsUpdated at 0.
	var alreadyClosed bool
	_, alreadyClosed, err = e.applyFetchedPR(ctx, bdc, rcfg, pr, nil, summary)
	if err != nil {
		summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: err.Error()})
	} else if !alreadyClosed {
		// One-shot reply drain (the shared apply path no longer does it).
		if rerr := e.processReplyDrafts(ctx, bdc, rcfg, summary); rerr != nil {
			summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: rerr.Error()})
		}
	}
	summary.Repos = []RepoSummary{{Repo: repo, PRs: 1}}
	summary.TotalPRs = 1
	summary.FinishedAt = e.deps.Now()
	flushOutbox(ctx, e.deps.Store, e.deps.Dispatch)
	return summary, nil
}

// applyFetchedPR runs the bead-upsert + feedback + (self) draft-promote
// pipeline for an OPEN, active PR. Caller handles closed/merged separately.
// It is the single place the CLI one-shot (SyncPR) and the daemon per-PR
// refresh (refreshPR) share this open-PR logic.
//
// Reply draining is NOT performed here. The daemon will drain replies once
// per tick from a maintenance goroutine (a later task); the CLI SyncPR
// drains explicitly after this call returns.
//
// Returns (id, alreadyClosed, err). When EnsureMergeRequest reports the bead
// is already closed, it returns early with (id, true, nil) and does NOT bump
// summary.BeadsUpdated or run the downstream pipelines. Callers can use
// alreadyClosed to skip reply draining when the bead is closed.
//
// Note: unlike the pre-refactor SyncPR (which ran the feedback, draft-promote,
// and reply phases independently, accumulating each phase's error), this
// returns on the FIRST hard error and skips the later phases. Hard errors are
// rare — most issues are recorded into summary.Errors and return nil — and
// fail-fast is the intended shape for the daemon's per-PR refresh.
func (e *Engine) applyFetchedPR(ctx context.Context, bdc BeadClient, rcfg config.RepoConfig, pr *api.PR, enriched *vcs.EnrichedPR, summary *Summary) (string, bool, error) {
	fields := beads.MergeRequestFields{
		Repo:         rcfg.Remote,
		PRNumber:     pr.Number,
		State:        stateForPR(*pr),
		Branch:       pr.Branch,
		Base:         pr.Base,
		Author:       pr.Author,
		URL:          pr.URL,
		LastSyncedAt: e.deps.Now().UTC().Format(time.RFC3339),
		Draft:        pr.Draft,
	}
	id, alreadyClosed, err := bdc.EnsureMergeRequest(ctx, pr.URL, fields)
	if err != nil || alreadyClosed {
		return id, alreadyClosed, err
	}
	summary.BeadsUpdated = 1
	// Phase 3: feedback + draft pipelines. enriched (when non-nil) carries the
	// PR's comments/CI runs so these helpers skip their own per-PR fetches.
	if err := e.processFeedback(ctx, bdc, nil, enriched, rcfg.Remote, *pr, id, summary); err != nil {
		return id, false, err
	}
	if e.isSelfAuthored(pr.Author) {
		if err := e.maybePromoteDraft(ctx, bdc, enriched, rcfg.Remote, *pr, id, summary); err != nil {
			return id, false, err
		}
	}
	return id, false, nil
}

// enumerate lists watched PRs for a single repo. Phase 1: self + team
// members. (watch_labels is not yet honored — design notes it but Phase 1
// only needs author-based selection to be useful.)
//
// When the configured VCS provider implements EnrichedPRsProvider AND the
// repo has at least one configured author (self or team), enumerate
// short-circuits to one GraphQL search that yields both the PR list and
// the snapshot's enrichment payload (reviews, comments, CI runs). The
// returned map is keyed by PR number and is non-nil only when the bulk
// fetch succeeded; callers that miss the cache fall back to per-PR
// ListReviews/ListComments/ListRuns the same way they always did.
func (e *Engine) enumerate(ctx context.Context, rcfg config.RepoConfig) ([]api.PR, map[int]vcs.EnrichedPR, error) {
	provider, err := e.providerFor(rcfg)
	if err != nil {
		return nil, nil, err
	}
	if enriched, ok := e.tryEnumerateEnriched(ctx, provider, rcfg); ok {
		return enriched.prs, enriched.byNumber, nil
	}
	seen := map[int]struct{}{}
	out := make([]api.PR, 0)

	myCtx, mySpan := startVCSSpan(ctx, "ListMyPRs", rcfg.Remote, 0)
	myPRs, err := provider.ListMyPRs(myCtx, rcfg.Remote)
	recordSpanErr(mySpan, err)
	mySpan.End()
	if err != nil {
		return nil, nil, fmt.Errorf("list my PRs: %w", err)
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
			return nil, nil, fmt.Errorf("list team PRs: %w", err)
		}
		for _, pr := range teamPRs {
			if _, dup := seen[pr.Number]; !dup {
				seen[pr.Number] = struct{}{}
				out = append(out, pr)
			}
		}
	}
	return out, nil, nil
}

// enumeratedEnrichment is the per-repo bulk-fetch result threaded through
// the sync engine when the VCS provider supports EnrichedPRsProvider.
type enumeratedEnrichment struct {
	prs      []api.PR
	byNumber map[int]vcs.EnrichedPR
}

// tryEnumerateEnriched returns the bulk-fetched PRs + enrichment when the
// provider supports EnrichedPRsProvider AND the repo has at least one
// configured author. Returns (nil, false) on any failure so the caller
// falls back to the REST path (ListMyPRs + ListTeamPRs).
func (e *Engine) tryEnumerateEnriched(ctx context.Context, provider VCSProvider, rcfg config.RepoConfig) (*enumeratedEnrichment, bool) {
	enricher, ok := provider.(vcs.EnrichedPRsProvider)
	if !ok {
		return nil, false
	}
	self := e.cfg().SelfLogin
	if self == "" && len(rcfg.TeamMembers) == 0 {
		// No authors to constrain the search — fall back rather than
		// fetching every open PR in the repo.
		return nil, false
	}
	query := buildEnrichedSearchQuery(rcfg.Remote, self, rcfg.TeamMembers)
	enrichCtx, enrichSpan := startVCSSpan(ctx, "EnrichedPRs", rcfg.Remote, 0)
	enriched, err := enricher.EnrichedPRs(enrichCtx, rcfg.Remote, query)
	recordSpanErr(enrichSpan, err)
	enrichSpan.End()
	if err != nil {
		return nil, false
	}
	prs := make([]api.PR, 0, len(enriched))
	byNumber := make(map[int]vcs.EnrichedPR, len(enriched))
	for _, ep := range enriched {
		// Defensive: providers may omit Repo on the inner PR; the configured
		// remote is the authority.
		if ep.PR.Repo == "" {
			ep.PR.Repo = rcfg.Remote
		}
		prs = append(prs, ep.PR)
		byNumber[ep.PR.Number] = ep
	}
	return &enumeratedEnrichment{prs: prs, byNumber: byNumber}, true
}

// buildEnrichedSearchQuery composes a GitHub-style search string covering
// the repo's open PRs authored by self or any team member. Multiple
// author: clauses act as implicit OR in GitHub's search syntax.
func buildEnrichedSearchQuery(repo, self string, team []string) string {
	parts := []string{"is:pr", "is:open", "repo:" + repo}
	seen := map[string]bool{}
	add := func(login string) {
		login = strings.TrimSpace(login)
		if login == "" || seen[login] {
			return
		}
		seen[login] = true
		parts = append(parts, "author:"+login)
	}
	add(self)
	for _, m := range team {
		add(m)
	}
	return strings.Join(parts, " ")
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
	for _, r := range e.cfg().Repos {
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

// storeFile returns the path to the SQLite store database.
func (e *Engine) storeFile() string {
	if e.deps.StateDir != "" {
		return filepath.Join(e.deps.StateDir, "store.db")
	}
	return defaultStoreFile()
}

func defaultStoreFile() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "pg-pr", "store.db")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "pg-pr", "store.db")
}

// flushOutbox drains the store's outbox through the dispatcher. Called at the
// end of each one-shot Sync and each daemon maintenance cycle. No-op until
// ingestion (a later phase) starts enqueuing events.
func flushOutbox(ctx context.Context, db *store.DB, dispatch store.DispatchFunc) {
	if db == nil || dispatch == nil {
		return
	}
	if err := db.RunOutbox(ctx, dispatch); err != nil {
		_ = err // pending rows are retried next run; daemon logs separately
	}
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
//
// cache, when non-nil, answers the workspace-wide lookups (open processing
// cycles, feedback under each cycle, fingerprint dedup) from a single
// per-tick bulk fetch — typically replacing ~5-8 bd calls per PR with
// in-memory map lookups. When nil, the original per-PR live-call path is
// used, which is the path tests with mocked BeadClients exercise.
//
// enriched, when non-nil, supplies the PR's comments and CI runs from the
// per-repo GraphQL bulk fetch — replaces per-PR ListComments + ListRuns
// REST calls. When nil, the helper falls back to the original per-PR
// gh-CLI calls; tests with mocked providers continue to work.
func (e *Engine) processFeedback(ctx context.Context, bdc BeadClient, cache *beads.TickCache, enriched *vcs.EnrichedPR, repo string, pr api.PR, prBeadID string, summary *Summary) error {
	if prBeadID == "" {
		return nil
	}
	rcfg, err := e.repoConfig(repo)
	if err != nil {
		// Not in config (single-PR ad-hoc) — feedback pipeline is repo-driven.
		return nil
	}

	// Store-path: ingest feedback into SQLite in parallel with the bead path.
	// Only runs when Deps.Store is set; errors are non-fatal (recorded into
	// summary.Errors) so the existing bead/processing-cycle path is unaffected.
	if e.deps.Store != nil {
		if err := e.ingestFeedbackToStore(ctx, repo, pr, enriched); err != nil {
			summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: "ingestFeedbackToStore: " + err.Error()})
		}
	}

	// Gather events.
	var events []feedbackEvent
	if enriched != nil {
		for _, c := range enriched.Comments {
			events = append(events, commentEvent(c))
		}
		for _, r := range enriched.CIRuns {
			events = append(events, ciRunEvent(r))
		}
	} else {
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
	}

	if len(events) == 0 {
		return nil
	}

	// Find or create the active processing-cycle for new feedback. The
	// cache is authoritative when present — it listed every open cycle
	// in the workspace, so a cache miss means no open cycle exists.
	var cycleID string
	var found bool
	if cache != nil {
		cycleID, found = cache.OpenCycleFor(prBeadID)
	} else {
		var err error
		cycleID, found, err = bdc.FindOpenProcessingCycle(ctx, prBeadID)
		if err != nil {
			return fmt.Errorf("find processing-cycle: %w", err)
		}
	}

	// Read the PR's feedback subtree ONCE (cache-less path) — a single
	// `bd dep tree <pr> --direction=up` — and serve BOTH the first-pass
	// CI-success resolver and the second-pass dedup from it. This replaces the
	// first pass's O(workspace-feedback) ListFeedback(cycleID) isChildOf scan;
	// the cache path keeps reading from the in-memory TickCache.
	var subtreeFeedback []beads.Feedback
	if cache == nil {
		var err error
		subtreeFeedback, err = e.prFeedbackSubtree(ctx, bdc, prBeadID)
		if err != nil {
			// Can't read the PR's feedback — skip this tick rather than risk
			// duplicate beads (second-pass concern) or mis-resolving CI
			// feedback (first-pass concern). A later tick retries. (This
			// unifies the two reads' prior error handling onto the more
			// conservative skip-the-tick behavior.)
			return nil
		}
	}

	// First pass: handle CI events whose conclusion is success and close
	// any matching prior ci-failure feedback (resolved-upstream).
	if found {
		var open []beads.Feedback
		if cache != nil {
			// FeedbackUnder returns open + closed; the ci-failure
			// resolver only wants currently-open beads.
			for _, fb := range cache.FeedbackUnder(cycleID) {
				if fb.Status != "closed" {
					open = append(open, fb)
				}
			}
		} else {
			// Same Status!=closed filter, served from the single subtree read
			// above instead of an O(F) ListFeedback(cycleID) scan. The source
			// widens from one open cycle to the PR's whole subtree (all
			// cycles) — safe by design: the resolver only closes on a unique CI
			// ExternalID match and MarkFeedbackResolvedUpstream is idempotent.
			for _, fb := range subtreeFeedback {
				if fb.Status != "closed" {
					open = append(open, fb)
				}
			}
		}
		{
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

	// Build the PR's existing-feedback fingerprint set for the second-pass
	// dedup. Cache-less path derives it from the single subtree read above (all
	// statuses, non-empty fingerprints) — same contents the prior
	// PRFeedbackFingerprints map produced, now with zero extra bd calls.
	var seen map[string]bool
	if cache == nil {
		seen = make(map[string]bool, len(subtreeFeedback))
		for _, fb := range subtreeFeedback {
			if fb.Fields.Fingerprint != "" {
				seen[fb.Fields.Fingerprint] = true
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
		// any cycle for this PR, skip. The cache-less path consults `seen`
		// (built once above) instead of re-listing the PR's cycles per event.
		if cache != nil {
			if _, ok := cache.FindFeedbackForPR(prBeadID, ev.fingerprint); ok {
				continue
			}
		} else if ev.fingerprint != "" && seen[ev.fingerprint] {
			continue
		}

		if !found {
			// Lazily create a processing-cycle on first new feedback.
			id, err := bdc.CreateProcessingCycle(ctx, prBeadID,
				fmt.Sprintf("%s#%d", repo, pr.Number), e.isSelfAuthored(pr.Author))
			if err != nil {
				return fmt.Errorf("create processing-cycle: %w", err)
			}
			cycleID = id
			found = true
			summary.CyclesCreated++
			if cache != nil {
				cache.OpenProcessingByPR[prBeadID] = cycleID
			}
		}

		bdCtx, bdSpan := startBeadsSpan(ctx, "CreateFeedback", repo, pr.Number)
		newID, err := bdc.CreateFeedback(bdCtx, beads.CreateFeedbackInput{
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
		// Augment the cache so same-tick events with duplicate
		// fingerprints dedup against this newly-created feedback rather
		// than triggering another bd CreateFeedback.
		if cache != nil {
			cache.FeedbackByCycle[cycleID] = append(cache.FeedbackByCycle[cycleID], beads.Feedback{
				ID:     newID,
				Status: "hooked",
				Fields: beads.FeedbackFields{
					Kind:        string(ev.kind),
					ExternalID:  ev.externalID,
					Fingerprint: ev.fingerprint,
					AuthorRole:  string(ev.authorRole),
				},
			})
		}
	}
	return nil
}

// prFeedbackSubtree returns every feedback bead in prBeadID's recursive
// parent-child subtree (MR -> processing-cycle -> feedback) in ONE scoped bd
// call (PRFeedbackInSubtree, a single `bd dep tree --direction=up`). Built once
// per refresh and consulted by both processFeedback passes: the CI-success
// resolver (open feedback) and the dedup (fingerprints of all statuses). A bd
// client that doesn't implement the capability (test fakes) yields an empty
// slice; the production daemon client is *beads.Client, which does implement it.
func (e *Engine) prFeedbackSubtree(ctx context.Context, bdc BeadClient, prBeadID string) ([]beads.Feedback, error) {
	r, ok := bdc.(feedbackSubtreeReader)
	if !ok {
		return nil, nil
	}
	return r.PRFeedbackInSubtree(ctx, prBeadID)
}

// maybePromoteDraft inspects the PR's draft state and, when all CI runs
// are green, promotes the PR to ready. bdc is the per-repo bd client whose
// workspace holds the merge-request bead being updated.
//
// enriched, when non-nil, supplies the PR's CI runs from the GraphQL
// bulk fetch — replaces per-PR ListRuns. When nil, the helper falls
// back to per-CICD-provider ListRuns calls.
func (e *Engine) maybePromoteDraft(ctx context.Context, bdc BeadClient, enriched *vcs.EnrichedPR, repo string, pr api.PR, prBeadID string, summary *Summary) error {
	if !pr.Draft {
		return nil
	}
	rcfg, err := e.repoConfig(repo)
	if err != nil {
		return nil
	}
	if enriched != nil {
		// Bulk-fetched CI runs cover every check; the rollup is over the
		// PR's last commit so it's authoritative for "all green".
		if !allRunsSuccessful(enriched.CIRuns) {
			return nil
		}
	} else {
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

		// Ownership guard: never post replies to threads on PRs we don't
		// own. ReplyDraft staged on a team-mate's feedback bead is a bug
		// class — emit a warning so the user can investigate, but skip
		// the post. The local ReplyDraft is left in place; the user can
		// inspect / delete / retarget it via bd or pg-pr verbs.
		if !e.isSelfAuthored(mr.Fields.Author) {
			summary.Warnings = append(summary.Warnings, SummaryError{
				Repo: rcfg.Remote,
				Message: fmt.Sprintf(
					"reply %s skipped: parent PR #%d authored by %q (not self) — ReplyDraft should not have been staged",
					fb.ID, mr.Fields.PRNumber, mr.Fields.Author),
			})
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

		resp, err := replier.ReplyToThread(ctx, mr.Fields.Repo, fb.Fields.ExternalID, marker.Stamp(fb.Fields.ReplyDraft))
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
//
// CodeRabbit walkthrough summaries embed a base64-encoded internal-state
// block that is 98% of the comment body (~127KB of ~129KB observed on
// the zr workspace). bd's description column is TEXT (64KB), so the
// block must be stripped before assignment. The fingerprint also uses
// the stripped body — since CodeRabbit comments never previously fit in
// bd, there are no existing feedback beads with full-body fingerprints
// to dedup against.
func commentEvent(c api.Comment) feedbackEvent {
	body := stripCodeRabbitInternalState(c.Body)
	kind := beads.FeedbackKindCommentThread
	if c.Path != "" || c.Line > 0 || c.ThreadID != "" {
		kind = beads.FeedbackKindReviewThread
	}
	fingerprint := fingerprintOf("comment", c.Author, c.Path, fmt.Sprintf("%d", c.Line), body)
	role := commentAuthorRole(c)
	title := strings.TrimSpace(strings.SplitN(body, "\n", 2)[0])
	if title == "" {
		title = "comment from " + c.Author
	}
	return feedbackEvent{
		kind:        kind,
		externalID:  c.ID,
		fingerprint: fingerprint,
		authorRole:  role,
		title:       title,
		body:        body,
	}
}

// stripCodeRabbitInternalState removes the `<!-- internal state start
// --> ... <!-- internal state end -->` block CodeRabbit embeds in
// walkthrough summary comments. The block is a base64-encoded bot
// state blob (no human-readable content) that runs ~120KB and exceeds
// bd's description column limit. Comments without the marker are
// returned unchanged.
func stripCodeRabbitInternalState(body string) string {
	const startMarker = "<!-- internal state start -->"
	const endMarker = "<!-- internal state end -->"
	start := strings.Index(body, startMarker)
	if start < 0 {
		return body
	}
	rel := strings.Index(body[start:], endMarker)
	if rel < 0 {
		// Unmatched start — leave the body alone so we don't drop
		// the rest of the comment on a malformed marker.
		return body
	}
	end := start + rel + len(endMarker)
	return body[:start] + "[CodeRabbit internal state elided]" + body[end:]
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

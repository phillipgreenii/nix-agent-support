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
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/cirollup"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/replyposter"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/telemetry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ticketlink"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/issues"
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

// SinglePREnricher is an optional capability: fetch one PR's enrichment
// (reviews, comments incl. the real review-thread node ids, CI) in a single
// GraphQL round-trip. enrichOnePR prefers it over the per-PR REST methods so
// `sync --pr` keys comment threads the same way as the bulk daemon path
// (by PRRT_ review-thread node id, with createdAt populated) — avoiding the
// divergent, duplicate, posted_at-less rows the REST path otherwise produces.
type SinglePREnricher interface {
	EnrichPR(ctx context.Context, repo string, number int) (*vcs.EnrichedPR, error)
}

// DraftToggler is the optional subset of VCSProvider needed for the draft
// auto-promote feature.
type DraftToggler interface {
	SetDraft(ctx context.Context, repo string, number int, draft bool) error
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
// After the #5 event-ownership refactor all bead writes (create/update/close)
// moved to beadsbridge; only ListMergeRequests is still called through this
// interface (by listExistingByKey). beadsbridge.BeadClient covers the write
// side — the two interfaces are intentionally disjoint (Interface Segregation).
type BeadClient interface {
	ListMergeRequests(ctx context.Context, includeClosed bool) ([]beads.MergeRequest, error)
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

	// Review configures the daemon-side draft-review consumption hook
	// (pg2-4c5i.36). When Review.Beads or Review.Spawner is nil the hook is
	// disabled (a no-op), so callers that don't wire review production keep
	// running unchanged.
	Review ReviewHookDeps

	// JiraProvider, when non-nil AND cfg.Jira is non-nil, is used to build the
	// JiraLookupFunc injected into enrich.Input (pg2-jpfw.4). Both fields must
	// be non-nil to activate the signal; when either is nil the signal stays
	// disabled (backward-compatible). Tests inject a fake provider here.
	// Production code leaves this nil; the wiring in enrichAndStore constructs
	// the default provider from PGPR_JIRA_BINARY.
	JiraProvider issues.Provider
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

	// lastRateLeft / rateResetAt retain the latest GraphQL rate-limit reading
	// (rateLimit.remaining + rateLimit.resetAt) from a successful fingerprint
	// poll, so the detector can proactively skip its next poll while remaining
	// is below the graphQLRateBuffer reserve until the window resets. Only the
	// daemon loop goroutine touches them (via recordPoll/fingerprintTick), so no
	// lock is needed. rateResetAt is zero until the first poll parses a resetAt.
	lastRateLeft int
	rateResetAt  time.Time

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

	// authoredByMe gates UPSTREAM WRITES that assert readiness on the author's
	// behalf (maybePromoteDraft). It stays AUTHORSHIP-ONLY: a co-owned PR (I
	// pushed commits onto a teammate's PR) must NOT be auto-promoted out of
	// draft. This deliberately diverges from the `ownership` string below, which
	// is 3-way. Empty Author / empty SelfLogin => not mine.
	authoredByMe := make(map[prKey]bool, len(observed))
	for key, pr := range observed {
		if e.isSelfAuthored(pr.Author) {
			authoredByMe[key] = true
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
			if authoredByMe[key] {
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

			// Pluck this PR's bulk-fetched enrichment BEFORE deciding ownership
			// (commit authors drive co-owned) and before emitPREvent writes the
			// row. nil means the EnrichedPRs path didn't populate the cache;
			// downstream helpers fall back to per-PR REST calls.
			var prEnriched *vcs.EnrichedPR
			if byNum := enrichByRepo[key.Repo]; byNum != nil {
				if ep, ok := byNum[pr.Number]; ok {
					prEnriched = &ep
				}
			}
			// The bulk-enumerated pr leaves Mergeable/MergeStateStatus/
			// AutoMergeEnabled empty; overlay GraphQL's merge-state so
			// pr.HasConflict() (emitPREvent's row/payload, and the attention
			// predicate downstream) reflects reality. pr is a loop-local copy of
			// the observed map's value, so mutating it here is safe (pg2-tsgkj).
			overlayMergeState(&pr, prEnriched)

			// 3-way ownership string for the store row + event (drives
			// dashboard, replyposter, beadsbridge, attention). Degrades to
			// authorship-only when enrichment is absent (prEnriched == nil).
			var commitAuthors []string
			if prEnriched != nil {
				commitAuthors = prEnriched.CommitAuthors
			}
			own := ownership.Classify(ownership.Engagement{
				Self: e.cfg().SelfLogin, PRAuthor: pr.Author, CommitAuthors: commitAuthors,
			})
			ownershipStr := own.String()

			// Event-ownership refactor (Task 6): the PR (merge-request) bead is
			// no longer created inline. emitPREvent writes the authoritative
			// store row for EVERY observed PR AND emits pr.opened/pr.updated in
			// one transaction (drives Task 8 close-detection via
			// store.ListOpenPRs); the beadsbridge handler projects the bead at
			// outbox flush. It no-ops when Store is nil (test/legacy configs).
			//
			// Emit pr.opened/pr.updated BEFORE this PR's feedback.created events
			// (enqueued inside processFeedback) so the bridge ensures the PR
			// bead before attaching a processing cycle. The serial loop within
			// one goroutine preserves this ordering.
			eventType := store.EventPROpened
			if _, was := repoPreExisting[key.Repo][key]; was {
				eventType = store.EventPRUpdated
			}
			if err := e.emitPREvent(prCtx, eventType, key.Repo, pr, ownershipStr); err != nil {
				telemetry.SyncErrorsTotal.WithLabelValues(key.Repo).Inc()
				recordSpanErr(prSpan, err)
				summary.Errors = append(summary.Errors, SummaryError{
					Repo:    key.Repo,
					Message: fmt.Sprintf("PR #%d emit: %v", pr.Number, err),
				})
				return
			}
			if eventType == store.EventPROpened {
				summary.BeadsCreated++
			} else {
				summary.BeadsUpdated++
			}

			// Resolve the repo config once for this PR: used by both
			// reconcileTruncatedCI and enrichAndStore (ticket-pattern extraction).
			// On a miss, fall back to a minimal RepoConfig carrying the remote so
			// enrichAndStore still runs (just without ticket patterns).
			prRcfg, prRcfgErr := e.repoConfig(key.Repo)
			if prRcfgErr != nil {
				prRcfg = config.RepoConfig{Remote: key.Repo}
			}
			// Repair CI truncated by the bulk GraphQL first:30 context cap: on a
			// PR with >30 checks, re-source CI from the dedicated CICD provider
			// (consistent with the per-PR enrichOnePR path) so BOTH ci-failure
			// ingestion (processFeedback) and draft auto-promote (maybePromoteDraft)
			// see the complete check set — no missed failing check beyond context
			// 30, no wrong promotion. prEnriched is a local copy (&ep), so this
			// mutation does not affect the shared enrichByRepo map.
			if prEnriched != nil {
				e.reconcileTruncatedCI(prCtx, prEnriched, prRcfg)
			}
			// Compute and persist enrichment (kind/languages/size/urgency) for
			// this PR. Runs after emitPREvent so the store row already exists.
			// Non-fatal: errors are recorded into summary.Errors and self-heal.
			if err := e.enrichAndStore(prCtx, key.Repo, pr, prEnriched, prRcfg); err != nil {
				summary.Errors = append(summary.Errors, SummaryError{Repo: key.Repo, Message: err.Error()})
			}
			// Phase 3: drive feedback + draft auto-promote pipelines for the PR.
			if err := e.processFeedback(prCtx, bdc, cachesByRepo[key.Repo], prEnriched, key.Repo, pr, summary); err != nil {
				telemetry.SyncErrorsTotal.WithLabelValues(key.Repo).Inc()
				recordSpanErr(prSpan, err)
				summary.Errors = append(summary.Errors, SummaryError{
					Repo:    key.Repo,
					Message: fmt.Sprintf("PR #%d feedback: %v", pr.Number, err),
				})
			}
			// Upstream-write phase: only for self-authored PRs.
			// See partition above.
			if authoredByMe[key] {
				if err := e.maybePromoteDraft(prCtx, prEnriched, key.Repo, pr, summary); err != nil {
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

	// Process queued replies from the store: the reply-poster reads
	// store.ListPendingReplies and posts via the github provider, recording
	// response_id for idempotency. Store-backed and repo-agnostic, so it runs
	// once per Sync (not per repo).
	if n, err := e.reconcileReplies(ctx); err != nil {
		summary.Errors = append(summary.Errors, SummaryError{
			Repo:    "(replies)",
			Message: fmt.Sprintf("reply pipeline: %v", err),
		})
	} else {
		summary.RepliesPosted += n
	}

	// Close PRs whose row is no longer in the observed set, but only among
	// repos that synced successfully. This prevents us from closing rows in a
	// repo whose sync failed (we'd have no authoritative view).
	//
	// Store-driven: read open PR rows from store.ListOpenPRs and, for each PR
	// no longer observed, mark the row closed (so it isn't re-detected next
	// tick) and emit pr.closed. The beadsbridge handler performs the actual
	// bead close + cascade at outbox flush.
	if e.deps.Store != nil {
		for repo := range healthyRepos {
			open, err := e.deps.Store.ListOpenPRs(ctx, repo)
			if err != nil {
				summary.Errors = append(summary.Errors, SummaryError{
					Repo:    repo,
					Message: fmt.Sprintf("list open prs: %v", err),
				})
				continue
			}
			for _, row := range open {
				k := prKey{Repo: row.Repo, Number: row.Number}
				if _, watched := observed[k]; watched {
					continue
				}
				// emitPRClosed atomically marks the store row closed (so
				// ListOpenPRs stops returning it next tick) AND emits pr.closed
				// in one transaction. If the enqueue fails the mark-closed rolls
				// back, so the row is re-detected next tick rather than silently
				// dropping the event. merged is false: Sync close-detection can't
				// distinguish merged-vs-closed (the daemon's refresh.go handles
				// merged); this matches the old close reason upstream-not-watched.
				if err := e.emitPRClosed(ctx, row, false); err != nil {
					summary.Errors = append(summary.Errors, SummaryError{
						Repo:    repo,
						Message: fmt.Sprintf("close %s#%d: %v", row.Repo, row.Number, err),
					})
					continue
				}
				summary.BeadsClosed++
			}
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
		// Mirror the per-PR loop: repair CI truncated by the bulk GraphQL
		// first:30 cap from the CICD provider so the snapshot's CI matches what
		// ingestion/promotion acted on (enriched is a local &ep copy).
		e.reconcileTruncatedCI(ctx, enriched, rcfg)
		bdc := repoClients[key.Repo]
		if bdc == nil {
			bdc = e.bdClientFor(rcfg)
		}
		in := e.buildPRInput(ctx, pr, enriched, bdc, cachesByRepo[key.Repo], rcfg, "")
		// JIRA — ticket keys parsed from branch/title/body via ticketlink;
		// downstream task (pg2-4c5i.26) resolves full Jira issue details.
		inputs = append(inputs, in)
	}

	snap := snapshot.Build(snapshot.BuilderInput{
		GeneratedAt:          e.deps.Now(),
		SyncIntervalSeconds:  int(e.deps.SyncInterval.Seconds()),
		Self:                 e.cfg().SelfLogin,
		TeamMembers:          e.allTeamMembers(),
		WatchLabels:          e.allWatchLabels(),
		Registry:             e.deps.AgentRegistry,
		PRs:                  inputs,
		ExcludedChecksByRepo: e.excludedChecksByRepo(),
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

	// Reviews + comments: prefer single-PR GraphQL so per-PR sync keys comment
	// threads by the real review-thread node id (PRRT_) + createdAt, identical
	// to the bulk daemon path (no divergent/duplicate rows). Fall back to the
	// per-PR REST methods only on a hard error. CI is fetched separately below
	// (from the dedicated CICD provider), so only thread-bearing truncation
	// matters here — ciContexts/files truncation is routine on large PRs and
	// irrelevant to thread identity.
	gotGraphQL := false
	if vp, err := e.providerFor(rcfg); err == nil {
		if spe, ok := vp.(SinglePREnricher); ok {
			ep, eerr := spe.EnrichPR(ctx, pr.Repo, pr.Number)
			if eerr == nil && ep != nil {
				out.Reviews = ep.Reviews
				out.Comments = ep.Comments
				out.Files = ep.Files
				out.Commits = ep.Commits
				// CommitAuthors drives ownership.Classify's co-owned detection
				// (pg2-aag72 A7). Without this, refreshPR's hoisted enrichOnePR call
				// would never see per-commit authors on the GraphQL single-PR path,
				// silently defeating co-owned detection in the daemon.
				out.CommitAuthors = ep.CommitAuthors
				// GraphQL is the only source of these merge-state fields; the
				// REST GetPR path (refreshPR) leaves them empty. Carry them so the
				// daemon snapshot / mine-panel reminder work, not just one-shot sync. (pg2-dwfld)
				out.PR.Mergeable = ep.PR.Mergeable
				out.PR.MergeStateStatus = ep.PR.MergeStateStatus
				out.PR.AutoMergeEnabled = ep.PR.AutoMergeEnabled
				if tt := threadBearingTruncations(ep.Truncated); len(tt) > 0 {
					fmt.Fprintf(os.Stderr, "pg-pr: enrichOnePR %s#%d: GraphQL thread data truncated %v (using partial, correctly-keyed data)\n", pr.Repo, pr.Number, tt)
				}
				gotGraphQL = true
			} else if eerr != nil {
				fmt.Fprintf(os.Stderr, "pg-pr: enrichOnePR %s#%d: GraphQL enrichment failed (%v); falling back to REST\n", pr.Repo, pr.Number, eerr)
			}
		}
		if !gotGraphQL {
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
	}

	// CI runs always come from the dedicated CICD provider (unchanged), so a PR
	// with many checks keeps complete CI regardless of which enrichment path ran
	// above — GraphQL statusCheckRollup caps at 30 contexts and is not used for
	// CI here.
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

// threadBearingTruncations filters EnrichedPR.Truncated to the connections that
// affect feedback-thread identity/content (reviews, comments, reviewThreads,
// threadComments). ciContexts/files/labels/commits truncation is routine on
// large PRs and irrelevant to thread keying, so enrichOnePR ignores it (CI is
// fetched from the dedicated CICD provider, not GraphQL statusCheckRollup).
func threadBearingTruncations(truncated []string) []string {
	var out []string
	for _, t := range truncated {
		switch t {
		case "reviews", "comments", "reviewThreads", "threadComments":
			out = append(out, t)
		}
	}
	return out
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
	// Derive "requested of me" from the provider's self-agnostic RequestedReviewers
	// against the configured self login. Done HERE — the single point BOTH the
	// daemon per-PR refresh (refreshPR) AND the one-shot full-sync snapshot
	// (buildAndStoreSnapshot) reach — so the dashboard's "PRs to Review" match
	// reason is consistent across both paths (pg2-ynhr.13 B2/B5 review #2).
	pr.ReviewRequestedOfMe = reviewRequestedOfSelf(e.cfg().SelfLogin, pr.RequestedReviewers)
	in := snapshot.PRInput{
		PR: pr,
		Ownership: ownership.Classify(ownership.Engagement{
			Self: e.cfg().SelfLogin, PRAuthor: pr.Author, CommitAuthors: commitAuthorsOf(enriched),
		}),
	}

	// --- ticket linkage ---
	// Extract linked external ticket key(s) from the PR's branch, title, and
	// body using the repo's config-driven patterns. Each key becomes a
	// stub JIRAItem (ID only); the downstream Jira-fetch task (pg2-4c5i.26)
	// is responsible for resolving Title/State/URL.
	if keys := ticketlink.Parse(pr.Branch, pr.Title, pr.Body, rcfg.TicketPatterns); len(keys) > 0 {
		in.JIRA = make([]api.Issue, len(keys))
		for i, k := range keys {
			in.JIRA[i] = api.Issue{ID: k}
		}
	}

	// --- reviews/comments/CI runs ---
	if enriched != nil {
		in.Reviews = enriched.Reviews
		in.Comments = enriched.Comments
		in.CIRuns = enriched.CIRuns
		// enriched.PR carries GraphQL-only merge-state the REST-built `pr` lacks
		// on the daemon refresh path; overlay so MineRow reminder fields populate. (pg2-dwfld)
		overlayMergeState(&in.PR, enriched)
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

	// --- attention read-model inputs (pg2-4c5i.13) ---
	// Thread the PR's PERSISTED revision timeline + the draft-review-closed
	// signal so buildTeamRow's snapshot.NeedsAttention call is store-derived —
	// the SAME predicate + SAME inputs the emitAttention write-model path uses,
	// so the dashboard signal can never diverge from the attention bead (D4/R4).
	// Only meaningful for team PRs (Build ignores these on Mine rows).
	if e.deps.Store != nil {
		if stored, gerr := e.deps.Store.GetPR(ctx, pr.Repo, pr.Number); gerr == nil && stored != nil {
			if revs, rerr := e.deps.Store.ListRevisions(ctx, stored.ID); rerr == nil {
				in.Revisions = revs
			}
			if finder, ok := bdc.(draftReviewFinder); ok {
				if _, closed, found, ferr := finder.FindDraftReviewForPR(ctx, pr.Repo, pr.Number); ferr == nil {
					in.DraftReviewClosed = found && closed
				}
			}
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

// allWatchLabels returns the de-duplicated union of WatchLabels across all
// configured repos — the review labels whose PRs join the "PRs to Review" set
// (pg2-ynhr.13). Mirrors allTeamMembers.
func (e *Engine) allWatchLabels() []string {
	seen := map[string]struct{}{}
	var out []string
	for _, r := range e.cfg().Repos {
		for _, l := range r.WatchLabels {
			if _, ok := seen[l]; ok {
				continue
			}
			seen[l] = struct{}{}
			out = append(out, l)
		}
	}
	return out
}

// excludedChecksByRepo maps each configured repo's remote to its
// excluded_ci_checks patterns, for the snapshot's cirollup excluder. (pg2-qs46b)
func (e *Engine) excludedChecksByRepo() map[string][]string {
	repos := e.cfg().Repos
	out := make(map[string][]string, len(repos))
	for _, r := range repos {
		if len(r.ExcludedCIChecks) > 0 {
			out[r.Remote] = r.ExcludedCIChecks
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

// reconcileTruncatedCI repairs an EnrichedPR whose bulk-fetched CI runs were
// truncated by the GraphQL statusCheckRollup.contexts(first:30) page cap.
//
// The bulk daemon path enumerates CI via a single GraphQL search capped at 30
// status contexts per PR; on a PR with >30 checks (common on large repos) the
// enumerated EnrichedPR.CIRuns is incomplete and EnrichedPR.Truncated records
// "ciContexts". Both downstream consumers read this enumerated set as
// authoritative: ci-failure ingestion (ingestFeedbackToStore iterates CIRuns
// for Conclusion==failure) would MISS a failing check beyond context 30, and
// draft auto-promote (maybePromoteDraft -> cirollup.Compute(...).State over a truncated
// "all green" set) would WRONGLY promote a draft whose >30 check is failing —
// maybePromoteDraft does not consult the aggregate rollup .state as a guard.
//
// The fix mirrors the per-PR enrichOnePR path (pg2-kiqq): when ciContexts
// truncated, re-source CI from the dedicated CICD provider (which paginates to
// completeness), preferring the branch-known path. On success the ciContexts
// flag is cleared so callers no longer treat the CI as partial. When no CICD
// provider is configured or the call errors, the (partial) GraphQL CI and the
// truncation flag are left intact — degrading no worse than before.
//
// Non-truncated PRs (the ≤30-context happy path) and absent-entry PRs (nil
// enriched) are untouched; the latter already fall back to the CICD provider in
// buildPRInput / maybePromoteDraft's enriched==nil branch.
func (e *Engine) reconcileTruncatedCI(ctx context.Context, enriched *vcs.EnrichedPR, rcfg config.RepoConfig) {
	if enriched == nil {
		return
	}
	truncated := false
	for _, f := range enriched.Truncated {
		if f == "ciContexts" {
			truncated = true
			break
		}
	}
	if !truncated {
		return
	}
	cp := e.firstCICDFor(rcfg)
	if cp == nil {
		return
	}
	pr := enriched.PR
	var runs []api.CIRun
	var cerr error
	if bl, ok := cp.(CICDBranchLister); ok && strings.TrimSpace(pr.Branch) != "" {
		runs, cerr = bl.ListRunsByBranch(ctx, pr.Repo, pr.Branch)
	} else {
		runs, cerr = cp.ListRuns(ctx, pr.Repo, pr.Number)
	}
	if cerr != nil {
		// Leave the partial GraphQL CI + truncation flag intact: no worse than
		// the pre-fix behaviour, and self-heals on a later tick.
		return
	}
	enriched.CIRuns = runs
	// CI is now complete from the CICD provider; drop the stale ciContexts flag
	// so downstream readers no longer treat CI as truncated.
	enriched.Truncated = withoutFlag(enriched.Truncated, "ciContexts")
}

// withoutFlag returns flags with every occurrence of drop removed. Returns nil
// when the result is empty so callers can use the conventional empty-slice
// semantics (len==0 means "nothing truncated").
func withoutFlag(flags []string, drop string) []string {
	var out []string
	for _, f := range flags {
		if f != drop {
			out = append(out, f)
		}
	}
	return out
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

// SetReviewHook wires the draft-review consumption hook (pg2-4c5i.36) onto the
// engine. Called by CLI daemon-mode setup. When deps.Beads or deps.Spawner is
// nil the hook stays a no-op. Safe to call only before the daemon loop starts.
func (e *Engine) SetReviewHook(deps ReviewHookDeps) {
	e.deps.Review = deps
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

	// If upstream is closed/merged, emit pr.closed/pr.merged so the beadsbridge
	// cascade-closes the bead + its descendants at outbox flush, instead of
	// upserting an open bead inline. The store row is upserted (closed/merged)
	// to keep it authoritative.
	if pr.State == "closed" || pr.State == "merged" || pr.Merged {
		merged := pr.Merged || pr.State == "merged"
		ownershipStr := ownership.Classify(ownership.Engagement{
			Self: e.cfg().SelfLogin, PRAuthor: pr.Author,
		}).String()
		row := e.prToStoreRow(repo, *pr, ownershipStr) // state resolves to closed/merged via stateForPR
		// emitPRClosed atomically upserts the closed/merged row AND enqueues the
		// event in one tx (keeping the store authoritative). The emit is
		// critical: a dropped pr.closed/pr.merged event also drops the bridge
		// cascade. Propagate (matching this function's other error paths) rather
		// than silencing it.
		if err := e.emitPRClosed(ctx, row, merged); err != nil {
			summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: err.Error()})
			summary.FinishedAt = e.deps.Now()
			return summary, err
		}
		summary.BeadsClosed = 1
		summary.Repos = []RepoSummary{{Repo: repo, PRs: 1}}
		summary.TotalPRs = 1
		summary.FinishedAt = e.deps.Now()
		flushOutbox(ctx, e.deps.Store, e.deps.Dispatch)
		return summary, nil
	}

	// Open PR: run the shared store-write + emit + feedback + (self)
	// draft-promote pipeline. Per-bead errors are accumulated into
	// summary.Errors so the single-PR command reports partial failures the
	// same way it always did. The PR bead is no longer created inline here —
	// applyFetchedPR emits pr.opened/updated and the beadsbridge projects the
	// bead at the flushOutbox below.
	//
	// Enrich BEFORE applyFetchedPR so the CLI single-sync path sees the same
	// GraphQL merge-state + commit-author signal the daemon refreshPR path does
	// (mirrors refresh.go's enrich-before-apply step; SyncPR intentionally omits
	// refreshPR's pre-apply overlay + attention emit, which it has no need for).
	// Without this, enriched==nil made overlayMergeState a
	// no-op — PRPayload.HasConflict was always false, flapping a daemon-stashed
	// conflict priority in the bridge (reconcilePriority's clear branch) — and
	// CommitAuthors was nil, degrading a co-owned PR to team. applyFetchedPR
	// re-applies overlayMergeState idempotently, so the overlaid merge-state is
	// in *pr before emitPREvent. (pg2-ic3nh)
	enriched := e.enrichOnePR(ctx, rcfg, *pr)
	if err = e.applyFetchedPR(ctx, rcfg, pr, enriched, summary); err != nil {
		summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: err.Error()})
	} else {
		// One-shot store-backed reply reconcile (the shared apply path no
		// longer does it).
		if n, rerr := e.reconcileReplies(ctx); rerr != nil {
			summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: rerr.Error()})
		} else {
			summary.RepliesPosted += n
		}
	}
	summary.Repos = []RepoSummary{{Repo: repo, PRs: 1}}
	summary.TotalPRs = 1
	summary.FinishedAt = e.deps.Now()
	flushOutbox(ctx, e.deps.Store, e.deps.Dispatch)
	return summary, nil
}

// applyFetchedPR runs the create/update pipeline for an OPEN, active PR.
// Caller handles closed/merged separately. It is the single place the CLI
// one-shot (SyncPR) and the daemon per-PR refresh (refreshPR) share this
// open-PR logic.
//
// Event-ownership model (Task 9): the PR (merge-request) bead is NO LONGER
// created inline here. applyFetchedPR writes the authoritative store row and
// EMITS pr.opened (first observation) or pr.updated (already-stored). The
// beadsbridge projects the bead from that event when the outbox is flushed.
// Callers MUST flush the outbox before any step that reads the bead back (e.g.
// buildPRInput's FindByRepoAndNumber).
//
// Ordering invariants:
//   - opened-vs-updated is decided from GetPR BEFORE UpsertPR writes the row
//     (UpsertPR would otherwise make every observation look "updated").
//   - pr.opened/updated is emitted BEFORE processFeedback enqueues
//     feedback.created, so the bridge has a PR bead to attach the cycle to.
//
// Reply draining is NOT performed here. The daemon drains replies once per
// tick from a maintenance goroutine; the CLI SyncPR drains explicitly after
// this call returns.
//
// Returns on the FIRST hard error and skips the later phases. Hard errors are
// rare — most issues are recorded into summary.Errors and return nil — and
// fail-fast is the intended shape for the daemon's per-PR refresh.
func (e *Engine) applyFetchedPR(ctx context.Context, rcfg config.RepoConfig, pr *api.PR, enriched *vcs.EnrichedPR, summary *Summary) error {
	// refreshPR already overlays merge-state before calling in on the daemon
	// path, but overlayMergeState is idempotent, so re-applying here makes
	// applyFetchedPR correct for ANY caller (pg2-tsgkj).
	overlayMergeState(pr, enriched)
	var commitAuthors []string
	if enriched != nil {
		commitAuthors = enriched.CommitAuthors
	}
	ownershipStr := ownership.Classify(ownership.Engagement{
		Self: e.cfg().SelfLogin, PRAuthor: pr.Author, CommitAuthors: commitAuthors,
	}).String()
	// Decide opened-vs-updated from existing store state BEFORE emitPREvent
	// writes the row — its UpsertPR would otherwise make GetPR always find the
	// row. GetPR is a read, so the decision is consistent with the atomic
	// upsert+emit that follows.
	eventType := store.EventPROpened
	if e.deps.Store != nil {
		if existing, _ := e.deps.Store.GetPR(ctx, rcfg.Remote, pr.Number); existing != nil {
			eventType = store.EventPRUpdated
		}
	}
	if eventType == store.EventPROpened {
		summary.BeadsCreated++
	} else {
		summary.BeadsUpdated++
	}
	// emitPREvent atomically writes the authoritative store row AND emits
	// pr.opened/updated — BEFORE processFeedback (which enqueues
	// feedback.created) so the bridge projects the PR bead first.
	if err := e.emitPREvent(ctx, eventType, rcfg.Remote, *pr, ownershipStr); err != nil {
		return err
	}
	// Compute and persist enrichment (kind/languages/size/urgency) for this PR.
	// Runs after emitPREvent so the store row already exists. Non-fatal: errors
	// are recorded into summary.Errors and self-heal on the next tick.
	if err := e.enrichAndStore(ctx, rcfg.Remote, *pr, enriched, rcfg); err != nil {
		summary.Errors = append(summary.Errors, SummaryError{Repo: rcfg.Remote, Message: err.Error()})
	}
	// Phase 3: feedback + draft pipelines. enriched (when non-nil) carries the
	// PR's comments/CI runs so these helpers skip their own per-PR fetches.
	if err := e.processFeedback(ctx, nil, nil, enriched, rcfg.Remote, *pr, summary); err != nil {
		return err
	}
	// Authorship-only (NOT ownership): never auto-promote a co-owned teammate draft.
	if e.isSelfAuthored(pr.Author) {
		if err := e.maybePromoteDraft(ctx, enriched, rcfg.Remote, *pr, summary); err != nil {
			return err
		}
	}
	return nil
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

// reconcileReplies posts queued replies from the SQLite store to upstream via
// the store-backed reply-poster. It reads store.ListPendingReplies and posts
// through the github VCS provider (idempotent via response_id).
//
// It is a no-op when Deps.Store is unset, or when the configured github VCS
// provider does not satisfy replyposter.Replier (e.g. a test stub that isn't a
// real provider) — reply reconciliation is skipped gracefully in that case.
func (e *Engine) reconcileReplies(ctx context.Context) (int, error) {
	if e.deps.Store == nil {
		return 0, nil
	}
	vp := e.deps.VCS["github"]
	if vp == nil {
		return 0, nil
	}
	replier, ok := vp.(replyposter.Replier)
	if !ok {
		// Provider lacks the reply capability (e.g. a test stub). Skip.
		return 0, nil
	}
	return replyposter.New(e.deps.Store, replier).Reconcile(ctx)
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

// processFeedback ingests a PR's upstream feedback (comments, CI failures)
// into the SQLite store and enqueues feedback.created outbox events. The
// process-feedback (processing-cycle) bead is no longer created here — it is
// ensured by the beadsbridge handler reacting to those events. Feedback beads
// themselves are gone entirely; feedback lives in internal/store.
//
// bdc and cache are retained in the signature for call-site compatibility but
// are unused now that the bead-feedback path is removed.
//
// enriched, when non-nil, supplies the PR's comments and CI runs from the
// per-repo GraphQL bulk fetch. When nil (or Deps.Store is unset), this is a
// no-op.
//
// Errors from ingestion are recorded into summary.Errors (non-fatal); the
// function always returns nil so the surrounding pipeline keeps progressing.
func (e *Engine) processFeedback(ctx context.Context, _ BeadClient, _ *beads.TickCache, enriched *vcs.EnrichedPR, repo string, pr api.PR, summary *Summary) error {
	if _, err := e.repoConfig(repo); err != nil {
		// Not in config (single-PR ad-hoc) — feedback pipeline is repo-driven.
		return nil
	}

	// Store-path: ingest feedback into SQLite and enqueue feedback.created
	// outbox events. Only runs when Deps.Store is set; errors are non-fatal
	// (recorded into summary.Errors).
	if e.deps.Store != nil {
		if err := e.ingestFeedbackToStore(ctx, repo, pr, enriched); err != nil {
			summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: "ingestFeedbackToStore: " + err.Error()})
		}
	}
	return nil
}

// maybePromoteDraft inspects the PR's draft state and, when all CI runs
// are green, promotes the PR to ready (SetDraft=false upstream).
//
// SetDraft is the authoritative upstream effect and fires immediately. The
// merge-request bead's new state is projected via an emitted pr.updated event
// (the beadsbridge updates the bead at outbox flush), not written inline.
//
// enriched, when non-nil, supplies the PR's CI runs from the GraphQL
// bulk fetch — replaces per-PR ListRuns. When nil, the helper falls
// back to per-CICD-provider ListRuns calls.
func (e *Engine) maybePromoteDraft(ctx context.Context, enriched *vcs.EnrichedPR, repo string, pr api.PR, summary *Summary) error {
	if !pr.Draft {
		return nil
	}
	rcfg, err := e.repoConfig(repo)
	if err != nil {
		return nil
	}
	ciExcl := cirollup.NewExcluder(rcfg.ExcludedCIChecks)
	if enriched != nil {
		// Bulk-fetched CI runs cover every check; the rollup is over the
		// PR's last commit so it's authoritative for "all green".
		if cirollup.Compute(enriched.CIRuns, ciExcl).State != "success" {
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
			if cirollup.Compute(runs, ciExcl).State != "success" {
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
	promoted := pr
	promoted.Draft = false
	promoted.State = "open"
	if err := e.emitPREvent(ctx, store.EventPRUpdated, repo, promoted, "mine"); err != nil {
		return fmt.Errorf("emit pr.updated (draft-promote): %w", err)
	}
	summary.DraftPromoted++
	return nil
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

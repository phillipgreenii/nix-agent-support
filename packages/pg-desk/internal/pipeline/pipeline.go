// Package pipeline implements pg-desk's run pipeline (docket pg2-2j5ac.32,
// Phase 9, packet 6; sync stage wired by docket pg2-2j5ac.34, Phase 10):
// the gather -> interpret -> store -> sync wiring for one entity, `pg-desk
// run`'s own exit-code contract, and structured JSON logging [design doc
// "7.9 Failure handling and logging"].
//
// This packet implements the PR-type pipeline only: an issue or thread
// event additionally re-running stage 2 only (design section 7.2) is
// Phase 10 (beads backend) / Phase 13 (Jira, threads) — cmd/pg-desk/run.go
// stubs those types at the CLI layer before this package is ever reached.
// The sync stage below runs only for entityType == "pr" for the same
// reason.
//
// # Exit codes [Binding decisions]
//
// Run returns nil on success or on a degraded-but-completed run (pr-pool
// exit 0), and a non-nil error on a triggering-entity fetch failure, a
// store error, or a sync-stage failure (pr-pool exit 1; a sync failure is
// ALSO recorded as sync_error first — pg2-kftf9.1). cmd/pg-desk/main.go's RunE dispatch maps
// any non-nil error to exit 1 and never calls os.Exit with another code,
// so this package need not (and must not) call os.Exit itself: returning
// nil/error is the whole contract, and it is never 9 and never a raw
// pg-connector exit code by construction.
//
// # annotation is never touched here
//
// dispositions in the persisted interpretation row are the interpret
// stage's own rule-set verdict, verbatim — no annotation-table override
// merge. internal/interpret/disposition.go's ApplyDispositionOverrides
// exists for a caller that reads the store's per-comment overrides, but
// that read is not part of this packet's pinned Contract ("Consumes:
// ... the store's entity/interpretation/meta writer methods (packet 3)"
// — annotation is deliberately absent from that list). A full pipeline
// run therefore leaves the annotation table byte-identical, satisfying
// this packet's own acceptance criterion, and never calls
// Store.UpsertAnnotation (internal/store/annotation.go's own doc comment:
// "no pipeline stage ... calls UpsertAnnotation").
//
// # sync_error
//
// sync_error is a sync-only column (internal/store's own Interpretation
// doc comment) — no OTHER stage in this package's own code ever writes it.
// A store error in gather/interpret/persist's own code path is still the
// exit-1 case above; only a failure from the sync stage itself (below) sets
// sync_error, and it does so via the SAME existing UpsertInterpretation
// writer persist already uses, never a new store method. The sync failure
// is then ALSO returned as a Run-level error (pg2-kftf9.1) so pg-router
// retries it — see the sync stage's own doc comment below.
//
// # Automatic retry of a sync_error (bead pg2-xb6fs)
//
// Every failed PR run of an entity that has a recorded sync_error — whether
// the sync stage itself failed, or an earlier stage failed on a later
// attempt — counts one attempt in that entity's store.SyncRetry, classified
// and scheduled by internal/sync's RetryPolicy (transient failures back off
// exponentially up to a retry bound; non-transient ones are never retried
// automatically). A successful PR run deletes the state along with clearing
// sync_error. Reconcile re-drives only the sync_error rows whose retry is
// due, so a transient failure heals on a later scheduled pass without an
// operator, and a persistent one stops after the bound.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/sync"
)

// gatherer is the subset of *gather.Gatherer's API this package depends
// on, defined locally (rather than accepted as *gather.Gatherer directly)
// so tests can inject a fake implementation without needing a real
// pg-connector subprocess on $PATH — this packet's own Contract pins only
// the signature ("Consumes: gather's Gather(ctx, entityType, entityID,
// change ChangeKind) (Facts, error)"), not the concrete type.
// *gather.Gatherer satisfies this interface by construction.
type gatherer interface {
	Gather(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error)
}

// openLister is the optional capability Reconcile uses to read the current
// open set (bead pg2-hpakl). It is a separate interface, asserted at use, so a
// gatherer without it (a test double) just re-reads every open anchor.
// *gather.Gatherer satisfies it.
type openLister interface {
	ListOpenIDs(ctx context.Context, entityType, query string) ([]string, error)
}

// syncer is the subset of *sync.Syncer's API this package depends on,
// defined locally for the same reason gatherer is: tests inject a fake
// implementation without needing a real pg-connector subprocess on $PATH.
// *sync.Syncer satisfies this interface by construction.
type syncer interface {
	Sync(ctx context.Context, repo, entityID string, change gather.ChangeKind, facts gather.Facts, interp interpret.Interpretation) error
	// AnchorStale/StampClosedAnchor back Reconcile's closed-anchor audit
	// (bead pg2-a6aw6).
	AnchorStale(ctx context.Context, repo, entityID string) (bool, error)
	StampClosedAnchor(ctx context.Context, repo, entityID, reason string) error
}

// Pipeline wires gather -> interpret -> store -> sync for one entity per
// Run call. Not safe for concurrent Run calls on the same entity — mirrors
// gather.Gatherer's own "not safe for concurrent Gather calls" contract,
// since Pipeline holds exactly one Gatherer.
type Pipeline struct {
	cfg      *config.Config
	store    *store.Store
	gatherer gatherer
	// entityGatherers is the generic per-entity-type gather registry used
	// by RunGenericEntity; populated once in New() from the real Gatherer.
	entityGatherers map[string]gather.EntityGatherer
	// extractors is the per-type link-extractor registry run after persist.
	extractors ExtractorRegistry
	syncer     syncer
	clock      interpret.Clock
	verbose    bool
	out        io.Writer

	reconcileBudget time.Duration // 0 = unbounded (pg2-a5z69)

	// retryPolicy schedules and bounds automatic retries of a recorded
	// sync_error; retryAll makes Reconcile ignore it (pg2-xb6fs).
	retryPolicy sync.RetryPolicy
	retryAll    bool
}

// Option configures a Pipeline constructed by New.
type Option func(*Pipeline)

// WithClock overrides the clock Interpret is stamped with and every log
// line's duration is measured against. Production always uses the
// default (interpret.SystemClock{}); tests inject a fixed clock for the
// deterministic acceptance criterion.
func WithClock(c interpret.Clock) Option {
	return func(p *Pipeline) { p.clock = c }
}

// WithVerbose enables the --verbose three-stage timeline
// [design 7.9: "--verbose on run prints the three-stage timeline"].
func WithVerbose(v bool) Option {
	return func(p *Pipeline) { p.verbose = v }
}

// WithReconcileBudget bounds one Reconcile pass to roughly d of wall-clock
// time (bead pg2-a5z69). The budget is checked between candidates, so the
// candidate in flight always finishes; d <= 0 means unbounded.
func WithReconcileBudget(d time.Duration) Option {
	return func(p *Pipeline) { p.reconcileBudget = d }
}

// WithReconcileRetryAll makes Reconcile re-drive every recorded sync_error
// now, ignoring the automatic-retry policy — backoff, the retry bound, and
// the non-transient classification alike (bead pg2-xb6fs). It is the
// operator's manual repair (`pg-desk reconcile --retry-all`) once the cause
// of an exhausted or non-transient row is fixed; scheduled runs never set it.
func WithReconcileRetryAll(v bool) Option {
	return func(p *Pipeline) { p.retryAll = v }
}

// WithSyncLogger overrides the logger the sync stage writes its diagnostics
// to (the per-write "anchor write ... cause=" line, bead pg2-kwwn2); the
// default is slog.Default().
func WithSyncLogger(l *slog.Logger) Option {
	return func(p *Pipeline) { p.syncer = sync.New(p.cfg, p.store, sync.WithLogger(l)) }
}

// WithLogWriter overrides where structured JSON logs are written.
// Production defaults to os.Stderr [design 7.9: "run logs structured
// JSON to stderr"]; cmd/pg-desk/run.go passes cmd.ErrOrStderr() instead,
// so cobra's own output redirection (and tests) both work; package tests
// capture to a buffer directly.
func WithLogWriter(w io.Writer) Option {
	return func(p *Pipeline) { p.out = w }
}

// New constructs a Pipeline backed by a fresh gather.Gatherer — exactly
// one per Pipeline instance, matching internal/gather's own documented
// expectation ("the pipeline packet, 6, is expected to construct exactly
// one Gatherer per `pg-desk run` invocation") — plus cfg and st. The
// caller owns st's lifecycle (open and Close); Pipeline never closes it.
func New(cfg *config.Config, st *store.Store, opts ...Option) *Pipeline {
	g := gather.NewGatherer(cfg, st)
	p := &Pipeline{
		cfg:             cfg,
		store:           st,
		gatherer:        g,
		entityGatherers: g.EntityGatherers(),
		extractors:      NewExtractorRegistry(cfg, st, repoOf(cfg)),
		syncer:          sync.New(cfg, st),
		clock:           interpret.SystemClock{},
		out:             os.Stderr,
		retryPolicy:     sync.RetryPolicyFor(cfg),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// stageEvent is one line of the --verbose timeline.
type stageEvent struct {
	Stage      string `json:"stage"`
	DurationMs int64  `json:"duration_ms"`
}

// runLogLine is the unconditional structured JSON log line to stderr
// [design 7.9]. Outcome is one of "ok" (clean success), "degraded" (ran
// to completion but Facts/Interpretation carried a degradation),
// "noop" (--change removed for an id the store does not know — Binding
// decisions), or "error" (non-nil Run error; also pr-pool exit 1).
type runLogLine struct {
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	Change     string `json:"change"`
	Outcome    string `json:"outcome"`
	Degraded   string `json:"degraded,omitempty"`
	Error      string `json:"error,omitempty"`
	// Stage and ErrorClass are set only on an "error" line whose error
	// carries a *StageError (bead pg2-gp50o): the pipeline stage that failed
	// and a coarse failure class (Class* constants), so a failure pg-router
	// records only as "exit status 1" can still be attributed.
	Stage      string `json:"stage,omitempty"`
	ErrorClass string `json:"error_class,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

// Run executes the gather -> interpret -> store pipeline once for one
// entity. See the package doc comment for the exit-code contract. For a PR
// entity it then records the run's outcome in the entity's sync_error retry
// state (see the package doc's "Automatic retry" section); a failure to
// record it is a store error and fails Run too.
func (p *Pipeline) Run(ctx context.Context, entityType, entityID string, change gather.ChangeKind) error {
	runErr := p.run(ctx, entityType, entityID, change)
	if entityType != entityTypePR {
		return runErr
	}
	if err := p.recordSyncRetry(entityID, runErr); err != nil {
		err = TagStage(StageSyncRetryState, err)
		if runErr == nil {
			return fmt.Errorf("pipeline: %w", err)
		}
		return errors.Join(runErr, fmt.Errorf("pipeline: %w", err))
	}
	return runErr
}

// recordSyncRetry updates entityID's retry state after one PR run: a
// success deletes it (the run also cleared sync_error); a failure of an
// entity that has a recorded sync_error counts one more attempt, classified
// from runErr. A failure of an entity with no recorded sync_error (an
// ordinary gather or store failure) records nothing: there is no sync_error
// to retry, and pg-router already retries the failed event itself.
func (p *Pipeline) recordSyncRetry(entityID string, runErr error) error {
	if runErr == nil {
		if err := p.store.DeleteSyncRetry(entityID); err != nil {
			return fmt.Errorf("clear sync retry state %s: %w", entityID, err)
		}
		return nil
	}
	interp, found, err := p.store.GetInterpretation(p.repo(), entityTypePR, entityID)
	if err != nil {
		return fmt.Errorf("read interpretation for sync retry state %s: %w", entityID, err)
	}
	if !found || interp.SyncError == "" {
		return nil
	}
	prev, _, err := p.store.GetSyncRetry(entityID)
	if err != nil {
		// A corrupt state value restarts the count rather than wedging the
		// row: the zero value is what a first failure would see.
		prev = store.SyncRetry{}
	}
	next := p.retryPolicy.NextState(prev, runErr, p.clock.Now())
	if err := p.store.SetSyncRetry(entityID, next); err != nil {
		return fmt.Errorf("record sync retry state %s: %w", entityID, err)
	}
	return nil
}

// run is Run's pipeline body, without the retry-state bookkeeping.
func (p *Pipeline) run(ctx context.Context, entityType, entityID string, change gather.ChangeKind) error {
	runStart := p.clock.Now()
	var timeline []stageEvent

	stageStart := p.clock.Now()
	facts, err := p.gatherer.Gather(ctx, entityType, entityID, change)
	timeline = append(timeline, stageEvent{Stage: "gather", DurationMs: p.clock.Now().Sub(stageStart).Milliseconds()})
	if err != nil {
		err = TagStage(StageGather, err)
		p.logRun(entityType, entityID, change, "error", "", err, runStart)
		return fmt.Errorf("pipeline: gather %s %s: %w", entityType, entityID, err)
	}

	if change == gather.ChangeRemoved {
		// Binding decisions > "--change removed": an id the store does not
		// know is a no-op — exits 0, no rows written. This applies to
		// every RemovedState (open/merged/closed/not_found) alike; only
		// whether the store has EVER heard of this (repo, type, id) decides
		// the no-op, not what the re-read found.
		_, known, getErr := p.store.GetEntity(p.repo(), entityType, entityID)
		if getErr != nil {
			getErr = TagStage(StageKnownCheck, getErr)
			p.logRun(entityType, entityID, change, "error", "", getErr, runStart)
			return fmt.Errorf("pipeline: check entity known %s %s: %w", entityType, entityID, getErr)
		}
		if !known {
			p.logRun(entityType, entityID, change, "noop", facts.Degraded, nil, runStart)
			return nil
		}
	}

	stageStart = p.clock.Now()
	interp, err := interpret.Interpret(facts, p.clock, p.cfg)
	timeline = append(timeline, stageEvent{Stage: "interpret", DurationMs: p.clock.Now().Sub(stageStart).Milliseconds()})
	if err != nil {
		err = TagStage(StageInterpret, err)
		p.logRun(entityType, entityID, change, "error", "", err, runStart)
		return fmt.Errorf("pipeline: interpret %s %s: %w", entityType, entityID, err)
	}

	stageStart = p.clock.Now()
	interpRow, err := p.persist(entityType, entityID, facts, interp)
	timeline = append(timeline, stageEvent{Stage: "store", DurationMs: p.clock.Now().Sub(stageStart).Milliseconds()})
	if err != nil {
		err = TagStage(StageStore, err)
		p.logRun(entityType, entityID, change, "error", "", err, runStart)
		return fmt.Errorf("pipeline: store %s %s: %w", entityType, entityID, err)
	}

	// Sync stage (docket pg2-2j5ac.34, Phase 10) — entityType == "pr" only
	// (see the package doc comment), and only once the entity/interpretation
	// rows above are durably persisted. A sync failure is recorded ON that
	// same interpretation row (sync_error, the diagnostic) AND returned as a
	// Run error (pg2-kftf9.1): a swallowed failure exited 0, so pg-router
	// treated the dispatch as success and never retried (25 merge-request
	// anchors stayed open after their PRs closed). Returning it makes the
	// event retry with pg-router's backoff and count in its failure metrics.
	// It is still an ordinary error -> exit 1 via main.go: never os.Exit(9),
	// never a raw pg-connector exit code. Retries are safe: sync closure is
	// ledger-guarded and re-entrant, and the next successful run's persist
	// rewrites the row with an empty sync_error.
	if entityType == entityTypePR {
		if syncErr := p.syncer.Sync(ctx, p.repo(), entityID, change, facts, interp); syncErr != nil {
			interpRow.SyncError = syncErr.Error()
			if upsertErr := p.store.UpsertInterpretation(interpRow); upsertErr != nil {
				// A failure to even RECORD the sync error is a genuine store
				// error (this package's own exit-1 case), unlike the sync
				// failure itself.
				upsertErr = TagStage(StageRecordSyncError, upsertErr)
				p.logRun(entityType, entityID, change, "error", "", upsertErr, runStart)
				return fmt.Errorf("pipeline: record sync_error %s %s: %w", entityType, entityID, upsertErr)
			}
			syncErr = TagStage(StageSync, syncErr)
			p.logRun(entityType, entityID, change, "error", interp.Degraded, syncErr, runStart)
			return fmt.Errorf("pipeline: sync %s %s: %w", entityType, entityID, syncErr)
		}
	}

	outcome := "ok"
	if interp.Degraded != "" {
		outcome = "degraded"
	}
	p.logRun(entityType, entityID, change, outcome, interp.Degraded, nil, runStart)
	if p.verbose {
		p.printTimeline(timeline)
	}
	return nil
}

// RunInterpretOnly re-runs stage 2 (interpret) only, for an entity whose
// facts were already gathered by an earlier "pr"-triggered Run call —
// cmd/pg-desk/run.go's "issue" case (Phase 10, docket pg2-2j5ac.34): an
// issue event re-runs interpretation for the PR it links to, WITHOUT
// invoking gather again (internal/gather.Gatherer.Gather rejects any
// entityType other than "pr" outright, and this path's entityID is
// resolved from the triggering bead before this method is ever called —
// see packages/pg-desk/internal/beadref) and WITHOUT invoking the sync
// stage [Binding decisions: "the sync stage is NOT re-invoked by this
// path either"]. change is carried only for this call's own structured
// log line [design 7.9] and is never branched on [Binding decisions:
// "added/changed/removed/sweep all resolve the SAME triggering bead's
// linked PR the same way"].
//
// Returns a non-nil error (never a panic) when change is not one of the
// four valid kinds, when the entity has never been gathered (no stored
// facts to re-interpret), or on an interpret/store failure — matching
// Run's own exit-code contract (see the package doc comment): nil on
// success or a degraded-but-completed run, non-nil otherwise, never
// os.Exit, never a raw pg-connector code.
func (p *Pipeline) RunInterpretOnly(ctx context.Context, entityType, entityID string, change gather.ChangeKind) error {
	runStart := p.clock.Now()

	switch change {
	case gather.ChangeAdded, gather.ChangeChanged, gather.ChangeRemoved, gather.ChangeSweep:
	default:
		err := TagStage(StageInterpretOnlyArgs, fmt.Errorf("pipeline: interpret-only %s %s: change kind %q is not one of added/changed/removed/sweep", entityType, entityID, change))
		p.logRun(entityType, entityID, change, "error", "", err, runStart)
		return err
	}

	entity, found, err := p.store.GetEntity(p.repo(), entityType, entityID)
	if err != nil {
		err = TagStage(StageLoadFacts, err)
		p.logRun(entityType, entityID, change, "error", "", err, runStart)
		return fmt.Errorf("pipeline: interpret-only get entity %s %s: %w", entityType, entityID, err)
	}
	if !found {
		noEntityErr := TagStage(StageLoadFacts, fmt.Errorf("pipeline: interpret-only %s %s: no stored entity facts (never gathered)", entityType, entityID))
		p.logRun(entityType, entityID, change, "error", "", noEntityErr, runStart)
		return noEntityErr
	}

	var facts gather.Facts
	if err := json.Unmarshal([]byte(entity.Facts), &facts); err != nil {
		err = TagStage(StageLoadFacts, err)
		p.logRun(entityType, entityID, change, "error", "", err, runStart)
		return fmt.Errorf("pipeline: interpret-only decode stored facts %s %s: %w", entityType, entityID, err)
	}

	interp, err := interpret.Interpret(facts, p.clock, p.cfg)
	if err != nil {
		err = TagStage(StageInterpret, err)
		p.logRun(entityType, entityID, change, "error", "", err, runStart)
		return fmt.Errorf("pipeline: interpret-only interpret %s %s: %w", entityType, entityID, err)
	}

	// Preserve any sync_error a prior sync-stage run already recorded on
	// this row: this path never invokes sync (see doc comment above), so
	// it must not silently erase it. Zero value ("") when no
	// interpretation row exists yet, or none was ever recorded — same
	// effect as a fresh row.
	prior, _, err := p.store.GetInterpretation(p.repo(), entityType, entityID)
	if err != nil {
		err = TagStage(StageStore, err)
		p.logRun(entityType, entityID, change, "error", "", err, runStart)
		return fmt.Errorf("pipeline: interpret-only get prior interpretation %s %s: %w", entityType, entityID, err)
	}

	interpRow, err := p.persist(entityType, entityID, facts, interp)
	if err != nil {
		err = TagStage(StageStore, err)
		p.logRun(entityType, entityID, change, "error", "", err, runStart)
		return fmt.Errorf("pipeline: interpret-only store %s %s: %w", entityType, entityID, err)
	}
	if prior.SyncError != "" {
		interpRow.SyncError = prior.SyncError
		if err := p.store.UpsertInterpretation(interpRow); err != nil {
			err = TagStage(StageStore, err)
			p.logRun(entityType, entityID, change, "error", "", err, runStart)
			return fmt.Errorf("pipeline: interpret-only restore sync_error %s %s: %w", entityType, entityID, err)
		}
	}

	outcome := "ok"
	if interp.Degraded != "" {
		outcome = "degraded"
	}
	p.logRun(entityType, entityID, change, outcome, interp.Degraded, nil, runStart)
	if p.verbose {
		p.printTimeline([]stageEvent{{Stage: "interpret", DurationMs: p.clock.Now().Sub(runStart).Milliseconds()}})
	}
	return nil
}

// Sweep implements `pg-desk sweep` (bead pg2-gznpe): the operator's
// "recompute everything" backfill — it re-runs the full gather/interpret/
// store/sync pipeline (Run, above; change is always ChangeSweep, matching
// the manual per-entity workaround this command replaces:
// `pg-desk run pr <id>` with no --change, which already defaults to
// sweep) for every entity in entities, in the given order, then stamps
// meta.last_sweep once the pass over the whole list has been attempted.
//
// Every entity is attempted even after an earlier one fails — mirrors
// cmd/pg-desk/run.go's own runJiraIssue/runThread "attempt every one,
// join errors" convention, one layer down: a handful of stale or
// since-deleted entities must not stop the rest of the backfill. Errors
// (per-entity Run failures, and a meta.last_sweep write failure) are
// joined into a single returned error; last_sweep is still stamped even
// when one or more entities failed, since the SWEEP ITSELF — the attempt
// to recompute every entity — did complete, mirroring Run's own "a
// degraded-but-completed run is still success" contract (see the package
// doc comment) rather than requiring every entity to succeed before the
// dashboard's last_sweep_at is allowed to advance.
//
// This is distinct from, and does NOT implement, sync.md's still-out-of-
// scope "store-wide sweep that re-verifies every ledger row whose entity
// has left every gathered query" — that needs a driver over the LEDGER
// table (which entities have vanished from every live query); this Sweep
// only re-runs the pipeline for entities the ENTITY table already knows
// about.
func (p *Pipeline) Sweep(ctx context.Context, entities []store.Entity) error {
	var errs []error
	for _, e := range entities {
		if err := p.Run(ctx, e.EntityType, e.EntityID, gather.ChangeSweep); err != nil {
			errs = append(errs, fmt.Errorf("sweep %s %s: %w", e.EntityType, e.EntityID, err))
		}
	}

	now := p.clock.Now().UTC().Format(time.RFC3339)
	if err := p.store.SetMeta(store.MetaKeyLastSweep, now); err != nil {
		errs = append(errs, fmt.Errorf("sweep: record meta.last_sweep: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("pipeline: sweep: %w", errors.Join(errs...))
	}
	return nil
}

// Reconcile implements `pg-desk reconcile` (bead pg2-kftf9.2): an
// event-independent repair pass over the ledger. Closure is otherwise
// driven only by the one-shot `removed` event, so a PR that has left every
// open query is never visited again and a single failed closure was
// permanent. Reconcile re-drives, via the SAME Run(ChangeRemoved) path the
// event uses (so closure stays ledger-guarded and re-entrant), every PR
// entity that has either
//
//   - a non-closed anchor ledger row whose PR is now merged/closed/gone
//     (confirmed by a `--change removed` re-read; an anchor whose PR is
//     still open is left alone), or
//   - a recorded sync_error whose automatic retry is due (re-driven
//     regardless of PR state; a successful run clears it).
//
// # Automatic retry (bead pg2-xb6fs)
//
// A sync_error row is re-driven only when sync.RetryDue says so: its
// transient failure's backoff has elapsed and the retry bound is not
// reached, or it has no recorded retry state yet. An exhausted or
// non-transient row, or one still backing off, is HELD: it is not re-driven
// by either path above (an open anchor on a held row is not re-read
// either — re-driving it would be one more retry of the same failing sync),
// though a closed-anchor audit still runs. WithReconcileRetryAll lifts the
// hold for an operator's manual repair.
//
// A PR whose review request is waiting out a head's settle window
// (sync.review_settle_window, bead pg2-a9yhn) is also a candidate once that
// window has elapsed: sync runs only on events, so a quiet PR whose last push
// settled would otherwise wait for its next unrelated event before the review
// of that head is requested. Such a candidate is re-driven by an ordinary
// sweep run, which reopens the review bead through the same ledger-guarded
// path. How soon after the window this happens is bounded by how often the
// scheduler runs reconcile.
//
// It also audits every ledger-closed anchor once (bead pg2-a6aw6): an
// anchor closed by an earlier review can keep stale bead metadata
// (state=open, no closed_at); a stale one is stamped with the truthful
// terminal state from a fresh PR re-read, with no operator action.
//
// It is idempotent: once an anchor is closed, audited and sync_error is
// empty the entity is no longer a candidate. Every candidate is attempted even after
// one fails; failures are joined into the returned error (exit 1). Unlike
// Sweep it does not touch meta.last_sweep.
//
// # Open set and transient failures (bead pg2-hpakl)
//
// An open anchor is re-read only when its PR is absent from the current open
// set: the ids the watched PR queries list right now (one ids-only listing per
// query, reconcileOpenSet). A PR a query still lists cannot have left the open
// set, so re-reading it (a ~4s `pr show --fresh`) bought nothing. When the
// open set cannot be read every open anchor is re-read, as before.
//
// A candidate whose READ of the PR (the peek, or the closed-anchor audit's
// re-read) fails transiently (a killed or timed-out connector child, or
// pg-connector's `unavailable` code, see transientClass) is logged as
// {"event":"reconcile_deferred",...} and does NOT fail the pass: the next
// scheduled pass retries it, since it keeps its place as a candidate. It fails
// the pass again once it has been deferred reconcileMaxDeferrals passes in a
// row (meta reconcile.deferred.<id>, cleared by its next success). Every
// other failure (a non-transient read failure, or any failure of the closure
// Run, which carries its own sync_error and retry bound) fails the pass at
// once.
//
// # Budget and convergence (bead pg2-a5z69)
//
// Each candidate costs a pg-connector re-read (~4s), so a large backlog can
// outlive the caller's timeout. With WithReconcileBudget set, Reconcile
// stops starting new candidates once the budget elapsed and returns nil
// (NOT an error: a budget stop is progress, not a failed re-drive) after
// logging a {"event":"reconcile_budget_exhausted","processed":N,
// "remaining":M} line. Candidates are visited oldest-checked first, using a
// per-entity meta stamp (reconcile.checked.<id>) written after every attempt
// (success, still-open, or failure), so successive bounded runs cover the
// whole backlog rather than re-reading the same head of the list.
func (p *Pipeline) Reconcile(ctx context.Context) error {
	repo := p.repo()
	ledger, err := p.store.ListLedger()
	if err != nil {
		return fmt.Errorf("pipeline: reconcile: %w", err)
	}
	interps, err := p.store.ListInterpretations()
	if err != nil {
		return fmt.Errorf("pipeline: reconcile: %w", err)
	}

	// Split the sync_error rows into due (re-driven below) and held (not
	// re-driven by either path) per the automatic-retry policy (pg2-xb6fs).
	now := p.clock.Now()
	var syncErrDue []string
	held := map[string]bool{}
	for _, i := range interps {
		if i.Repo != repo || i.EntityType != entityTypePR || i.SyncError == "" {
			continue
		}
		due := p.retryAll
		if !due {
			r, found, rErr := p.store.GetSyncRetry(i.EntityID)
			// Unreadable state is treated like none: due, so the re-drive
			// records fresh state.
			due = rErr != nil || sync.RetryDue(r, found, now)
		}
		if due {
			syncErrDue = append(syncErrDue, i.EntityID)
		} else {
			held[i.EntityID] = true
		}
	}

	// needsPeek[id]=true: anchor-open candidate that must be confirmed
	// merged/closed first; false: sync_error candidate, always re-driven.
	// An id absent from needsPeek is not re-driven (audit-only).
	needsPeek := map[string]bool{}
	audit := map[string]bool{}
	settle := map[string]bool{} // review settle window elapsed (pg2-a9yhn)
	var order []string
	inOrder := map[string]bool{}
	touch := func(id string) {
		if !inOrder[id] {
			inOrder[id] = true
			order = append(order, id)
		}
	}
	add := func(id string, peek bool) {
		prev, seen := needsPeek[id]
		touch(id)
		if !seen {
			needsPeek[id] = peek
			return
		}
		needsPeek[id] = prev && peek
	}
	settleWindow := p.cfg.ReviewSettleWindow()
	// Only an anchor whose PR is absent from the current open set can have
	// left it, so the open set is read first (one ids-only listing per watched
	// query) and a listed anchor is not re-read at all (bead pg2-hpakl). It is
	// read only when some open anchor could use it.
	var openSet map[string]bool
	for _, l := range ledger {
		if l.Repo == repo && l.EntityType == entityTypePR && l.Kind == sync.KindAnchor && l.BeadID != "" &&
			l.LastSyncedContentHash != sync.ClosedSentinel && !held[l.EntityID] {
			openSet = p.reconcileOpenSet(ctx)
			break
		}
	}
	skippedOpen := 0
	for _, l := range ledger {
		if l.Repo == repo && l.EntityType == entityTypePR && !held[l.EntityID] && sync.SettleDue(l, settleWindow, now) {
			settle[l.EntityID] = true
			touch(l.EntityID)
		}
		if l.Repo != repo || l.EntityType != entityTypePR || l.Kind != sync.KindAnchor || l.BeadID == "" {
			continue
		}
		if l.LastSyncedContentHash != sync.ClosedSentinel {
			if held[l.EntityID] {
				continue
			}
			if openSet[l.EntityID] {
				skippedOpen++
				continue
			}
			add(l.EntityID, true)
			continue
		}
		// Closed anchor: audit its bead's metadata once (pg2-a6aw6).
		_, audited, mErr := p.store.GetMeta(reconcileAuditedKeyPrefix + l.EntityID)
		if mErr != nil {
			return fmt.Errorf("pipeline: reconcile: %w", mErr)
		}
		if !audited {
			audit[l.EntityID] = true
			touch(l.EntityID)
		}
	}
	for _, id := range syncErrDue {
		add(id, false)
	}

	// Oldest-checked (or never-checked) first; stable so ties keep the
	// deterministic ledger/interpretation order.
	checkedAt := map[string]string{}
	deferrals := map[string]int{}
	for _, id := range order {
		v, _, mErr := p.store.GetMeta(reconcileCheckedKeyPrefix + id)
		if mErr != nil {
			return fmt.Errorf("pipeline: reconcile: %w", mErr)
		}
		checkedAt[id] = v
		d, found, mErr := p.store.GetMeta(reconcileDeferredKeyPrefix + id)
		if mErr != nil {
			return fmt.Errorf("pipeline: reconcile: %w", mErr)
		}
		if found {
			n, aErr := strconv.Atoi(d)
			if aErr != nil || n < 0 {
				n = 0 // a corrupt counter restarts the count rather than wedging the row
			}
			deferrals[id] = n
		}
	}
	sort.SliceStable(order, func(a, b int) bool { return checkedAt[order[a]] < checkedAt[order[b]] })

	if openSet != nil {
		line, _ := json.Marshal(map[string]any{
			"event": "reconcile_open_set", "open_ids": len(openSet), "skipped_open": skippedOpen, "candidates": len(order),
		})
		_, _ = fmt.Fprintln(p.out, string(line))
	}

	start := p.clock.Now()
	var errs []error
	for idx, id := range order {
		if p.reconcileBudget > 0 && idx > 0 && p.clock.Now().Sub(start) >= p.reconcileBudget {
			line, _ := json.Marshal(map[string]any{
				"event": "reconcile_budget_exhausted", "processed": idx, "remaining": len(order) - idx,
			})
			_, _ = fmt.Fprintln(p.out, string(line))
			break
		}
		p.markReconcileChecked(id)
		err := p.reconcileCandidate(ctx, id, needsPeek, settle, audit)
		switch {
		case err == nil:
			if deferrals[id] > 0 {
				_ = p.store.DeleteMeta(reconcileDeferredKeyPrefix + id) // best-effort, like the stamp
			}
		case deferrable(ctx, err, deferrals[id]):
			deferrals[id]++
			_ = p.store.SetMeta(reconcileDeferredKeyPrefix+id, strconv.Itoa(deferrals[id]))
			line, _ := json.Marshal(map[string]any{
				"event": "reconcile_deferred", "entity_id": id, "error_class": transientClass(err),
				"consecutive": deferrals[id], "max": reconcileMaxDeferrals, "error": err.Error(),
			})
			_, _ = fmt.Fprintln(p.out, string(line))
		default:
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("pipeline: reconcile: %w", errors.Join(errs...))
	}
	return nil
}

// reconcileCandidate runs one candidate's re-drive, settled-review re-run and
// closed-anchor audit.
func (p *Pipeline) reconcileCandidate(ctx context.Context, id string, needsPeek, settle, audit map[string]bool) error {
	if peek, redrive := needsPeek[id]; redrive {
		if err := p.reconcileRedrive(ctx, id, peek); err != nil {
			return err
		}
	}
	if settle[id] {
		// Re-driven through the ordinary run path; the anchor-closure
		// path above (if it ran) already cleared the pending head when
		// the PR had left the open set, so this is then a cheap no-op.
		if err := p.Run(ctx, entityTypePR, id, gather.ChangeSweep); err != nil {
			return fmt.Errorf("reconcile %s: settled review: %w", id, err)
		}
	}
	if audit[id] {
		if err := p.reconcileAuditClosedAnchor(ctx, p.repo(), id); err != nil {
			return fmt.Errorf("reconcile %s: %w", id, err)
		}
	}
	return nil
}

// reconcileOpenSet returns the ids the watched PR queries currently list (one
// ids-only listing per query, bead pg2-hpakl), or nil when it cannot be
// known: the gatherer cannot list, no PR query is configured, or every
// listing failed. nil makes every open anchor a re-read candidate, the
// pre-existing behavior, so a failed listing only costs time, never closure.
// A query whose listing fails or is partial just contributes fewer ids.
func (p *Pipeline) reconcileOpenSet(ctx context.Context) map[string]bool {
	lister, ok := p.gatherer.(openLister)
	if !ok || p.cfg == nil {
		return nil
	}
	queries := p.cfg.WatchQueries(entityTypePR)
	if len(queries) == 0 {
		return nil
	}
	set := map[string]bool{}
	listed := 0
	for _, q := range queries {
		ids, err := lister.ListOpenIDs(ctx, entityTypePR, q)
		if err != nil {
			line, _ := json.Marshal(map[string]any{"event": "reconcile_open_set_query_failed", "query": q, "error": err.Error()})
			_, _ = fmt.Fprintln(p.out, string(line))
			continue
		}
		listed++
		for _, id := range ids {
			set[id] = true
		}
	}
	if listed == 0 {
		return nil
	}
	return set
}

// reconcileMaxDeferrals is how many CONSECUTIVE passes may defer one
// candidate's transient failure before the next such failure is a failure
// (exit 1) again: a read that keeps being killed is a real problem, not a
// blip, and must reach the failure metric (bead pg2-hpakl).
const reconcileMaxDeferrals = 3

// readError marks a failure of Reconcile's own read of a PR (the peek that
// confirms an anchor's PR left the open set, or the closed-anchor audit's
// re-read), as opposed to a failure of the closure Run that follows. Its text
// is the wrapped error's, unchanged.
type readError struct{ err error }

func (e *readError) Error() string { return e.err.Error() }
func (e *readError) Unwrap() error { return e.err }

// deferrable reports whether err is a transient connector failure that this
// pass may log as deferred instead of failing on: a candidate READ (readError)
// whose child was killed, whose deadline expired, or which pg-connector
// answered "unavailable". A failed closure Run is never deferred: it records
// sync_error and its own retry state, and its exit-1 contract is unchanged.
// Nor is it ever deferred when the pass's own context is done (the caller is
// cancelling the whole run), once the candidate has already been deferred
// reconcileMaxDeferrals passes in a row, or for any other class: non-transient
// failures (a missing binary, a store error, an auth or validation failure)
// still fail the run.
func deferrable(ctx context.Context, err error, priorDeferrals int) bool {
	var re *readError
	if !errors.As(err, &re) || ctx.Err() != nil || priorDeferrals >= reconcileMaxDeferrals {
		return false
	}
	return transientClass(err) != ""
}

// unavailableRe matches pg-connector's `unavailable` wire code as the gather
// layer renders it into an error ("...: exit 1: unavailable: <message>").
var unavailableRe = regexp.MustCompile(`exit \d+: unavailable:`)

// transientClass returns the deferrable class of err (ClassKilled,
// ClassDeadline or "unavailable"), or "" when err is not deferrable.
func transientClass(err error) string {
	switch ClassifyError(err) {
	case ClassKilled:
		return ClassKilled
	case ClassDeadline:
		return ClassDeadline
	}
	var ce *sync.ConnectorError
	if errors.As(err, &ce) && ce.Code == "unavailable" {
		return "unavailable"
	}
	if unavailableRe.MatchString(err.Error()) {
		return "unavailable"
	}
	return ""
}

// reconcileRedrive re-drives closure for one candidate: peek=true confirms
// the PR left the open set first (a still-open PR is left alone).
func (p *Pipeline) reconcileRedrive(ctx context.Context, id string, peek bool) error {
	if peek {
		facts, err := p.gatherer.Gather(ctx, entityTypePR, id, gather.ChangeRemoved)
		if err != nil {
			return &readError{fmt.Errorf("reconcile %s: %w", id, err)}
		}
		if facts.RemovedState == "open" {
			return nil // still open: nothing to close
		}
	}
	if err := p.Run(ctx, entityTypePR, id, gather.ChangeRemoved); err != nil {
		return fmt.Errorf("reconcile %s: %w", id, err)
	}
	return nil
}

// reconcileAuditClosedAnchor repairs a closed anchor whose bead metadata
// was left stale (state=open, no closed_at) by an earlier review that
// closed it before handleClosure stamped the terminal state (bead
// pg2-a6aw6). Policy: the truthful state comes from a fresh PR re-read
// (merged, else closed), closed_at is the repair time, and each anchor is
// audited exactly once (reconcile.anchor-audited.<id>) so the per-anchor
// `issue show` cost is not repaid on every pass. A PR that has since
// reopened is left unmarked and unrepaired.
func (p *Pipeline) reconcileAuditClosedAnchor(ctx context.Context, repo, id string) error {
	stale, err := p.syncer.AnchorStale(ctx, repo, id)
	if err != nil {
		return err
	}
	if stale {
		facts, gErr := p.gatherer.Gather(ctx, entityTypePR, id, gather.ChangeRemoved)
		if gErr != nil {
			return &readError{gErr}
		}
		if facts.RemovedState == "open" || facts.RemovedState == "" {
			return nil
		}
		reason := "closed"
		if facts.RemovedState == "merged" {
			reason = "merged"
		}
		if err := p.syncer.StampClosedAnchor(ctx, repo, id, reason); err != nil {
			return err
		}
	}
	// Best-effort mark: a failed stamp only costs one repeated audit.
	_ = p.store.SetMeta(reconcileAuditedKeyPrefix+id, p.clock.Now().UTC().Format(time.RFC3339Nano))
	return nil
}

// reconcileAuditedKeyPrefix prefixes the per-entity meta key recording that
// a closed anchor's bead metadata has been audited (and repaired if stale).
const reconcileAuditedKeyPrefix = "reconcile.anchor-audited."

// reconcileCheckedKeyPrefix prefixes the per-entity meta key recording when
// Reconcile last attempted that entity (RFC3339Nano UTC).
const reconcileCheckedKeyPrefix = "reconcile.checked."

// reconcileDeferredKeyPrefix prefixes the per-entity meta key counting the
// consecutive passes that deferred that entity's transient failure; deleted by
// the entity's next successful attempt.
const reconcileDeferredKeyPrefix = "reconcile.deferred."

// markReconcileChecked stamps id as attempted now. Best-effort: a failed
// stamp only costs ordering fidelity on the next run, never correctness.
func (p *Pipeline) markReconcileChecked(id string) {
	_ = p.store.SetMeta(reconcileCheckedKeyPrefix+id, p.clock.Now().UTC().Format(time.RFC3339Nano))
}

// entityTypePR is the one entity type the sync stage runs for — mirrors
// cmd/pg-desk/desk.go's own entityTypePR constant (this package does not
// import cmd/pg-desk, so it is repeated here rather than shared).
const entityTypePR = "pr"

// repo returns the single Phase-9 configured repository's remote, or ""
// if none is configured — mirrors internal/gather's own
// g.cfg.Repos[0]... convention (Phase 9 supports exactly one repo).
func (p *Pipeline) repo() string {
	if p.cfg == nil || len(p.cfg.Repos) == 0 {
		return ""
	}
	return p.cfg.Repos[0].Remote
}

// persist writes facts and interp to the entity and interpretation
// tables, returning the interpretation row it wrote so the sync stage
// (Run, above) can update its sync_error field via the SAME writer, on the
// SAME row, rather than reconstructing it or adding a new store method.
// Never touches the annotation table — see the package doc comment.
func (p *Pipeline) persist(entityType, entityID string, facts gather.Facts, interp interpret.Interpretation) (store.Interpretation, error) {
	repo := p.repo()

	factsJSON, err := json.Marshal(facts)
	if err != nil {
		return store.Interpretation{}, fmt.Errorf("marshal facts: %w", err)
	}
	// entity.as_of prefers the data's own as-of (from pg-connector, via
	// gather); it falls back to the interpretation's clock-stamped as-of
	// only when gather returned no data at all (the removed/not_found
	// case, where Facts.AsOf is empty) — the column is NOT NULL, so it
	// must always carry a value.
	asOf := facts.AsOf
	if asOf == "" {
		asOf = interp.AsOf
	}
	if err := p.store.UpsertEntity(store.Entity{
		Repo:       repo,
		EntityType: entityType,
		EntityID:   entityID,
		Facts:      string(factsJSON),
		AsOf:       asOf,
		// Stale: no wire-level staleness field reaches gather.Facts in
		// this phase (gather.Facts carries no such field) — always false,
		// the same "no data source yet" default interpret.go documents
		// for GateState.
		Stale:       false,
		ContentHash: contentHash(factsJSON),
		HeadSHA:     facts.HeadSHA,
	}); err != nil {
		return store.Interpretation{}, fmt.Errorf("upsert entity: %w", err)
	}

	enrichmentJSON, err := json.Marshal(interp.Enrichment)
	if err != nil {
		return store.Interpretation{}, fmt.Errorf("marshal enrichment: %w", err)
	}
	urgencyJSON, err := json.Marshal(interp.Urgency)
	if err != nil {
		return store.Interpretation{}, fmt.Errorf("marshal urgency: %w", err)
	}
	dispositionsJSON, err := json.Marshal(interp.Dispositions)
	if err != nil {
		return store.Interpretation{}, fmt.Errorf("marshal dispositions: %w", err)
	}
	approvalsJSON, err := json.Marshal(interp.Approvals)
	if err != nil {
		return store.Interpretation{}, fmt.Errorf("marshal approvals: %w", err)
	}
	matchReasonsJSON, err := json.Marshal(interp.MatchReasons)
	if err != nil {
		return store.Interpretation{}, fmt.Errorf("marshal match reasons: %w", err)
	}

	interpRow := store.Interpretation{
		Repo:           repo,
		EntityType:     entityType,
		EntityID:       entityID,
		Ownership:      interp.Ownership,
		Enrichment:     string(enrichmentJSON),
		Urgency:        string(urgencyJSON),
		Category:       interp.Category,
		Dispositions:   string(dispositionsJSON),
		Approvals:      string(approvalsJSON),
		GateState:      interp.GateState,
		MatchReasons:   string(matchReasonsJSON),
		Panel:          interp.Panel,
		ReadyToPromote: interp.ReadyToPromote,
		Degraded:       interp.Degraded != "",
		SyncError:      "", // set by the sync stage (Run, above) only on a sync failure
		AsOf:           interp.AsOf,
	}
	if err := p.store.UpsertInterpretation(interpRow); err != nil {
		return store.Interpretation{}, fmt.Errorf("upsert interpretation: %w", err)
	}
	return interpRow, nil
}

// contentHash is this packet's own deterministic content-hash algorithm
// for the entity table's content_hash column: no other packet pins an
// algorithm (internal/store/entity.go's Entity.ContentHash doc names the
// column, not a scheme), so this is a plain sha256 hex digest of the
// marshaled Facts JSON — deterministic and collision-safe enough for its
// only job (comparing two point-in-time reads for equality).
func contentHash(factsJSON []byte) string {
	sum := sha256.Sum256(factsJSON)
	return hex.EncodeToString(sum[:])
}

// logRun writes the unconditional structured JSON log line for one run
// [design 7.9]. Never returns an error: a log-encoding failure (nil
// runErr aside, this can only happen if json.Marshal itself fails, which
// it cannot for this fixed, all-string/int shape) falls back to a plain
// line rather than losing the run's outcome entirely.
func (p *Pipeline) logRun(entityType, entityID string, change gather.ChangeKind, outcome, degraded string, runErr error, start time.Time) {
	line := runLogLine{
		EntityType: entityType,
		EntityID:   entityID,
		Change:     string(change),
		Outcome:    outcome,
		Degraded:   degraded,
		DurationMs: p.clock.Now().Sub(start).Milliseconds(),
	}
	if runErr != nil {
		line.Error = runErr.Error()
		line.Stage, line.ErrorClass = StageOf(runErr)
	}
	b, err := json.Marshal(line)
	if err != nil {
		fmt.Fprintf(p.out, "{\"outcome\":\"log-error\",\"error\":%q}\n", err.Error())
		return
	}
	fmt.Fprintln(p.out, string(b))
}

// printTimeline prints the --verbose three-stage timeline, one JSON line
// per stage [design 7.9].
func (p *Pipeline) printTimeline(timeline []stageEvent) {
	for _, ev := range timeline {
		b, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		fmt.Fprintln(p.out, string(b))
	}
}

// Run is the package-level convenience entry point pinned by this
// packet's Contract ("Produces ... an EXPORTED internal/pipeline
// function ... that packet 8's show --refresh calls directly to re-run
// the pipeline for one entity without shelling out to the pg-desk run
// CLI"). It loads config and opens/closes pg-desk's own default store
// itself, so a caller needs nothing but ctx/entityType/entityID/change —
// exactly the shape a single-entity refresh needs. cmd/pg-desk/run.go
// does NOT use this: it needs --verbose/log-writer control this
// convenience wrapper deliberately omits, so it constructs its own
// Pipeline via New instead.
func Run(ctx context.Context, entityType, entityID string, change gather.ChangeKind) error {
	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("pipeline: load config: %w", err)
	}
	st, err := store.Open(store.DefaultPath())
	if err != nil {
		return fmt.Errorf("pipeline: open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	return New(cfg, st).Run(ctx, entityType, entityID, change)
}

func repoOf(cfg *config.Config) string {
	if cfg == nil || len(cfg.Repos) == 0 {
		return ""
	}
	return cfg.Repos[0].Remote
}

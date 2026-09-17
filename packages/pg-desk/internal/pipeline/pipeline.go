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
// exit 0), and a non-nil error on a triggering-entity fetch failure or a
// store error (pr-pool exit 1). cmd/pg-desk/main.go's RunE dispatch maps
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
// writer persist already uses, never a new store method, and never as a
// Run-level error — see the sync stage's own doc comment below for why.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
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

// syncer is the subset of *sync.Syncer's API this package depends on,
// defined locally for the same reason gatherer is: tests inject a fake
// implementation without needing a real pg-connector subprocess on $PATH.
// *sync.Syncer satisfies this interface by construction.
type syncer interface {
	Sync(ctx context.Context, repo, entityID string, change gather.ChangeKind, facts gather.Facts, interp interpret.Interpretation) error
}

// Pipeline wires gather -> interpret -> store -> sync for one entity per
// Run call. Not safe for concurrent Run calls on the same entity — mirrors
// gather.Gatherer's own "not safe for concurrent Gather calls" contract,
// since Pipeline holds exactly one Gatherer.
type Pipeline struct {
	cfg      *config.Config
	store    *store.Store
	gatherer gatherer
	syncer   syncer
	clock    interpret.Clock
	verbose  bool
	out      io.Writer
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
	p := &Pipeline{
		cfg:      cfg,
		store:    st,
		gatherer: gather.NewGatherer(cfg),
		syncer:   sync.New(cfg, st),
		clock:    interpret.SystemClock{},
		out:      os.Stderr,
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
	DurationMs int64  `json:"duration_ms"`
}

// Run executes the gather -> interpret -> store pipeline once for one
// entity. See the package doc comment for the exit-code contract.
func (p *Pipeline) Run(ctx context.Context, entityType, entityID string, change gather.ChangeKind) error {
	runStart := p.clock.Now()
	var timeline []stageEvent

	stageStart := p.clock.Now()
	facts, err := p.gatherer.Gather(ctx, entityType, entityID, change)
	timeline = append(timeline, stageEvent{Stage: "gather", DurationMs: p.clock.Now().Sub(stageStart).Milliseconds()})
	if err != nil {
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
		p.logRun(entityType, entityID, change, "error", "", err, runStart)
		return fmt.Errorf("pipeline: interpret %s %s: %w", entityType, entityID, err)
	}

	stageStart = p.clock.Now()
	interpRow, err := p.persist(entityType, entityID, facts, interp)
	timeline = append(timeline, stageEvent{Stage: "store", DurationMs: p.clock.Now().Sub(stageStart).Milliseconds()})
	if err != nil {
		p.logRun(entityType, entityID, change, "error", "", err, runStart)
		return fmt.Errorf("pipeline: store %s %s: %w", entityType, entityID, err)
	}

	// Sync stage (docket pg2-2j5ac.34, Phase 10) — entityType == "pr" only
	// (see the package doc comment), and only once the entity/interpretation
	// rows above are durably persisted, since a sync failure records itself
	// ON that same interpretation row (sync_error) rather than failing this
	// Run call. This mirrors the design's own exit-code contract: "Sync
	// failures are recorded on the interpretation row (sync_error) ...
	// never a raw pg-connector exit code" [design 7.9] — never returned as
	// a Run error, never os.Exit(9), never a raw pg-connector exit code.
	if entityType == entityTypePR {
		if syncErr := p.syncer.Sync(ctx, p.repo(), entityID, change, facts, interp); syncErr != nil {
			interpRow.SyncError = syncErr.Error()
			if upsertErr := p.store.UpsertInterpretation(interpRow); upsertErr != nil {
				// A failure to even RECORD the sync error is a genuine store
				// error (this package's own exit-1 case), unlike the sync
				// failure itself.
				p.logRun(entityType, entityID, change, "error", "", upsertErr, runStart)
				return fmt.Errorf("pipeline: record sync_error %s %s: %w", entityType, entityID, upsertErr)
			}
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

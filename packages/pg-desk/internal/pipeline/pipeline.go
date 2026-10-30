// Package pipeline implements pg-desk's interpret-only run path
// (RunInterpretOnly, used by `pg-desk run issue` and `run thread`), the
// generic per-entity hydration pipeline (RunGenericEntity, RunEntityChange)
// and pg-desk's structured JSON run records [design doc "7.9 Failure
// handling and logging"]. The legacy gather -> interpret -> store -> sync run
// for a pull request, its Reconcile and Sweep passes and the sync_error retry
// bookkeeping were removed with the sync stage (PR lane refactor, packet
// P9a-2): the daemon is the PR feed now.
//
// # Exit codes [Binding decisions]
//
// An entry point returns nil on success or on a degraded-but-completed run
// (pg-router exit 0), and a non-nil error on a store or interpret failure
// (pg-router exit 1). cmd/pg-desk/main.go's RunE dispatch maps any non-nil
// error to exit 1 and never calls os.Exit with another code, so this package
// need not (and must not) call os.Exit itself.
//
// # annotation is never touched here
//
// dispositions in the persisted interpretation row are the interpret
// stage's own rule-set verdict, verbatim: no annotation-table override
// merge, and no call to Store.UpsertAnnotation.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
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

// Pipeline wires gather, interpret and store for one entity per call. Not
// safe for concurrent calls on the same entity — mirrors
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
	clock      interpret.Clock
	verbose    bool
	out        io.Writer
	// recordOut, when non-nil, receives every run record in addition to out
	// (bead pg2-dpml1).
	recordOut io.Writer
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

// WithRunRecordWriter additionally appends every per-run record (the same
// JSON line the log writer gets) to w. `pg-desk run` points it at a bounded
// file next to the store, because pg-router discards the stderr of a
// successful command-role run (bead pg2-dpml1). A write failure is dropped.
func WithRunRecordWriter(w io.Writer) Option {
	return func(p *Pipeline) { p.recordOut = w }
}

// New constructs a Pipeline backed by a fresh gather.Gatherer — exactly
// one per Pipeline instance, matching internal/gather's own documented
// expectation ("the pipeline packet, 6, is expected to construct exactly
// one Gatherer per `pg-desk run` invocation") — plus cfg and st. The
// caller owns st's lifecycle (open and Close); Pipeline never closes it.
func New(cfg *config.Config, st *store.Store, opts ...Option) *Pipeline {
	g := gather.NewGatherer(cfg, st)
	// hydration.read_issue_deps (default false): this is the gatherer that
	// hydrates issues for run, refresh, changes, sweep and reconcile, so the
	// switch must be set before EntityGatherers snapshots it.
	g.SetReadIssueDeps(cfg.HydrationReadIssueDeps())
	p := &Pipeline{
		cfg:             cfg,
		store:           st,
		gatherer:        g,
		entityGatherers: g.EntityGatherers(),
		extractors:      NewExtractorRegistry(cfg, st, repoOf(cfg)),
		clock:           interpret.SystemClock{},
		out:             os.Stderr,
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
//
// The same line is the persistent per-run record (bead pg2-dpml1; see
// WithRunRecordWriter): Ts, Path, Repo and PR make a day of records countable.
type runLogLine struct {
	// Ts is the run's end time, RFC 3339 UTC, from the pipeline clock.
	Ts         string `json:"ts"`
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	// Repo and PR identify the pull request (PR is its number). Both are
	// omitted for a non-PR entity.
	Repo string `json:"repo,omitempty"`
	PR   int    `json:"pr,omitempty"`
	// Path names which entry point produced the record: "interpret_only"
	// (`run issue` and `run thread`, which re-interpret stored facts and do
	// not gather) or "early"
	// (a failure before the pipeline ran: args, config, store open, bead
	// resolution).
	Path     string `json:"path"`
	Change   string `json:"change"`
	Outcome  string `json:"outcome"`
	Degraded string `json:"degraded,omitempty"`
	Error    string `json:"error,omitempty"`
	// Stage and ErrorClass are set only on an "error" line whose error
	// carries a *StageError (bead pg2-gp50o): the pipeline stage that failed
	// and a coarse failure class (Class* constants), so a failure pg-router
	// records only as "exit status 1" can still be attributed.
	Stage      string `json:"stage,omitempty"`
	ErrorClass string `json:"error_class,omitempty"`
	DurationMs int64  `json:"duration_ms"`
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
// An entity with no stored facts (never gathered) is a quiet no-op: nil
// error, one "noop" run record (see the decision comment in the body).
//
// Returns a non-nil error (never a panic) when change is not one of the
// four valid kinds, or on a load/interpret/store failure — matching
// Run's own exit-code contract (see the package doc comment): nil on
// success or a degraded-but-completed run, non-nil otherwise, never
// os.Exit, never a raw pg-connector code.
func (p *Pipeline) RunInterpretOnly(ctx context.Context, entityType, entityID string, change gather.ChangeKind) error {
	runStart := p.clock.Now()

	switch change {
	case gather.ChangeAdded, gather.ChangeChanged, gather.ChangeRemoved, gather.ChangeSweep:
	default:
		err := TagStage(StageInterpretOnlyArgs, fmt.Errorf("pipeline: interpret-only %s %s: change kind %q is not one of added/changed/removed/sweep", entityType, entityID, change))
		p.logRunObs(pathInterpretOnly, entityType, entityID, change, "error", "", err, runStart)
		return err
	}

	entity, found, err := p.store.GetEntity(p.repo(), entityType, entityID)
	if err != nil {
		err = TagStage(StageLoadFacts, err)
		p.logRunObs(pathInterpretOnly, entityType, entityID, change, "error", "", err, runStart)
		return fmt.Errorf("pipeline: interpret-only get entity %s %s: %w", entityType, entityID, err)
	}
	if !found {
		// Decision (bead pg2-4i9ywp): a never-gathered entity is a quiet
		// "noop", not a load_facts error. PR facts are gathered by the
		// daemon-backed PR feed (outside this repo; `run pr` and the sync
		// stage are gone), so an issue event can legitimately arrive for a
		// newly linked PR before that feed has stored it. There is nothing to
		// re-interpret yet, and the feed's own gather interprets the PR when
		// it lands. Gathering here instead would resurrect the retired
		// gather-from-run-issue path (Gatherer.Gather is the feed's job).
		p.logRunObs(pathInterpretOnly, entityType, entityID, change, "noop", "", nil, runStart)
		return nil
	}

	var facts gather.Facts
	if err := json.Unmarshal([]byte(entity.Facts), &facts); err != nil {
		err = TagStage(StageLoadFacts, err)
		p.logRunObs(pathInterpretOnly, entityType, entityID, change, "error", "", err, runStart)
		return fmt.Errorf("pipeline: interpret-only decode stored facts %s %s: %w", entityType, entityID, err)
	}

	interp, err := interpret.Interpret(facts, p.clock, p.cfg)
	if err != nil {
		err = TagStage(StageInterpret, err)
		p.logRunObs(pathInterpretOnly, entityType, entityID, change, "error", "", err, runStart)
		return fmt.Errorf("pipeline: interpret-only interpret %s %s: %w", entityType, entityID, err)
	}

	if err := p.persist(entityType, entityID, facts, interp); err != nil {
		err = TagStage(StageStore, err)
		p.logRunObs(pathInterpretOnly, entityType, entityID, change, "error", "", err, runStart)
		return fmt.Errorf("pipeline: interpret-only store %s %s: %w", entityType, entityID, err)
	}

	outcome := "ok"
	if interp.Degraded != "" {
		outcome = "degraded"
	}
	p.logRunObs(pathInterpretOnly, entityType, entityID, change, outcome, interp.Degraded, nil, runStart)
	if p.verbose {
		p.printTimeline([]stageEvent{{Stage: "interpret", DurationMs: p.clock.Now().Sub(runStart).Milliseconds()}})
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
// tables. Never touches the annotation table — see the package doc comment.
func (p *Pipeline) persist(entityType, entityID string, facts gather.Facts, interp interpret.Interpretation) error {
	repo := p.repo()

	factsJSON, err := json.Marshal(facts)
	if err != nil {
		return fmt.Errorf("marshal facts: %w", err)
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
		return fmt.Errorf("upsert entity: %w", err)
	}

	enrichmentJSON, err := json.Marshal(interp.Enrichment)
	if err != nil {
		return fmt.Errorf("marshal enrichment: %w", err)
	}
	urgencyJSON, err := json.Marshal(interp.Urgency)
	if err != nil {
		return fmt.Errorf("marshal urgency: %w", err)
	}
	dispositionsJSON, err := json.Marshal(interp.Dispositions)
	if err != nil {
		return fmt.Errorf("marshal dispositions: %w", err)
	}
	approvalsJSON, err := json.Marshal(interp.Approvals)
	if err != nil {
		return fmt.Errorf("marshal approvals: %w", err)
	}
	matchReasonsJSON, err := json.Marshal(interp.MatchReasons)
	if err != nil {
		return fmt.Errorf("marshal match reasons: %w", err)
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
		AsOf:           interp.AsOf,
	}
	if err := p.store.UpsertInterpretation(interpRow); err != nil {
		return fmt.Errorf("upsert interpretation: %w", err)
	}
	return nil
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

// Paths a run record can name (runLogLine.Path).
const (
	pathInterpretOnly = "interpret_only"
	// PathEarly labels a record written by WriteRunRecord for a failure
	// before any pipeline ran.
	PathEarly = "early"
)

// prNumberOf returns the pull-request number an entity id names, which is the
// whole id when numeric or the digits after the last '#' in a qualified
// "<repo>#<n>" id; 0 when there is none.
func prNumberOf(entityID string) int {
	s := entityID
	if i := strings.LastIndex(s, "#"); i >= 0 {
		s = s[i+1:]
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// WriteRunRecord writes one run record (the runLogLine shape) to w. logRunObs
// uses it for every pipeline run; the cmd layer uses it, with PathEarly, for a
// failure that happens before a Pipeline exists. A nil w is a no-op.
func WriteRunRecord(w io.Writer, now time.Time, d time.Duration, repo, path, entityType, entityID string, change gather.ChangeKind, outcome, degraded string, runErr error) {
	if w == nil {
		return
	}
	line := runLogLine{
		Ts:         now.UTC().Format(time.RFC3339),
		EntityType: entityType,
		EntityID:   entityID,
		Path:       path,
		Change:     string(change),
		Outcome:    outcome,
		Degraded:   degraded,
		DurationMs: d.Milliseconds(),
	}
	if entityType == entityTypePR {
		line.Repo = repo
		line.PR = prNumberOf(entityID)
	}
	if runErr != nil {
		line.Error = runErr.Error()
		line.Stage, line.ErrorClass = StageOf(runErr)
	}
	b, err := json.Marshal(line)
	if err != nil {
		fmt.Fprintf(w, "{\"outcome\":\"log-error\",\"error\":%q}\n", err.Error())
		return
	}
	fmt.Fprintln(w, string(b))
}

// logRunObs writes the unconditional structured JSON log line for one run
// [design 7.9] to the log writer and, when configured, the persistent run
// record (WithRunRecordWriter). Never returns an error: a log-encoding
// failure (which cannot happen for this fixed, all-string/int/bool shape)
// falls back to a plain line rather than losing the run's outcome entirely.
func (p *Pipeline) logRunObs(path string, entityType, entityID string, change gather.ChangeKind, outcome, degraded string, runErr error, start time.Time) {
	now := p.clock.Now()
	for _, w := range []io.Writer{p.out, p.recordOut} {
		WriteRunRecord(w, now, now.Sub(start), p.repo(), path, entityType, entityID, change, outcome, degraded, runErr)
	}
}

// RecordRun writes a run record for an outcome the pipeline entry points do
// not themselves log, namely `run issue` for a ticket with no linked PR (a
// "noop" the invocation would otherwise leave no trace of).
func (p *Pipeline) RecordRun(entityType, entityID string, change gather.ChangeKind, outcome string, start time.Time) {
	p.logRunObs(pathInterpretOnly, entityType, entityID, change, outcome, "", nil, start)
}

// Now returns the pipeline clock's current time, so a caller can take the
// start time RecordRun measures its duration from.
func (p *Pipeline) Now() time.Time { return p.clock.Now() }

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

func repoOf(cfg *config.Config) string {
	if cfg == nil || len(cfg.Repos) == 0 {
		return ""
	}
	return cfg.Repos[0].Remote
}

package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// gatherFunc adapts a plain function to the gatherer interface (mirrors
// net/http.HandlerFunc's pattern) — this suite's fake pg-connector-free
// wire double: pipeline.go defines gatherer locally exactly so tests can
// do this instead of needing a real pg-connector subprocess on $PATH
// (see pipeline.go's own doc comment on the gatherer interface).
type gatherFunc func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error)

func (f gatherFunc) Gather(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
	return f(ctx, entityType, entityID, change)
}

// syncerFunc adapts a plain function to the syncer interface (mirrors
// gatherFunc, above) — this suite's own fake sync stage, so
// TestPipelineRun_SyncFailureSetsSyncError can inject a failure without a
// real pg-connector subprocess.
type syncerFunc func(ctx context.Context, repo, entityID string, change gather.ChangeKind, facts gather.Facts, interp interpret.Interpretation) error

func (f syncerFunc) Sync(ctx context.Context, repo, entityID string, change gather.ChangeKind, facts gather.Facts, interp interpret.Interpretation) error {
	return f(ctx, repo, entityID, change, facts, interp)
}

// testCfg is a minimal single-repo Config, matching Phase 9's "exactly
// one repository" scope.
func testCfg() *config.Config {
	return &config.Config{
		SelfLogin: "me",
		Repos:     []config.RepoConfig{{Remote: "acme/widgets"}},
	}
}

// minimalFacts builds a Facts value carrying a well-formed, minimal
// `pr show` payload — mirrors internal/interpret/interpret_test.go's own
// factsFor helper, independently (this package does not import
// interpret's test file).
func minimalFacts(t *testing.T, pr map[string]any) gather.Facts {
	t.Helper()
	raw, err := json.Marshal(pr)
	if err != nil {
		t.Fatalf("marshal pr show fixture: %v", err)
	}
	return gather.Facts{PRShow: raw, AsOf: "2026-09-16T00:00:00Z", HeadSHA: "deadbeef"}
}

func newTestPipeline(t *testing.T, g gatherer, out *bytes.Buffer, opts ...Option) *Pipeline {
	t.Helper()
	st := store.OpenForTest(t)
	p := New(testCfg(), st, opts...)
	p.gatherer = g
	if out != nil {
		p.out = out
	}
	p.clock = interpret.FixedClock(time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC))
	return p
}

// TestPipelineRun_Success is the exit-code acceptance criterion's "0 on
// success" case: a clean gather+interpret run persists an entity and
// interpretation row and returns nil.
func TestPipelineRun_Success(t *testing.T) {
	facts := minimalFacts(t, map[string]any{"author": "me", "title": "fix: x", "repo": "acme/widgets", "number": 1})
	var out bytes.Buffer
	p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		return facts, nil
	}), &out)

	if err := p.Run(context.Background(), "pr", "1", gather.ChangeAdded); err != nil {
		t.Fatalf("Run: %v", err)
	}

	e, found, err := p.store.GetEntity("acme/widgets", "pr", "1")
	if err != nil || !found {
		t.Fatalf("GetEntity: found=%v err=%v", found, err)
	}
	if e.HeadSHA != "deadbeef" {
		t.Fatalf("entity.HeadSHA = %q, want %q", e.HeadSHA, "deadbeef")
	}

	interp, found, err := p.store.GetInterpretation("acme/widgets", "pr", "1")
	if err != nil || !found {
		t.Fatalf("GetInterpretation: found=%v err=%v", found, err)
	}
	if interp.SyncError != "" {
		t.Fatalf("sync_error = %q, want empty (never written by this packet)", interp.SyncError)
	}
	if interp.Degraded {
		t.Fatalf("interpretation.Degraded = true, want false for a clean run")
	}

	if !strings.Contains(out.String(), `"outcome":"ok"`) {
		t.Fatalf("expected a structured JSON log line with outcome ok, got: %s", out.String())
	}
}

// TestPipelineRun_Degraded is the exit-code acceptance criterion's "0 on
// a degraded run" case: Facts.Degraded propagates to the interpretation
// row's Degraded flag, and Run still returns nil.
func TestPipelineRun_Degraded(t *testing.T) {
	facts := minimalFacts(t, map[string]any{"author": "me", "title": "x"})
	facts.Degraded = "ci list"
	var out bytes.Buffer
	p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		return facts, nil
	}), &out)

	if err := p.Run(context.Background(), "pr", "2", gather.ChangeSweep); err != nil {
		t.Fatalf("Run: %v", err)
	}

	interp, found, err := p.store.GetInterpretation("acme/widgets", "pr", "2")
	if err != nil || !found {
		t.Fatalf("GetInterpretation: found=%v err=%v", found, err)
	}
	if !interp.Degraded {
		t.Fatalf("interpretation.Degraded = false, want true (Facts.Degraded was %q)", facts.Degraded)
	}
	if !strings.Contains(out.String(), `"outcome":"degraded"`) || !strings.Contains(out.String(), `"degraded":"ci list"`) {
		t.Fatalf("expected a structured JSON log line naming the degraded input, got: %s", out.String())
	}
}

// TestPipelineRun_TriggeringEntityFetchFailure is the exit-code
// acceptance criterion's "1 on triggering-entity fetch failure" case:
// Run returns a non-nil error (pr-pool exit 1 via main.go), and writes no
// rows.
func TestPipelineRun_TriggeringEntityFetchFailure(t *testing.T) {
	var out bytes.Buffer
	wantErr := errors.New("gather: fetch triggering PR 3: pg-connector reports not_found")
	p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		return gather.Facts{}, wantErr
	}), &out)

	err := p.Run(context.Background(), "pr", "3", gather.ChangeAdded)
	if err == nil {
		t.Fatal("Run: expected a non-nil error for a triggering-entity fetch failure")
	}
	if !strings.Contains(err.Error(), "not_found") {
		t.Fatalf("Run error = %v, want it to wrap the gather failure", err)
	}

	if _, found, _ := p.store.GetEntity("acme/widgets", "pr", "3"); found {
		t.Fatal("expected no entity row to be written on a fetch failure")
	}
	if !strings.Contains(out.String(), `"outcome":"error"`) {
		t.Fatalf("expected a structured JSON error log line, got: %s", out.String())
	}
}

// TestPipelineRun_StoreError is the exit-code acceptance criterion's "1
// on a store error" case: a real store error (the connection is closed
// out from under a successful gather+interpret) surfaces as a non-nil
// Run error, never silently swallowed.
func TestPipelineRun_StoreError(t *testing.T) {
	facts := minimalFacts(t, map[string]any{"author": "me", "title": "x"})
	var out bytes.Buffer
	p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		return facts, nil
	}), &out)

	if err := p.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	err := p.Run(context.Background(), "pr", "4", gather.ChangeAdded)
	if err == nil {
		t.Fatal("Run: expected a non-nil error for a store error")
	}
	if !strings.Contains(out.String(), `"outcome":"error"`) {
		t.Fatalf("expected a structured JSON error log line, got: %s", out.String())
	}
}

// TestPipelineRun_AnnotationByteIdentical is the acceptance criterion "a
// full pipeline run leaves annotation byte-identical": a pre-existing
// PR-level annotation row survives an unrelated successful Run
// unchanged.
func TestPipelineRun_AnnotationByteIdentical(t *testing.T) {
	facts := minimalFacts(t, map[string]any{"author": "me", "title": "x"})
	var out bytes.Buffer
	p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		return facts, nil
	}), &out)

	hidden := true
	before := store.Annotation{
		Repo: "acme/widgets", EntityType: "pr", EntityID: "5",
		Hidden: &hidden, HiddenReason: "flaky", SetBy: "operator", SetAt: "2026-09-01T00:00:00Z",
	}
	if err := p.store.UpsertAnnotation(before); err != nil {
		t.Fatalf("seed annotation: %v", err)
	}

	if err := p.Run(context.Background(), "pr", "5", gather.ChangeAdded); err != nil {
		t.Fatalf("Run: %v", err)
	}

	after, found, err := p.store.GetPRAnnotation("acme/widgets", "pr", "5")
	if err != nil || !found {
		t.Fatalf("GetPRAnnotation: found=%v err=%v", found, err)
	}
	if after.HiddenReason != before.HiddenReason || after.SetBy != before.SetBy || after.SetAt != before.SetAt {
		t.Fatalf("annotation changed after a pipeline run: before=%+v after=%+v", before, after)
	}
	if after.Hidden == nil || *after.Hidden != *before.Hidden {
		t.Fatalf("annotation.Hidden changed after a pipeline run: before=%v after=%v", before.Hidden, after.Hidden)
	}
}

// TestPipelineRun_VerboseTimeline is the acceptance criterion "--verbose
// timeline works": a verbose run prints all three stages, and a
// non-verbose run prints none of them (only the base summary line).
func TestPipelineRun_VerboseTimeline(t *testing.T) {
	facts := minimalFacts(t, map[string]any{"author": "me", "title": "x"})
	gf := gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		return facts, nil
	})

	var verboseOut bytes.Buffer
	pVerbose := newTestPipeline(t, gf, &verboseOut, WithVerbose(true))
	if err := pVerbose.Run(context.Background(), "pr", "6", gather.ChangeAdded); err != nil {
		t.Fatalf("Run (verbose): %v", err)
	}
	for _, stage := range []string{"gather", "interpret", "store"} {
		if !strings.Contains(verboseOut.String(), `"stage":"`+stage+`"`) {
			t.Fatalf("verbose output missing stage %q; got: %s", stage, verboseOut.String())
		}
	}

	var quietOut bytes.Buffer
	pQuiet := newTestPipeline(t, gf, &quietOut)
	if err := pQuiet.Run(context.Background(), "pr", "7", gather.ChangeAdded); err != nil {
		t.Fatalf("Run (quiet): %v", err)
	}
	if strings.Contains(quietOut.String(), `"stage":`) {
		t.Fatalf("non-verbose run printed a stage timeline; got: %s", quietOut.String())
	}
}

// TestPipelineRun_RemovedUnknownIDIsNoop is the Binding decisions
// acceptance criterion "an id the store does not know is a no-op (exits
// 0, no rows written)" for --change removed.
func TestPipelineRun_RemovedUnknownIDIsNoop(t *testing.T) {
	var out bytes.Buffer
	p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		return gather.Facts{RemovedState: "not_found"}, nil
	}), &out)

	if err := p.Run(context.Background(), "pr", "8", gather.ChangeRemoved); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, found, _ := p.store.GetEntity("acme/widgets", "pr", "8"); found {
		t.Fatal("expected no entity row to be written for an id the store never knew")
	}
	if _, found, _ := p.store.GetInterpretation("acme/widgets", "pr", "8"); found {
		t.Fatal("expected no interpretation row to be written for an id the store never knew")
	}
	if !strings.Contains(out.String(), `"outcome":"noop"`) {
		t.Fatalf("expected a structured JSON noop log line, got: %s", out.String())
	}
}

// TestPipelineRun_SyncFailureSetsSyncError is docket pg2-2j5ac.34's own
// acceptance criterion: a sync stage failure sets Interpretation.SyncError
// on that entity's row (via the existing UpsertInterpretation) rather than
// failing the whole Run invocation — never a Run error, never exit 9,
// never a raw pg-connector exit code.
func TestPipelineRun_SyncFailureSetsSyncError(t *testing.T) {
	facts := minimalFacts(t, map[string]any{"author": "me", "title": "x", "repo": "acme/widgets", "number": 10})
	var out bytes.Buffer
	p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		return facts, nil
	}), &out)
	wantSyncErr := errors.New("sync: pg-connector issue create: exit 1: boom")
	p.syncer = syncerFunc(func(ctx context.Context, repo, entityID string, change gather.ChangeKind, facts gather.Facts, interp interpret.Interpretation) error {
		return wantSyncErr
	})

	if err := p.Run(context.Background(), "pr", "10", gather.ChangeAdded); err != nil {
		t.Fatalf("Run: expected nil (a sync failure must not fail Run), got %v", err)
	}

	interp, found, err := p.store.GetInterpretation("acme/widgets", "pr", "10")
	if err != nil || !found {
		t.Fatalf("GetInterpretation: found=%v err=%v", found, err)
	}
	if interp.SyncError != wantSyncErr.Error() {
		t.Fatalf("interpretation.SyncError = %q, want %q", interp.SyncError, wantSyncErr.Error())
	}
	// The rest of the persisted row must survive the second UpsertInterpretation
	// call untouched (same row, only SyncError changed).
	if interp.Ownership == "" {
		t.Fatalf("interpretation row lost its other fields on the sync_error update: %+v", interp)
	}
	if strings.Contains(out.String(), `"outcome":"error"`) {
		t.Fatalf("a sync failure must not log a run-level error outcome, got: %s", out.String())
	}
}

// TestPipelineRun_SyncSkippedForNonPREntityType proves the sync stage
// never runs for a non-"pr" entityType (this packet's own Contract: "sync
// stage ... when entityType == pr").
func TestPipelineRun_SyncSkippedForNonPREntityType(t *testing.T) {
	facts := minimalFacts(t, map[string]any{"author": "me", "title": "x"})
	var out bytes.Buffer
	p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		return facts, nil
	}), &out)
	called := false
	p.syncer = syncerFunc(func(ctx context.Context, repo, entityID string, change gather.ChangeKind, facts gather.Facts, interp interpret.Interpretation) error {
		called = true
		return nil
	})

	if err := p.Run(context.Background(), "issue", "11", gather.ChangeAdded); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if called {
		t.Fatal("sync stage was invoked for a non-pr entityType")
	}
}

// TestPipelineRun_RemovedKnownIDRecordsClosure covers the Binding
// decisions' "merged/closed is a confirmed closure ... this packet's
// closure handling is limited to recording the closed state on
// entity/interpretation and exiting 0": a known id's removed re-read is
// persisted (RemovedState lands in entity.facts) rather than being
// treated as a no-op.
func TestPipelineRun_RemovedKnownIDRecordsClosure(t *testing.T) {
	var out bytes.Buffer
	p := newTestPipeline(t, gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		facts := minimalFacts(t, map[string]any{"author": "me", "title": "x", "state": "closed"})
		facts.RemovedState = "closed"
		return facts, nil
	}), &out)

	// Seed the store so this id is already known (an earlier run's row).
	if err := p.store.UpsertEntity(store.Entity{
		Repo: "acme/widgets", EntityType: "pr", EntityID: "9",
		Facts: `{}`, AsOf: "2026-09-01T00:00:00Z", ContentHash: "seed",
	}); err != nil {
		t.Fatalf("seed entity: %v", err)
	}

	if err := p.Run(context.Background(), "pr", "9", gather.ChangeRemoved); err != nil {
		t.Fatalf("Run: %v", err)
	}

	e, found, err := p.store.GetEntity("acme/widgets", "pr", "9")
	if err != nil || !found {
		t.Fatalf("GetEntity: found=%v err=%v", found, err)
	}
	if !strings.Contains(e.Facts, `"removed_state"`) {
		t.Fatalf("entity.facts does not record removed_state: %s", e.Facts)
	}
	if !strings.Contains(out.String(), `"outcome":"ok"`) {
		t.Fatalf("expected a structured JSON ok log line for a recorded closure, got: %s", out.String())
	}
}

// --- RunInterpretOnly: docket pg2-2j5ac.34.2's own interpret-only re-run
// path (cmd/pg-desk/run.go's "issue" case, Phase 10, beads backend) -------

// countingGatherer wraps a gatherer and counts Gather calls, so a test can
// assert RunInterpretOnly never invokes gather again — this packet's own
// acceptance criterion ("proven by a wire-double gather-invocation-count
// assertion staying at zero for this path").
type countingGatherer struct {
	inner gatherer
	calls int
}

func (g *countingGatherer) Gather(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
	g.calls++
	return g.inner.Gather(ctx, entityType, entityID, change)
}

// seedInterpretOnlyEntity writes the entity row RunInterpretOnly re-reads
// (Store.GetEntity) without ever gathering.
func seedInterpretOnlyEntity(t *testing.T, p *Pipeline, entityID string, facts gather.Facts) {
	t.Helper()
	factsJSON, err := json.Marshal(facts)
	if err != nil {
		t.Fatalf("marshal facts fixture: %v", err)
	}
	if err := p.store.UpsertEntity(store.Entity{
		Repo: "acme/widgets", EntityType: "pr", EntityID: entityID,
		Facts: string(factsJSON), AsOf: facts.AsOf, HeadSHA: facts.HeadSHA, ContentHash: "fixture",
	}); err != nil {
		t.Fatalf("seed entity: %v", err)
	}
}

// TestPipelineRunInterpretOnly_Success proves the core contract: a fresh
// interpretation is persisted from the already-stored facts, gather is
// never invoked, the sync stage is never invoked, and a prior sync_error
// on the row survives untouched (this path never runs sync, so it must
// not silently erase what an earlier sync run recorded).
func TestPipelineRunInterpretOnly_Success(t *testing.T) {
	var out bytes.Buffer
	cg := &countingGatherer{inner: gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		t.Fatal("gather was invoked by RunInterpretOnly")
		return gather.Facts{}, nil
	})}
	p := newTestPipeline(t, cg, &out)
	syncCalled := false
	p.syncer = syncerFunc(func(ctx context.Context, repo, entityID string, change gather.ChangeKind, facts gather.Facts, interp interpret.Interpretation) error {
		syncCalled = true
		return nil
	})

	facts := minimalFacts(t, map[string]any{"author": "me", "title": "fix: x", "repo": "acme/widgets", "number": 20})
	seedInterpretOnlyEntity(t, p, "20", facts)
	// Seed a prior sync_error on the row (as an earlier "pr"-triggered Run
	// call's sync stage would have) — RunInterpretOnly must preserve it.
	if err := p.store.UpsertInterpretation(store.Interpretation{
		Repo: "acme/widgets", EntityType: "pr", EntityID: "20",
		SyncError: "sync: pg-connector issue create: exit 1: boom", AsOf: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed prior interpretation: %v", err)
	}

	if err := p.RunInterpretOnly(context.Background(), "pr", "20", gather.ChangeChanged); err != nil {
		t.Fatalf("RunInterpretOnly: %v", err)
	}

	if cg.calls != 0 {
		t.Fatalf("gather was invoked %d times by RunInterpretOnly, want 0", cg.calls)
	}
	if syncCalled {
		t.Fatal("sync stage was invoked by RunInterpretOnly")
	}

	interp, found, err := p.store.GetInterpretation("acme/widgets", "pr", "20")
	if err != nil || !found {
		t.Fatalf("GetInterpretation: found=%v err=%v", found, err)
	}
	if interp.Ownership == "" {
		t.Fatalf("interpretation row looks unpopulated: %+v", interp)
	}
	if interp.SyncError != "sync: pg-connector issue create: exit 1: boom" {
		t.Fatalf("interpretation.SyncError = %q, want the prior value preserved", interp.SyncError)
	}
	if !strings.Contains(out.String(), `"outcome":"ok"`) || !strings.Contains(out.String(), `"change":"changed"`) {
		t.Fatalf("expected a structured JSON ok log line naming the change kind, got: %s", out.String())
	}
}

// TestPipelineRunInterpretOnly_NeverGathered proves an entity with no
// stored facts (never gathered) is a clear, non-panicking error rather
// than a nil-Facts crash.
func TestPipelineRunInterpretOnly_NeverGathered(t *testing.T) {
	var out bytes.Buffer
	cg := &countingGatherer{inner: gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		return gather.Facts{}, nil
	})}
	p := newTestPipeline(t, cg, &out)

	err := p.RunInterpretOnly(context.Background(), "pr", "no-such-entity", gather.ChangeChanged)
	if err == nil {
		t.Fatal("RunInterpretOnly: expected an error for an entity with no stored facts")
	}
	if !strings.Contains(err.Error(), "never gathered") {
		t.Fatalf("RunInterpretOnly error = %v, want it to name the missing-facts case", err)
	}
	if cg.calls != 0 {
		t.Fatalf("gather was invoked %d times by RunInterpretOnly, want 0", cg.calls)
	}
}

// TestPipelineRunInterpretOnly_InvalidChangeKind proves a malformed
// --change value is rejected clearly, never a panic, and never reaches
// the store.
func TestPipelineRunInterpretOnly_InvalidChangeKind(t *testing.T) {
	var out bytes.Buffer
	cg := &countingGatherer{inner: gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		return gather.Facts{}, nil
	})}
	p := newTestPipeline(t, cg, &out)

	err := p.RunInterpretOnly(context.Background(), "pr", "21", gather.ChangeKind("bogus"))
	if err == nil {
		t.Fatal("RunInterpretOnly: expected an error for an invalid change kind")
	}
	if !strings.Contains(err.Error(), "not one of added/changed/removed/sweep") {
		t.Fatalf("RunInterpretOnly error = %v, want it to name the valid change kinds", err)
	}
	if cg.calls != 0 {
		t.Fatalf("gather was invoked %d times by RunInterpretOnly, want 0", cg.calls)
	}
}

// TestPipelineRunInterpretOnly_Degraded proves Facts.Degraded (carried
// over from the original gather run's stored facts) still propagates to
// the re-interpreted row's Degraded flag and the "degraded" log outcome.
func TestPipelineRunInterpretOnly_Degraded(t *testing.T) {
	var out bytes.Buffer
	cg := &countingGatherer{inner: gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		return gather.Facts{}, nil
	})}
	p := newTestPipeline(t, cg, &out)

	facts := minimalFacts(t, map[string]any{"author": "me", "title": "x"})
	facts.Degraded = "ci list"
	seedInterpretOnlyEntity(t, p, "22", facts)

	if err := p.RunInterpretOnly(context.Background(), "pr", "22", gather.ChangeSweep); err != nil {
		t.Fatalf("RunInterpretOnly: %v", err)
	}

	interp, found, err := p.store.GetInterpretation("acme/widgets", "pr", "22")
	if err != nil || !found {
		t.Fatalf("GetInterpretation: found=%v err=%v", found, err)
	}
	if !interp.Degraded {
		t.Fatalf("interpretation.Degraded = false, want true (Facts.Degraded was %q)", facts.Degraded)
	}
	if !strings.Contains(out.String(), `"outcome":"degraded"`) {
		t.Fatalf("expected a structured JSON degraded log line, got: %s", out.String())
	}
	if cg.calls != 0 {
		t.Fatalf("gather was invoked %d times by RunInterpretOnly, want 0", cg.calls)
	}
}

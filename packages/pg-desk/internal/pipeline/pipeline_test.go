package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
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
// never invoked and a stored interpretation row is written.
func TestPipelineRunInterpretOnly_Success(t *testing.T) {
	var out bytes.Buffer
	cg := &countingGatherer{inner: gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		t.Fatal("gather was invoked by RunInterpretOnly")
		return gather.Facts{}, nil
	})}
	p := newTestPipeline(t, cg, &out)

	facts := minimalFacts(t, map[string]any{"author": "me", "title": "fix: x", "repo": "acme/widgets", "number": 20})
	seedInterpretOnlyEntity(t, p, "20", facts)
	if err := p.RunInterpretOnly(context.Background(), "pr", "20", gather.ChangeChanged); err != nil {
		t.Fatalf("RunInterpretOnly: %v", err)
	}

	if cg.calls != 0 {
		t.Fatalf("gather was invoked %d times by RunInterpretOnly, want 0", cg.calls)
	}

	interp, found, err := p.store.GetInterpretation("acme/widgets", "pr", "20")
	if err != nil || !found {
		t.Fatalf("GetInterpretation: found=%v err=%v", found, err)
	}
	if interp.Ownership == "" {
		t.Fatalf("interpretation row looks unpopulated: %+v", interp)
	}
	if !strings.Contains(out.String(), `"outcome":"ok"`) || !strings.Contains(out.String(), `"change":"changed"`) {
		t.Fatalf("expected a structured JSON ok log line naming the change kind, got: %s", out.String())
	}
}

// TestPipelineRunInterpretOnly_NeverGathered proves an entity with no
// stored facts (never gathered, e.g. a newly linked PR the daemon feed has
// not stored yet) is a quiet "noop" against the real store: nil error, no
// load_facts error line, no gather, no row written (bead pg2-4i9ywp).
func TestPipelineRunInterpretOnly_NeverGathered(t *testing.T) {
	var out bytes.Buffer
	cg := &countingGatherer{inner: gatherFunc(func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) (gather.Facts, error) {
		return gather.Facts{}, nil
	})}
	p := newTestPipeline(t, cg, &out)

	err := p.RunInterpretOnly(context.Background(), "pr", "no-such-entity", gather.ChangeAdded)
	if err != nil {
		t.Fatalf("RunInterpretOnly: never-gathered entity must be a quiet no-op, got %v", err)
	}
	if strings.Contains(out.String(), `"outcome":"error"`) || strings.Contains(out.String(), "load_facts") {
		t.Fatalf("no error/load_facts line expected, got: %s", out.String())
	}
	if !strings.Contains(out.String(), `"outcome":"noop"`) || !strings.Contains(out.String(), `"path":"interpret_only"`) {
		t.Fatalf("expected one interpret_only noop record, got: %s", out.String())
	}
	if _, found, _ := p.store.GetEntity(p.repo(), "pr", "no-such-entity"); found {
		t.Fatal("a no-op must not write an entity row")
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

// --- Sweep: `pg-desk sweep` (bead pg2-gznpe) -------------------------------

// --- Reconcile: pg2-kftf9.2 — re-drive PRs that left the open set ----------

// --- Reconcile budget: pg2-a5z69 — bounded, convergent runs -----------------

// --- Reconcile and transient failures: pg2-hpakl ----------------------------

// killedErr is the exact failure text the 2026-10-07 router health review
// found behind desk-reconcile's exit 1.
const killedErrText = "gather: removed re-read pr show: pg-connector [pr show acme/widgets#9 --fresh]: exit 1: unavailable: scriptout: pg-connector-pr-github: signal: killed"

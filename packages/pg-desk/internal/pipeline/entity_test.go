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

// fakeEntityGatherer returns a canned result/error.
type fakeEntityGatherer struct {
	result gather.GatherResult
	err    error
}

func (f fakeEntityGatherer) GatherEntity(context.Context, string, gather.ChangeKind) (gather.GatherResult, error) {
	return f.result, f.err
}

func newGenericPipeline(t *testing.T, out *bytes.Buffer, gs map[string]gather.EntityGatherer) *Pipeline {
	t.Helper()
	p := newTestPipeline(t, nil, out)
	// Hydration rebuilds derived links, which needs a new-schema store.
	p.store = store.OpenNewSchemaForTest(t)
	p.extractors = NewExtractorRegistry(p.cfg, p.store, p.repo())
	p.entityGatherers = gs
	return p
}

func issueResult(t *testing.T) gather.GatherResult {
	t.Helper()
	payload, err := json.Marshal(gather.IssueFacts{IssueShow: json.RawMessage(`{"id":"i-1","owner":"me","issue_type":"task"}`)})
	if err != nil {
		t.Fatal(err)
	}
	return gather.GatherResult{Payload: payload, AsOf: "2026-09-16T00:00:00Z"}
}

func TestRunGenericEntity_UnregisteredType(t *testing.T) {
	p := newGenericPipeline(t, nil, map[string]gather.EntityGatherer{})
	err := p.RunGenericEntity(context.Background(), "widget", "w1", gather.ChangeAdded)
	if err == nil || !strings.Contains(err.Error(), "no gather adapter registered") {
		t.Fatalf("err = %v, want no gather adapter registered", err)
	}
}

func TestRunGenericEntity_GatherWithoutInterpreter(t *testing.T) {
	p := newGenericPipeline(t, nil, map[string]gather.EntityGatherer{
		"widget": fakeEntityGatherer{result: gather.GatherResult{Payload: json.RawMessage(`{}`)}},
	})
	err := p.RunGenericEntity(context.Background(), "widget", "w1", gather.ChangeAdded)
	if err == nil || !strings.Contains(err.Error(), "no interpret adapter registered") {
		t.Fatalf("err = %v, want no interpret adapter registered", err)
	}
}

func TestRunGenericEntity_IssueSuccessWritesRows(t *testing.T) {
	p := newGenericPipeline(t, nil, map[string]gather.EntityGatherer{
		"issue": fakeEntityGatherer{result: issueResult(t)},
	})
	if err := p.RunGenericEntity(context.Background(), "issue", "i-1", gather.ChangeAdded); err != nil {
		t.Fatalf("RunGenericEntity: %v", err)
	}
	e, found, err := p.store.GetEntity("acme/widgets", "issue", "i-1")
	if err != nil || !found {
		t.Fatalf("GetEntity found=%v err=%v", found, err)
	}
	if e.AsOf != "2026-09-16T00:00:00Z" || e.ContentHash == "" {
		t.Fatalf("entity = %+v", e)
	}
	if _, found, err := p.store.GetInterpretation("acme/widgets", "issue", "i-1"); err != nil || !found {
		t.Fatalf("GetInterpretation found=%v err=%v", found, err)
	}
}

func TestRunGenericEntity_NotFoundGate(t *testing.T) {
	p := newGenericPipeline(t, nil, map[string]gather.EntityGatherer{
		"issue": fakeEntityGatherer{result: gather.GatherResult{RemovedState: "not_found"}},
	})
	err := p.RunGenericEntity(context.Background(), "issue", "i-9", gather.ChangeChanged)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want not found", err)
	}
	if _, found, _ := p.store.GetEntity("acme/widgets", "issue", "i-9"); found {
		t.Fatal("not-found run must not write an entity row")
	}

	// Explicit removal is exempt and persists gracefully.
	if err := p.RunGenericEntity(context.Background(), "issue", "i-9", gather.ChangeRemoved); err != nil {
		t.Fatalf("removal: %v", err)
	}
	if _, found, _ := p.store.GetEntity("acme/widgets", "issue", "i-9"); !found {
		t.Fatal("removal path should persist an entity row")
	}
}

func TestFailedDetailReadKeepsPreviousSnapshot(t *testing.T) {
	var out bytes.Buffer
	p := newGenericPipeline(t, &out, map[string]gather.EntityGatherer{
		"issue": fakeEntityGatherer{result: issueResult(t)},
	})
	if err := p.RunGenericEntity(context.Background(), "issue", "i-1", gather.ChangeAdded); err != nil {
		t.Fatal(err)
	}
	e1, _, _ := p.store.GetEntity("acme/widgets", "issue", "i-1")
	i1, _, _ := p.store.GetInterpretation("acme/widgets", "issue", "i-1")
	out.Reset()

	p.entityGatherers["issue"] = fakeEntityGatherer{err: errors.New("boom")}
	err := p.RunGenericEntity(context.Background(), "issue", "i-1", gather.ChangeChanged)
	if err == nil || !strings.Contains(err.Error(), "pipeline: gather issue i-1") {
		t.Fatalf("err = %v", err)
	}
	e2, _, _ := p.store.GetEntity("acme/widgets", "issue", "i-1")
	i2, _, _ := p.store.GetInterpretation("acme/widgets", "issue", "i-1")
	if e1 != e2 {
		t.Fatalf("entity changed:\n%+v\n%+v", e1, e2)
	}
	if i1 != i2 {
		t.Fatalf("interpretation changed:\n%+v\n%+v", i1, i2)
	}
	if out.Len() != 0 {
		t.Fatalf("pipeline log written: %q", out.String())
	}
}

// TestRunGenericEntity_PRHeadSHAMatchesPRPath pins the HeadSHA reconciliation:
// a pr row written through RunGenericEntity carries the same head_sha the PR
// path stores.
func TestRunGenericEntity_PRHeadSHAMatchesPRPath(t *testing.T) {
	facts := minimalFacts(t, map[string]any{"author": "me", "title": "fix: x", "repo": "acme/widgets", "number": 1})
	payload, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	p := newGenericPipeline(t, nil, map[string]gather.EntityGatherer{
		"pr": fakeEntityGatherer{result: gather.GatherResult{Payload: payload, AsOf: facts.AsOf}},
	})
	p.clock = interpret.FixedClock(time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC))
	if err := p.RunGenericEntity(context.Background(), "pr", "1", gather.ChangeAdded); err != nil {
		t.Fatalf("RunGenericEntity: %v", err)
	}
	generic, found, err := p.store.GetEntity("acme/widgets", "pr", "1")
	if err != nil || !found {
		t.Fatalf("GetEntity found=%v err=%v", found, err)
	}
	if generic.HeadSHA != "deadbeef" {
		t.Fatalf("HeadSHA = %q, want deadbeef", generic.HeadSHA)
	}

	// Same facts through the PR path on a fresh store.
	st := store.OpenForTest(t)
	q := New(&config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "acme/widgets"}}}, st)
	q.clock = p.clock
	interp, err := interpret.Interpret(facts, q.clock, q.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.persist("pr", "1", facts, interp); err != nil {
		t.Fatal(err)
	}
	viaPR, _, _ := st.GetEntity("acme/widgets", "pr", "1")
	if viaPR.HeadSHA != generic.HeadSHA || viaPR.ContentHash != generic.ContentHash {
		t.Fatalf("PR path %+v vs generic %+v", viaPR, generic)
	}
}

package pipeline

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func interpretOnlyPipeline(t *testing.T, out *bytes.Buffer) *Pipeline {
	t.Helper()
	return newTestPipeline(t, gatherFunc(func(context.Context, string, string, gather.ChangeKind) (gather.Facts, error) {
		t.Fatal("RunInterpretOnly must never gather")
		return gather.Facts{}, nil
	}), out)
}

// A store-write failure (the interpretOnlyWrite test seam) is tagged as the
// store stage, logged as an error and leaves the previous row and the entity
// untouched.
func TestRunInterpretOnly_StoreWriteFailureIsTaggedLoggedAndWritesNothing(t *testing.T) {
	var out bytes.Buffer
	p := interpretOnlyPipeline(t, &out)
	facts := minimalFacts(t, map[string]any{"author": "me", "title": "fix: x"})
	seedInterpretOnlyEntity(t, p, "30", facts)
	entityBefore, _, _ := p.store.GetEntity("acme/widgets", "pr", "30")

	boom := errors.New("injected store failure")
	p.seams.interpretOnlyWrite = func() error { return boom }
	err := p.RunInterpretOnly(context.Background(), "pr", "30", gather.ChangeChanged)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "reinterpret store pr 30") {
		t.Fatalf("err = %v, want the wrapped store failure", err)
	}
	if stage, _ := StageOf(err); stage != StageStore {
		t.Fatalf("stage = %q, want %q", stage, StageStore)
	}
	if !strings.Contains(out.String(), `"outcome":"error"`) || !strings.Contains(out.String(), `"path":"interpret_only"`) {
		t.Fatalf("log = %s, want an interpret_only error record", out.String())
	}
	if _, found, _ := p.store.GetInterpretation("acme/widgets", "pr", "30"); found {
		t.Fatal("a failed store write left an interpretation row")
	}
	if entityAfter, _, _ := p.store.GetEntity("acme/widgets", "pr", "30"); entityAfter != entityBefore {
		t.Fatalf("entity changed: before=%+v after=%+v", entityBefore, entityAfter)
	}
	// With the seam cleared the very same call succeeds: the seam alone caused the failure.
	p.seams.interpretOnlyWrite = nil
	if err := p.RunInterpretOnly(context.Background(), "pr", "30", gather.ChangeChanged); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

// A REAL store failure in the write (the interpretation table refuses
// inserts) takes the same path without any seam.
func TestRunInterpretOnly_RealStoreWriteFailureIsReturned(t *testing.T) {
	var out bytes.Buffer
	p := interpretOnlyPipeline(t, &out)
	seedInterpretOnlyEntity(t, p, "31", minimalFacts(t, map[string]any{"author": "me", "title": "x"}))
	failStoreWrites(t, p.store, "INSERT", "interpretation")

	err := p.RunInterpretOnly(context.Background(), "pr", "31", gather.ChangeChanged)
	if err == nil || !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("err = %v, want the store's injected failure", err)
	}
	if stage, _ := StageOf(err); stage != StageStore {
		t.Fatalf("stage = %q, want %q", stage, StageStore)
	}
}

func TestRunInterpretOnly_LoadFactsFailures(t *testing.T) {
	t.Run("store read failure", func(t *testing.T) {
		var out bytes.Buffer
		p := interpretOnlyPipeline(t, &out)
		if err := p.store.Close(); err != nil {
			t.Fatal(err)
		}
		err := p.RunInterpretOnly(context.Background(), "pr", "32", gather.ChangeChanged)
		if err == nil || !strings.Contains(err.Error(), "reinterpret get entity pr 32") {
			t.Fatalf("err = %v", err)
		}
		if stage, _ := StageOf(err); stage != StageLoadFacts {
			t.Fatalf("stage = %q, want %q", stage, StageLoadFacts)
		}
		if !strings.Contains(out.String(), `"outcome":"error"`) {
			t.Fatalf("log = %s", out.String())
		}
	})
	t.Run("undecodable stored facts", func(t *testing.T) {
		var out bytes.Buffer
		p := interpretOnlyPipeline(t, &out)
		if err := p.store.UpsertEntity(store.Entity{
			Repo: "acme/widgets", EntityType: "pr", EntityID: "33",
			Facts: "{not json", AsOf: "2026-09-16T00:00:00Z", ContentHash: "h",
		}); err != nil {
			t.Fatal(err)
		}
		err := p.RunInterpretOnly(context.Background(), "pr", "33", gather.ChangeChanged)
		if err == nil || !strings.Contains(err.Error(), "reinterpret decode stored facts pr 33") {
			t.Fatalf("err = %v", err)
		}
		if stage, _ := StageOf(err); stage != StageLoadFacts {
			t.Fatalf("stage = %q, want %q", stage, StageLoadFacts)
		}
		if _, found, _ := p.store.GetInterpretation("acme/widgets", "pr", "33"); found {
			t.Fatal("an interpretation was written from undecodable facts")
		}
	})
}

// Facts whose pr show payload cannot be decoded fail the interpret stage and
// write nothing.
func TestRunInterpretOnly_InterpretFailureIsTaggedAndWritesNothing(t *testing.T) {
	var out bytes.Buffer
	p := interpretOnlyPipeline(t, &out)
	seedInterpretOnlyEntity(t, p, "34", gather.Facts{PRShow: []byte(`"not an object"`), AsOf: "2026-09-16T00:00:00Z"})

	err := p.RunInterpretOnly(context.Background(), "pr", "34", gather.ChangeChanged)
	if err == nil || !strings.Contains(err.Error(), "reinterpret interpret pr 34") {
		t.Fatalf("err = %v", err)
	}
	if stage, _ := StageOf(err); stage != StageInterpret {
		t.Fatalf("stage = %q, want %q", stage, StageInterpret)
	}
	if _, found, _ := p.store.GetInterpretation("acme/widgets", "pr", "34"); found {
		t.Fatal("an interpretation was written after an interpret failure")
	}
}

// Verbose mode prints the one-stage timeline after a successful run.
func TestRunInterpretOnly_VerbosePrintsTheTimeline(t *testing.T) {
	var out bytes.Buffer
	p := interpretOnlyPipeline(t, &out)
	p.verbose = true
	seedInterpretOnlyEntity(t, p, "35", minimalFacts(t, map[string]any{"author": "me", "title": "x"}))
	if err := p.RunInterpretOnly(context.Background(), "pr", "35", gather.ChangeAdded); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "interpret") {
		t.Fatalf("out = %s, want a timeline naming the interpret stage", out.String())
	}
}

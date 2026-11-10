package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const riRepo = "acme/widgets"

func riPipeline(t *testing.T) *Pipeline {
	t.Helper()
	return interpretOnlyPipeline(t, &bytes.Buffer{})
}

func riReinterpret(t *testing.T, p *Pipeline, entityType, id string, opts ReinterpretOptions) (ReinterpretResult, error) {
	t.Helper()
	return ReinterpretStored(context.Background(), p.store, p.cfg, p.clock, entityType, id, opts)
}

// seedStale seeds the entity and an interpretation row with an OLD decision
// (waiting on "someone-else", a different panel) under another stamp.
func seedStale(t *testing.T, p *Pipeline, id string) {
	t.Helper()
	seedInterpretOnlyEntity(t, p, id, minimalFacts(t, map[string]any{"author": "me", "title": "fix: x"}))
	p.store.SeedInterpretationForTest(t, store.Interpretation{
		Repo: riRepo, EntityType: "pr", EntityID: id,
		Enrichment: "{}", Urgency: "{}", Dispositions: "[]", MatchReasons: "[]",
		Approvals: `{"waiting_on":"someone-else"}`, Panel: "old-panel", AsOf: "2020-01-01T00:00:00Z",
	})
}

func riHistory(t *testing.T, st *store.Store, entityType, id string) []store.ChangeRecord {
	t.Helper()
	recs, err := st.ListEntityHistory(riRepo, entityType, id, 50)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

// INV-REINTERPRET-1/5: a re-interpretation of a PR whose decision pair changed
// writes the row with the handle's stamp, appends exactly one
// interpretation_changed record at the entity's version, and leaves the entity
// row (facts, as-of, hash, version) byte-identical.
func TestReinterpretStored_ChangedDecisionIsWrittenStampedAndRecordedWithoutTouchingTheEntity(t *testing.T) {
	p := riPipeline(t)
	seedStale(t, p, "40")
	before, _, _ := p.store.GetEntity(riRepo, "pr", "40")

	res, err := riReinterpret(t, p, "pr", "40", ReinterpretOptions{Origin: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Found || !res.Write.Written || !res.Write.DecisionChanged || !res.Write.Recorded {
		t.Fatalf("result = %+v, want found, written, changed, recorded", res)
	}
	row, _, _ := p.store.GetInterpretation(riRepo, "pr", "40")
	if row.InterpVersion != "test/stamp" || row.Panel == "old-panel" {
		t.Fatalf("row = %+v, want the new content under the handle stamp", row)
	}
	recs := riHistory(t, p.store, "pr", "40")
	if len(recs) != 1 || recs[0].Kinds[0] != store.ChangeKindInterpretationChanged || recs[0].Origin != "test" || recs[0].Version != before.Version {
		t.Fatalf("history = %+v, want one interpretation_changed at version %d from origin test", recs, before.Version)
	}
	if after, _, _ := p.store.GetEntity(riRepo, "pr", "40"); after != before {
		t.Fatalf("entity changed:\nbefore %+v\nafter  %+v", before, after)
	}

	// A second run changes nothing and records nothing.
	res, err = riReinterpret(t, p, "pr", "40", ReinterpretOptions{})
	if err != nil || !res.Write.Written || res.Write.DecisionChanged || res.Write.Recorded {
		t.Fatalf("second run = %+v, %v, want written, unchanged, unrecorded", res, err)
	}
	if n := len(riHistory(t, p.store, "pr", "40")); n != 1 {
		t.Fatalf("history length = %d, want still 1", n)
	}
}

// INV-REINTERPRET-8: the change record is for a `pr` only.
func TestReinterpretStored_RecordsOnlyForPR(t *testing.T) {
	p := riPipeline(t)
	issue := gather.IssueFacts{IssueShow: []byte(`{"key":"PROJ-1","summary":"s","status":"Open"}`)}
	factsJSON := mustJSON(t, issue)
	p.store.SeedEntityAndInterpretationForTest(t,
		store.Entity{Repo: riRepo, EntityType: "issue", EntityID: "PROJ-1", Facts: string(factsJSON), AsOf: "2026-09-16T00:00:00Z", ContentHash: "h"},
		store.Interpretation{
			Repo: riRepo, EntityType: "issue", EntityID: "PROJ-1", Enrichment: "{}", Urgency: "{}", Dispositions: "[]", MatchReasons: "[]",
			Approvals: `{"waiting_on":"zzz"}`, Panel: "old-panel", AsOf: "2020-01-01T00:00:00Z",
		})
	res, err := riReinterpret(t, p, "issue", "PROJ-1", ReinterpretOptions{})
	if err != nil || !res.Write.Written || !res.Write.DecisionChanged {
		t.Fatalf("result = %+v, %v, want a written, changed issue row", res, err)
	}
	if res.Write.Recorded || len(riHistory(t, p.store, "issue", "PROJ-1")) != 0 {
		t.Fatalf("an issue re-interpretation appended a record: %+v", res)
	}
}

func TestReinterpretStored_MissingEntityIsNotFoundAndWritesNothing(t *testing.T) {
	p := riPipeline(t)
	res, err := riReinterpret(t, p, "pr", "nope", ReinterpretOptions{})
	if err != nil || res.Found || res.Write.Written {
		t.Fatalf("result = %+v, %v, want a quiet not-found", res, err)
	}
}

// INV-REINTERPRET-6: facts committed between the read and the write are not
// overwritten; the write is reported skipped, not failed.
func TestReinterpretStored_DifferentFactsCommittedBetweenReadAndWriteSkipTheWrite(t *testing.T) {
	p := riPipeline(t)
	seedStale(t, p, "41")
	opts := ReinterpretOptions{beforeWrite: func() error {
		e, _, _ := p.store.GetEntity(riRepo, "pr", "41")
		e.ContentHash = "someone-hydrated-different-facts"
		return p.store.UpsertEntity(e)
	}}
	res, err := riReinterpret(t, p, "pr", "41", opts)
	if err != nil || !res.Write.Skipped || res.Write.Written {
		t.Fatalf("result = %+v, %v, want skipped", res, err)
	}
	if row, _, _ := p.store.GetInterpretation(riRepo, "pr", "41"); row.Panel != "old-panel" {
		t.Fatalf("a skipped write overwrote the row: %+v", row)
	}
	if n := len(riHistory(t, p.store, "pr", "41")); n != 0 {
		t.Fatalf("a skipped write appended %d records", n)
	}
}

// INV-REINTERPRET-4: ReinterpretStored requires the handle to carry a stamp.
func TestReinterpretStored_RequiresAStamp(t *testing.T) {
	st := store.OpenNewSchemaForTest(t) // no stamp set
	p := New(testCfg(), st)
	seedInterpretOnlyEntity(t, p, "42", minimalFacts(t, map[string]any{"author": "me", "title": "x"}))
	_, err := ReinterpretStored(context.Background(), st, p.cfg, interpret.SystemClock{}, "pr", "42", ReinterpretOptions{})
	if !errors.Is(err, store.ErrStampRequired) {
		t.Fatalf("err = %v, want ErrStampRequired", err)
	}
	if stage, _ := StageOf(err); stage != StageStore {
		t.Fatalf("stage = %q, want store", stage)
	}
	if _, found, _ := st.GetInterpretation(riRepo, "pr", "42"); found {
		t.Fatal("a refused write left a row")
	}
}

// INV-REINTERPRET-12: a schema-1 store is refused with the cutover message.
func TestReinterpretStored_SchemaOneStoreIsRefused(t *testing.T) {
	st := store.OpenForTest(t)
	st.SetInterpStamp("s")
	p := New(testCfg(), st)
	seedInterpretOnlyEntity(t, p, "43", minimalFacts(t, map[string]any{"author": "me", "title": "x"}))
	_, err := ReinterpretStored(context.Background(), st, p.cfg, interpret.SystemClock{}, "pr", "43", ReinterpretOptions{})
	if !errors.Is(err, store.ErrOldSchema) {
		t.Fatalf("err = %v, want the cutover refusal", err)
	}
	if _, found, _ := st.GetInterpretation(riRepo, "pr", "43"); found {
		t.Fatal("a refused write left a row")
	}
}

func TestReinterpretStored_CancelledContextStopsBeforeReading(t *testing.T) {
	p := riPipeline(t)
	seedStale(t, p, "44")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ReinterpretStored(ctx, p.store, p.cfg, p.clock, "pr", "44", ReinterpretOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if stage, _ := StageOf(err); stage != StageLoadFacts {
		t.Fatalf("stage = %q", stage)
	}
	if row, _, _ := p.store.GetInterpretation(riRepo, "pr", "44"); row.Panel != "old-panel" {
		t.Fatal("a cancelled call wrote")
	}
}

func TestReinterpretStored_UnknownEntityTypeIsAnInterpretFailure(t *testing.T) {
	p := riPipeline(t)
	p.store.SeedEntityForTest(t, store.Entity{Repo: riRepo, EntityType: "widget", EntityID: "w", Facts: "{}", AsOf: "2026-09-16T00:00:00Z", ContentHash: "h"})
	_, err := riReinterpret(t, p, "widget", "w", ReinterpretOptions{})
	if stage, _ := StageOf(err); stage != StageInterpret || err == nil || !strings.Contains(err.Error(), "reinterpret interpret widget w") {
		t.Fatalf("err = %v (stage %q), want an interpret-stage failure", err, stage)
	}
}

// INV-REINTERPRET-1: RunInterpretOnly leaves the entity row alone (facts,
// as-of, hash, version, hydration time, list fingerprint) and records a
// changed decision.
func TestRunInterpretOnly_LeavesTheEntityRowAloneAndRecordsAChange(t *testing.T) {
	var out bytes.Buffer
	p := interpretOnlyPipeline(t, &out)
	seedStale(t, p, "45")
	before, _, _ := p.store.GetEntity(riRepo, "pr", "45")
	if err := p.RunInterpretOnly(context.Background(), "pr", "45", gather.ChangeChanged); err != nil {
		t.Fatal(err)
	}
	if after, _, _ := p.store.GetEntity(riRepo, "pr", "45"); after != before {
		t.Fatalf("entity rewritten:\nbefore %+v\nafter  %+v", before, after)
	}
	if recs := riHistory(t, p.store, "pr", "45"); len(recs) != 1 {
		t.Fatalf("history = %+v, want one interpretation_changed record", recs)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

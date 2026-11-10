package pipeline

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/corpus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// This file pins TODAY's interpretation-row behavior over the golden corpus
// (internal/corpus): the byte-exact row goldens under testdata/golden/rows were
// captured from the code as it stood before any behavior-changing commit, and
// every path that writes an interpretation row is held to them.

// goldenRow is the path of a fixture's row golden. Every path, whatever it is,
// MUST produce exactly these bytes.
func goldenRow(name string) string {
	return filepath.Join("testdata", "golden", "rows", name+".row.txt")
}

const corpusAsOf = "2026-09-16T00:00:00Z"

// corpusGatherer answers every gather with one fixture's payload.
type corpusGatherer struct{ fx corpus.Fixture }

func (g corpusGatherer) result() gather.GatherResult {
	return gather.GatherResult{Payload: json.RawMessage(g.fx.Payload), AsOf: corpusAsOf}
}

func (g corpusGatherer) GatherEntity(context.Context, string, gather.ChangeKind) (gather.GatherResult, error) {
	return g.result(), nil
}

func (g corpusGatherer) GatherEntityOn(context.Context, string, string, gather.ChangeKind) (gather.GatherResult, error) {
	return g.result(), nil
}

func (g corpusGatherer) GatherEntityWatch(context.Context, string, string) (gather.GatherResult, error) {
	return g.result(), nil
}

// corpusPipeline is a pipeline over the corpus configuration and fixed clock
// whose gatherer for every entity type answers with fx.
func corpusPipeline(t *testing.T, st *store.Store, fx corpus.Fixture) *Pipeline {
	t.Helper()
	p := New(corpus.Config(), st, WithClock(corpus.Clock()), WithLogWriter(io.Discard))
	g := corpusGatherer{fx}
	p.entityGatherers = map[string]gather.EntityGatherer{"pr": g, "issue": g, "thread": g}
	p.extractors = NewExtractorRegistry(p.cfg, p.store, p.repo())
	return p
}

func mustFixtures(t *testing.T) []corpus.Fixture {
	t.Helper()
	fxs, err := corpus.Fixtures()
	if err != nil {
		t.Fatal(err)
	}
	if len(fxs) < 40 {
		t.Fatalf("the corpus holds %d fixtures, want at least 40", len(fxs))
	}
	return fxs
}

func renderStored(t *testing.T, st *store.Store, fx corpus.Fixture) []byte {
	t.Helper()
	row, found, err := st.GetInterpretation(corpus.Repo, fx.Type, fx.ID)
	if err != nil || !found {
		t.Fatalf("%s: interpretation row found=%v err=%v", fx.Name, found, err)
	}
	return corpus.RenderRow(row)
}

// Hydration (RunEntityChange) writes the row the golden holds. This test
// CAPTURES the goldens (PG_DESK_UPDATE_GOLDEN=1) and every other path is
// compared with them.
func TestCorpusRowGoldens_Hydration(t *testing.T) {
	for _, fx := range mustFixtures(t) {
		t.Run(fx.Name, func(t *testing.T) {
			st := store.OpenNewSchemaForTest(t)
			p := corpusPipeline(t, st, fx)
			if _, err := p.RunEntityChange(context.Background(), fx.Type, fx.ID, gather.ChangeAdded, EntityChangeOptions{}); err != nil {
				t.Fatalf("RunEntityChange: %v", err)
			}
			corpus.AssertBytes(t, goldenRow(fx.Name), renderStored(t, st, fx))
		})
	}
}

// RunGenericEntity (the `run issue` hydration) writes the same row, for every
// type. Schema 1 is gone from every write path (the interpretation write
// requires schema 2), so only the new schema is characterized here; the
// refusal on a schema-1 store is pinned in entity_test.go.
func TestCorpusRowGoldens_RunGenericEntity(t *testing.T) {
	stores := map[string]func(testing.TB) *store.Store{
		"new-schema": func(t testing.TB) *store.Store { return store.OpenNewSchemaForTest(t) },
	}
	for _, fx := range mustFixtures(t) {
		for schema, open := range stores {
			t.Run(fx.Name+"/"+schema, func(t *testing.T) {
				st := open(t)
				p := corpusPipeline(t, st, fx)
				if err := p.RunGenericEntity(context.Background(), fx.Type, fx.ID, gather.ChangeAdded); err != nil {
					t.Fatalf("RunGenericEntity: %v", err)
				}
				corpus.AssertBytes(t, goldenRow(fx.Name), renderStored(t, st, fx))
			})
		}
	}
}

// The issue feed row (IngestFeedRow) writes the same row for an issue fixture.
func TestCorpusRowGoldens_FeedRow(t *testing.T) {
	for _, fx := range mustFixtures(t) {
		if fx.Type != "issue" {
			continue
		}
		t.Run(fx.Name, func(t *testing.T) {
			st := store.OpenNewSchemaForTest(t)
			InstallIssueGate(st, nil)
			p := corpusPipeline(t, st, fx)
			res, err := p.IngestFeedRow(context.Background(), FeedRowInput{
				Instance: frInstance, StoreIdentity: frIdentity,
				Row: gather.FeedRow{Seq: 1, EntityID: fx.ID, Kinds: []string{"added"}},
			})
			if err != nil || !res.Written {
				t.Fatalf("IngestFeedRow: res=%+v err=%v", res, err)
			}
			corpus.AssertBytes(t, goldenRow(fx.Name), renderStored(t, st, fx))
		})
	}
}

// RunInterpretOnly over the stored facts of a PR fixture writes the same row
// and leaves a second run idempotent.
func TestCorpusRowGoldens_RunInterpretOnly(t *testing.T) {
	for _, fx := range mustFixtures(t) {
		if fx.Type != "pr" {
			continue
		}
		t.Run(fx.Name, func(t *testing.T) {
			st := store.OpenNewSchemaForTest(t)
			if err := st.UpsertEntity(store.Entity{
				Repo: corpus.Repo, EntityType: "pr", EntityID: fx.ID,
				Facts: string(fx.Payload), AsOf: corpusAsOf, ContentHash: "seed",
			}); err != nil {
				t.Fatal(err)
			}
			p := corpusPipeline(t, st, fx)
			for i := 0; i < 2; i++ {
				if err := p.RunInterpretOnly(context.Background(), "pr", fx.ID, gather.ChangeChanged); err != nil {
					t.Fatalf("RunInterpretOnly: %v", err)
				}
				corpus.AssertBytes(t, goldenRow(fx.Name), renderStored(t, st, fx))
			}
		})
	}
}

// Every reason code of both reason tables has a fixture, and the fixture's row
// carries exactly that decision. Adding a rule to a table requires adding a
// fixture, or this test fails.
func TestCorpusCoversEveryReasonCode(t *testing.T) {
	byName := map[string]corpus.Fixture{}
	for _, fx := range mustFixtures(t) {
		byName[fx.Name] = fx
	}
	check := func(prefix, ownership string, rows []interpret.ReasonRow) {
		for _, r := range rows {
			fx, ok := byName["pr-"+prefix+"-"+r.Reason]
			if !ok {
				t.Errorf("reason code %q (%s) has no fixture pr-%s-%s", r.Reason, ownership, prefix, r.Reason)
				continue
			}
			st := store.OpenNewSchemaForTest(t)
			p := corpusPipeline(t, st, fx)
			if _, err := p.RunEntityChange(context.Background(), "pr", fx.ID, gather.ChangeAdded, EntityChangeOptions{}); err != nil {
				t.Fatal(err)
			}
			row, _, _ := st.GetInterpretation(corpus.Repo, "pr", fx.ID)
			var appr struct {
				WaitingOn string `json:"waiting_on"`
				Reason    string `json:"reason"`
			}
			if err := json.Unmarshal([]byte(row.Approvals), &appr); err != nil {
				t.Fatal(err)
			}
			if appr.Reason != r.Reason || appr.WaitingOn != string(r.WaitingOn) || !strings.HasPrefix(row.Ownership, ownership) {
				t.Errorf("%s: decision = (%q, %q, ownership %q), want (%q, %q, %s)", fx.Name, appr.WaitingOn, appr.Reason, row.Ownership, r.WaitingOn, r.Reason, ownership)
			}
		}
	}
	check("own", "mine", interpret.OwnReasons)
	check("team", "team", interpret.TeamReasons)
}

// waiting_on_me is the legacy boolean still serialized into approvals; a fixture
// with Deps keeps its removal or reordering visible.
func TestCorpusKeepsLegacyWaitingOnMe(t *testing.T) {
	var fx corpus.Fixture
	for _, f := range mustFixtures(t) {
		if f.Name == "pr-waiting-on-me-deps" {
			fx = f
		}
	}
	st := store.OpenNewSchemaForTest(t)
	p := corpusPipeline(t, st, fx)
	if _, err := p.RunEntityChange(context.Background(), "pr", fx.ID, gather.ChangeAdded, EntityChangeOptions{}); err != nil {
		t.Fatal(err)
	}
	row, _, _ := st.GetInterpretation(corpus.Repo, "pr", fx.ID)
	if !strings.Contains(row.Approvals, `"waiting_on_me":true`) {
		t.Errorf("approvals = %s, want waiting_on_me true for the Deps fixture", row.Approvals)
	}
}

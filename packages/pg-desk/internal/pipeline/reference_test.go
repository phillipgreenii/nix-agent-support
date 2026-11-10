package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/corpus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// refRow is the TEST-ONLY reference implementation's row: its own local struct
// of column strings, deliberately not store.Interpretation, so a later change
// to the store struct (the stamp column, the removal of SyncError) needs no edit
// here.
//
// WHY THIS EXISTS: it is a measuring stick, a second, independently written copy
// of the pre-refactor decode step and row builder (the literal inside persist and
// interpretationRow, as they stood before interpretEntity). The differential
// test compares what the production code writes with it, column by column, over
// the whole corpus. It lives in a _test.go file and is NOT production code: the
// plan's one-way-to-interpret rule (no duplicated code) applies to production,
// and the byte-exact goldens are the durable guard once this file is retired.
type refRow struct {
	repo, entityType, entityID, ownership string
	enrichment, urgency, category         string
	dispositions, approvals, gateState    string
	matchReasons, panel                   string
	readyToPromote, degraded              bool
	asOf                                  string
}

func refJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// refBuild decodes a stored payload the way the pre-refactor code did and builds
// its row: a pr payload is decoded as gather.Facts and interpreted directly (the
// RunInterpretOnly shape); an issue or thread payload goes through its
// registered entity interpreter (the hydration shape).
func refBuild(t *testing.T, fx corpusFixture) refRow {
	t.Helper()
	var in interpret.Interpretation
	var err error
	switch fx.Type {
	case "pr":
		var facts gather.Facts
		if err = json.Unmarshal(fx.Payload, &facts); err != nil {
			t.Fatal(err)
		}
		in, err = interpret.Interpret(facts, corpus.Clock(), corpus.Config())
	case "issue":
		in, err = interpret.InterpretIssue(gather.GatherResult{Payload: fx.Payload}, corpus.Clock(), corpus.Config())
	case "thread":
		in, err = interpret.InterpretThread(gather.GatherResult{Payload: fx.Payload}, corpus.Clock(), corpus.Config())
	default:
		t.Fatalf("unknown type %q", fx.Type)
	}
	if err != nil {
		t.Fatal(err)
	}
	return refRow{
		repo: corpus.Repo, entityType: fx.Type, entityID: fx.ID, ownership: in.Ownership,
		enrichment: refJSON(in.Enrichment), urgency: refJSON(in.Urgency), category: in.Category,
		dispositions: refJSON(in.Dispositions), approvals: refJSON(in.Approvals), gateState: in.GateState,
		matchReasons: refJSON(in.MatchReasons), panel: in.Panel,
		readyToPromote: in.ReadyToPromote, degraded: in.Degraded != "", asOf: in.AsOf,
	}
}

type corpusFixture = corpus.Fixture

// columns lists a stored row as name/value pairs, in table order.
func (r refRow) columns() [][2]string {
	return [][2]string{
		{"repo", r.repo},
		{"entity_type", r.entityType},
		{"entity_id", r.entityID},
		{"ownership", r.ownership},
		{"enrichment", r.enrichment},
		{"urgency", r.urgency},
		{"category", r.category},
		{"dispositions", r.dispositions},
		{"approvals", r.approvals},
		{"gate_state", r.gateState},
		{"match_reasons", r.matchReasons},
		{"panel", r.panel},
		{"ready_to_promote", fmt.Sprint(r.readyToPromote)},
		{"degraded", fmt.Sprint(r.degraded)},
		{"as_of", r.asOf},
	}
}

func storedColumns(s store.Interpretation) [][2]string {
	return refRow{
		repo: s.Repo, entityType: s.EntityType, entityID: s.EntityID, ownership: s.Ownership,
		enrichment: s.Enrichment, urgency: s.Urgency, category: s.Category, dispositions: s.Dispositions,
		approvals: s.Approvals, gateState: s.GateState, matchReasons: s.MatchReasons, panel: s.Panel,
		readyToPromote: s.ReadyToPromote, degraded: s.Degraded, asOf: s.AsOf,
	}.columns()
}

// The differential test: the row every production path writes equals the
// reference implementation's, column by column, over the whole corpus.
func TestCorpusDifferentialAgainstReference(t *testing.T) {
	for _, fx := range mustFixtures(t) {
		t.Run(fx.Name, func(t *testing.T) {
			st := store.OpenNewSchemaForTest(t)
			p := corpusPipeline(t, st, fx)
			if _, err := p.RunEntityChange(context.Background(), fx.Type, fx.ID, gather.ChangeAdded, EntityChangeOptions{}); err != nil {
				t.Fatal(err)
			}
			got, found, err := st.GetInterpretation(corpus.Repo, fx.Type, fx.ID)
			if err != nil || !found {
				t.Fatalf("row found=%v err=%v", found, err)
			}
			want := refBuild(t, fx).columns()
			have := storedColumns(got)
			for i := range want {
				if want[i] != have[i] {
					t.Errorf("column %s: production %q, reference %q", want[i][0], have[i][1], want[i][1])
				}
			}
		})
	}
}

// Arbitrary bytes as stored facts: interpretEntity never panics, and on an
// error it returns the zero row (nothing for a caller to write). The seed corpus
// runs as ordinary unit tests.
func FuzzInterpretEntityFacts(f *testing.F) {
	fxs, err := corpus.Fixtures()
	if err != nil {
		f.Fatal(err)
	}
	for _, fx := range fxs {
		if fx.Type == "pr" {
			f.Add(fx.Payload)
		}
	}
	f.Add([]byte(""))
	f.Add([]byte("not json"))
	f.Add([]byte(`{"pr_show":"x"}`))
	f.Add([]byte(`{"pr_show":{"reviews":[{"commit_oid":null}],"comments":[{}]}}`))
	f.Fuzz(func(t *testing.T, facts []byte) {
		row, _, err := interpretEntity(corpus.Repo, corpus.Config(), corpus.Clock(), "pr", "1", gather.GatherResult{Payload: facts, AsOf: corpusAsOf})
		if err != nil && row != (store.Interpretation{}) {
			t.Fatalf("an error (%v) must return the zero row, got %+v", err, row)
		}
	})
}

package parity

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// goldenDoc builds a minimal valid golden document for the parse tests.
func goldenDoc(mutate func(g *Golden)) []byte {
	g := &Golden{
		Scenario:   "s",
		FrozenFrom: Frozen{AgentSupportCommit: "abc", PgDesk: "/nix/store/x-pg-desk"},
		OldPlan:    []Entry{e("anchor", OpCreate), e("process-feedback", OpCreate)},
		Explained: []Expected{
			exp("S16", "process-feedback", OpCreate, SideOld),
			exp("S13", "fix-ci", OpCreate, SideNew),
		},
		ExpectedPlan: []Entry{e("anchor", OpCreate), e("fix-ci", OpCreate)},
	}
	if mutate != nil {
		mutate(g)
	}
	b, err := json.Marshal(g)
	if err != nil {
		panic(err)
	}
	return b
}

func TestParseGoldenAcceptsAConsistentGolden(t *testing.T) {
	g, err := ParseGolden(goldenDoc(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.ExpectedPlan) != 2 || len(g.Explained) != 2 {
		t.Fatalf("golden = %+v", g)
	}
}

func TestParseGoldenRefusesAnInconsistentOrIncompleteGolden(t *testing.T) {
	cases := map[string]func(g *Golden){
		"expected_plan edited without an explanation": func(g *Golden) {
			g.ExpectedPlan = append(g.ExpectedPlan, e("review-pr", OpCreate))
		},
		"explanation without the expected_plan edit": func(g *Golden) {
			g.ExpectedPlan = []Entry{e("anchor", OpCreate)}
		},
		"old-only explanation of an entry the old plan lacks": func(g *Golden) {
			g.Explained = append(g.Explained, exp("S15", "review-pr", OpCreate, SideOld))
		},
		"no pinned commit":  func(g *Golden) { g.FrozenFrom.AgentSupportCommit = "" },
		"no pinned pg-desk": func(g *Golden) { g.FrozenFrom.PgDesk = "" },
		"unknown exception id": func(g *Golden) {
			g.Explained[1].ID = "S99"
		},
		"empty scenario name": func(g *Golden) { g.Scenario = "" },
		"an empty entry":      func(g *Golden) { g.ExpectedPlan[0].Kind = "" },
		"nothing recorded at all (0 equals 0)": func(g *Golden) {
			g.OldPlan, g.Explained, g.ExpectedPlan = nil, nil, nil
		},
	}
	for name, mutate := range cases {
		if _, err := ParseGolden(goldenDoc(mutate)); err == nil {
			t.Errorf("%s: want an error, got none", name)
		}
	}
	if _, err := ParseGolden([]byte(strings.Replace(string(goldenDoc(nil)), `"scenario"`, `"scenarioo"`, 1))); err == nil {
		t.Error("unknown member: want an error, got none")
	}
}

func TestApplyExplainedRemovesOldOnlyAndAddsNewOnly(t *testing.T) {
	got, err := ApplyExplained(
		[]Entry{e("anchor", OpCreate), e("anchor", OpCreate), e("review-pr", OpCreate)},
		[]Expected{exp("S15", "anchor", OpCreate, SideOld), exp("S13", "fix-ci", OpCreate, SideNew)},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{e("anchor", OpCreate), e("fix-ci", OpCreate), e("review-pr", OpCreate)}
	if !equalEntries(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestGoldenIsFrozenOnceAndCoversEveryScenario guards the golden files
// themselves; it needs no binaries.
func TestGoldenIsFrozenOnceAndCoversEveryScenario(t *testing.T) {
	scs, err := Scenarios()
	if err != nil {
		t.Fatal(err)
	}
	if len(scs) < MinScenarios {
		t.Fatalf("%d scenarios, the gate requires at least %d", len(scs), MinScenarios)
	}
	names, err := goldenNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != len(scs) {
		t.Errorf("%d goldens for %d scenarios: every scenario needs exactly one", len(names), len(scs))
	}
	gs, err := LoadGoldens() // also refuses a set frozen from two commits
	if err != nil {
		t.Fatal(err)
	}
	commit := regexp.MustCompile(`^[0-9a-f]{40}$`)
	total := 0
	for name, g := range gs {
		if !commit.MatchString(g.FrozenFrom.AgentSupportCommit) {
			t.Errorf("%s: frozen_from.agent_support_commit %q is not a full commit hash", name, g.FrozenFrom.AgentSupportCommit)
		}
		if !strings.HasPrefix(g.FrozenFrom.PgDesk, "/nix/store/") {
			t.Errorf("%s: frozen_from.pg_desk %q is not a nix store path", name, g.FrozenFrom.PgDesk)
		}
		total += len(g.ExpectedPlan)
	}
	if total == 0 {
		t.Error("the goldens expect no entry at all: the gate would compare 0 with 0")
	}
}

// TestGoldenKeepsTheHandPinnedUnobservableEntries: the golden MUST carry the
// pins the old side cannot observe, because they are most of what the gate
// protects (design 6.7).
func TestGoldenKeepsTheHandPinnedUnobservableEntries(t *testing.T) {
	gs, err := LoadGoldens()
	if err != nil {
		t.Fatal(err)
	}
	pins := 0
	for name, g := range gs {
		for _, x := range g.Explained {
			if x.ID != "UNOBS" {
				continue
			}
			pins++
			if indexOfEntry(g.ExpectedPlan, Entry{x.Entity, x.Kind, x.Op}) < 0 {
				t.Errorf("%s: pinned %s %s %s is not in expected_plan", name, x.Entity, x.Kind, x.Op)
			}
		}
	}
	if pins == 0 {
		t.Error("no UNOBS pin in any golden")
	}
}

// TestParityGateNegativeCases changes one kind, and one operation, of the
// comparison input of every scenario and shows the gate fails; an unchanged
// input passes. It also shows a stale golden entry and an unplanned extra
// entry fail. It runs on the golden itself, so it needs no binaries.
func TestParityGateNegativeCases(t *testing.T) {
	gs, err := LoadGoldens()
	if err != nil {
		t.Fatal(err)
	}
	for name, g := range gs {
		t.Run(name, func(t *testing.T) {
			planned := append([]Entry(nil), g.ExpectedPlan...)
			if rep := Compare(name, g, planned); !rep.Clean() {
				t.Fatalf("an unchanged input must pass, got %+v", rep)
			}
			if len(planned) == 0 {
				// A scenario whose decider plan is empty has nothing to
				// change; the extra-entry case below is its negative.
				extra := append(append([]Entry(nil), planned...), e("anchor", OpCreate))
				if rep := Compare(name, g, extra); rep.Clean() {
					t.Error("an unplanned extra entry passed")
				}
				return
			}
			kind := append([]Entry(nil), planned...)
			kind[0].Kind = kind[0].Kind + "-changed"
			if rep := Compare(name, g, kind); rep.Clean() || len(rep.Unexplained) != 2 {
				t.Errorf("one kind changed must fail with the stale and the unplanned entry, got %+v", rep)
			}
			op := append([]Entry(nil), planned...)
			op[0].Op = op[0].Op + "-changed"
			if rep := Compare(name, g, op); rep.Clean() || len(rep.Unexplained) != 2 {
				t.Errorf("one op changed must fail with the stale and the unplanned entry, got %+v", rep)
			}
			if rep := Compare(name, g, planned[1:]); rep.Clean() || len(rep.Unexplained) != 1 || rep.Unexplained[0].Side != SideOld {
				t.Errorf("a stale golden entry (one the decider no longer plans) must fail, got %+v", rep)
			}
			extra := append(append([]Entry(nil), planned...), Entry{planned[0].Entity, "review-pr", "reopen"})
			if rep := Compare(name, g, extra); rep.Clean() || len(rep.Unexplained) != 1 || rep.Unexplained[0].Side != SideNew {
				t.Errorf("an unplanned extra entry must fail, got %+v", rep)
			}
		})
	}
}

func TestClassifyUnhydratedPlan(t *testing.T) {
	const empty = `{"actions":[],"skipped":[]}`
	if why, err := classifyUnhydratedPlan("x", 0, empty, ""); err != nil || !strings.Contains(why, "no action") {
		t.Errorf("an empty plan is a decline: %q, %v", why, err)
	}
	if why, err := classifyUnhydratedPlan("x", 3, "", "unhydrated view\nmore"); err != nil || !strings.Contains(why, "exit 3") || !strings.Contains(why, "unhydrated view") || strings.Contains(why, "more") {
		t.Errorf("a non-zero exit is a refusal named by its first line: %q, %v", why, err)
	}
	planned := `{"actions":[{"op":"create","kind":"anchor","rule":"anchor.lazy"}],"skipped":[]}`
	if _, err := classifyUnhydratedPlan("x", 0, planned, ""); err == nil {
		t.Error("a plan with an action built from an unhydrated row must be an error")
	}
	if _, err := classifyUnhydratedPlan("x", 0, "not json", ""); err == nil {
		t.Error("an undecodable plan must be an error")
	}
}

func TestFixtureLegacySeededMustNameAnEvaluatedEntity(t *testing.T) {
	good := `{"name":"n","description":"d","entities":["acme/api#1"],"legacy_seeded":["acme/api#1"],"prs":[{"number":1}]}`
	if _, err := ParseFixture([]byte(good)); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseFixture([]byte(strings.Replace(good, `"legacy_seeded":["acme/api#1"]`, `"legacy_seeded":["acme/api#2"]`, 1))); err == nil {
		t.Error("a legacy_seeded entity that is not evaluated must be refused")
	}
}

func TestTheLegacyScenarioExists(t *testing.T) {
	sc := scenarioByPrefix(t, "19-")
	fx, err := LoadFixture(sc)
	if err != nil {
		t.Fatal(err)
	}
	if len(fx.LegacySeeded) == 0 {
		t.Fatal("scenario 19 seeds no legacy row")
	}
}

// TestGateFailsOnAnEditedGoldenAgainstTheRealDecider runs the real decider for
// one scenario and shows the gate comparison passes on its golden and fails on
// a golden with one kind, one op, or an entry changed.
func TestGateFailsOnAnEditedGoldenAgainstTheRealDecider(t *testing.T) {
	env := realEnv(t)
	sc := scenarioByPrefix(t, "01-")
	g, err := LoadGolden(sc)
	if err != nil {
		t.Fatal(err)
	}
	nw, err := RunNew(context.Background(), env, sc)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := NormalizeNew(nw)
	if err != nil {
		t.Fatal(err)
	}
	if rep := Compare(sc.Name, g, planned); !rep.Clean() {
		t.Fatalf("the real decider's plan must pass its golden, got %+v", rep)
	}
	for name, edit := range map[string]func(x *Golden){
		"kind":  func(x *Golden) { x.ExpectedPlan[0].Kind = "fix-ci" },
		"op":    func(x *Golden) { x.ExpectedPlan[0].Op = "close" },
		"stale": func(x *Golden) { x.ExpectedPlan = append(x.ExpectedPlan, e("resolve-conflict", OpCreate)) },
	} {
		edited := *g
		edited.ExpectedPlan = append([]Entry(nil), g.ExpectedPlan...)
		edit(&edited)
		if rep := Compare(sc.Name, &edited, planned); rep.Clean() {
			t.Errorf("a golden with a changed %s passed against the real decider", name)
		}
	}
}

// TestLegacyRowStoreHelperRefusesANonLegacyStore: the helper writes only into a
// version-1 store (before the cutover). Against a cut-over store it must refuse
// rather than write a row the cutover never produced.
func TestLegacyRowStoreHelperRefusesANonLegacyStore(t *testing.T) {
	env := realEnv(t)
	sc := scenarioByPrefix(t, "19-")
	fx, err := LoadFixture(sc)
	if err != nil {
		t.Fatal(err)
	}
	sb, cleanup, err := newSandbox(env, fx, "helper")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	sqlite3, err := env.sqlite3Bin()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := sb.step(ctx, sb.deskExec, "heartbeat"); err != nil {
		t.Fatal(err)
	}
	if err := sb.seedLegacyRow(ctx, sqlite3, fx.LegacySeeded[0]); err != nil {
		t.Fatalf("a version-1 store takes the legacy row: %v", err)
	}
	if _, err := sb.step(ctx, sb.deskExec, "migrate", "--cutover"); err != nil {
		t.Fatal(err)
	}
	version, hydrated, err := sb.readEntityRow(ctx, sqlite3, fx.LegacySeeded[0])
	if err != nil || version != 0 || hydrated != "<null>" {
		t.Fatalf("after the cutover the row is version %d hydrated_at %q (%v), want version 0, never hydrated", version, hydrated, err)
	}
	if err := sb.seedLegacyRow(ctx, sqlite3, "acme/api#120"); err == nil {
		t.Error("the helper wrote a legacy row into a cut-over store")
	}
	if _, err := sb.step(ctx, sb.deskExec, "pr", "refresh", fx.LegacySeeded[0]); err != nil {
		t.Fatal(err)
	}
	version, hydrated, err = sb.readEntityRow(ctx, sqlite3, fx.LegacySeeded[0])
	if err != nil || version == 0 || hydrated == "<null>" {
		t.Fatalf("after the refresh the row is version %d hydrated_at %q (%v), want hydrated", version, hydrated, err)
	}
}

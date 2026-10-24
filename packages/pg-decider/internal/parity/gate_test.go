package parity

import (
	"context"
	"strings"
	"testing"
)

// TestExpectedDiffNamesRealScenariosAndCoversTheDocumentedExceptions needs no
// binaries: it guards the explained differences recorded in the goldens.
func TestExpectedDiffNamesRealScenariosAndCoversTheDocumentedExceptions(t *testing.T) {
	exp, err := LoadExpected()
	if err != nil {
		t.Fatal(err)
	}
	scs, err := Scenarios()
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, sc := range scs {
		known[sc.Name] = true
	}
	ids := map[string]bool{}
	for name, list := range exp {
		if !known[name] {
			t.Errorf("expected-diff names scenario %q, which does not exist", name)
		}
		for _, x := range list {
			ids[x.ID] = true
		}
	}
	for _, id := range []string{"S13", "S14", "S15", "S16", "S19", "S24", "S26", "R16"} {
		if !ids[id] {
			t.Errorf("expected-diff lists no %s difference", id)
		}
	}
}

// TestParityGate runs every scenario through the decider as its own subtest and
// fails on any difference from the scenario's frozen golden: an entry the
// decider plans that the golden does not list, or a stale golden entry the
// decider no longer plans. The subtest names are what the nix check asserts a
// PASS line for.
func TestParityGate(t *testing.T) {
	env := realEnv(t)
	scs, err := Scenarios()
	if err != nil {
		t.Fatal(err)
	}
	if len(scs) < MinScenarios {
		t.Fatalf("the gate covers %d scenarios, it requires at least %d", len(scs), MinScenarios)
	}
	gs, err := LoadGoldens()
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range scs {
		t.Run(sc.Name, func(t *testing.T) {
			o := RunScenario(context.Background(), env, sc, gs[sc.Name])
			var b strings.Builder
			if !Render(&b, []Outcome{o}) {
				t.Errorf("parity gate is not clean:\n%s", b.String())
			}
			fx, err := LoadFixture(sc)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range fx.LegacySeeded {
				if o.Refused[id] == "" {
					t.Errorf("the legacy-seeded %s was not shown to be declined before its refresh", id)
				}
			}
		})
	}
}

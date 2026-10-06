package parity

import (
	"context"
	"strings"
	"testing"
)

// TestExpectedDiffNamesRealScenariosAndCoversTheDocumentedExceptions needs no
// binaries: it guards the expected-diff file itself.
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
	for _, id := range []string{"S13", "S14", "S15", "S16", "S19", "S24", "S26"} {
		if !ids[id] {
			t.Errorf("expected-diff lists no %s difference", id)
		}
	}
}

// TestParityGate runs every scenario through both runners and fails on any
// unexplained difference, or on a listed exception that did not occur.
func TestParityGate(t *testing.T) {
	env := realEnv(t)
	outs, err := RunGate(context.Background(), env)
	if err != nil {
		t.Fatalf("RunGate: %v", err)
	}
	scs, _ := Scenarios()
	if len(outs) != len(scs) {
		t.Fatalf("gate covered %d scenarios, want all %d", len(outs), len(scs))
	}
	var b strings.Builder
	if !Render(&b, outs) {
		t.Errorf("parity gate is not clean:\n%s", b.String())
	}
}

// TestTeamPRFeedbackCycleExceptionIsExercised pins S16 on the real binaries:
// for a team PR with unaddressed comments the old side plans a feedback-cycle
// row and the new side plans no process-feedback action.
func TestTeamPRFeedbackCycleExceptionIsExercised(t *testing.T) {
	env := realEnv(t)
	sc := scenarioByPrefix(t, "03-")
	old, err := RunOld(context.Background(), env, sc)
	if err != nil {
		t.Fatal(err)
	}
	nw, err := RunNew(context.Background(), env, sc)
	if err != nil {
		t.Fatal(err)
	}
	id := sc.Entities[0]
	sawCycle := false
	for _, r := range old.Rows[id] {
		if r.Kind == "feedback-cycle" {
			sawCycle = true
		}
	}
	if !sawCycle {
		t.Errorf("old side planned no feedback-cycle row for the team PR: %v", old.Rows[id])
	}
	for _, a := range nw.Plans[id].Actions {
		if a.Kind == "process-feedback" {
			t.Errorf("new side planned a process-feedback action for a team PR: %+v", a)
		}
	}
}

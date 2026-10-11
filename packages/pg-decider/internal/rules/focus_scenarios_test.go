package rules

// The shared bead-state scenarios, decider side: ONE list of scenarios
// (testdata/focus/state-scenarios.json) that pg-desk reads for its echo
// prediction and this package reads for the actual plan over the same view,
// so "the echo says will hold" and "the decider holds" stay true together.
// pg-desk keeps a byte-identical copy at
// packages/pg-desk/internal/focus/testdata/state-scenarios.json (the two Go
// modules cannot import each other and are built from separate source trees);
// pg-desk's TestStateScenarioCopiesAreByteIdentical keeps the two equal in the
// repository checkout. Every helper here is prefixed "focusScenario" because
// sibling packets add test files to this package.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/config"
)

type focusScenarioPlan struct {
	Transition string `json:"transition"`
	SkipReason string `json:"skip_reason"`
	Cause      string `json:"cause"`
	MarkerOnly bool   `json:"marker_only"`
	ApplySkips bool   `json:"apply_skips"`
}

type focusScenario struct {
	Name    string          `json:"name"`
	Row     string          `json:"row"`
	View    json.RawMessage `json:"view"`
	Decider *struct {
		Strike focusScenarioPlan `json:"strike"`
		Select focusScenarioPlan `json:"select"`
	} `json:"decider"`
}

type focusScenarioFile struct {
	Contract string `json:"contract"`
	Period   string `json:"period"`
	Config   struct {
		BeadIDPattern string `json:"bead_id_pattern"`
	} `json:"config"`
	Scenarios []focusScenario `json:"scenarios"`
}

func focusScenarioLoad(t *testing.T) focusScenarioFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "focus", "state-scenarios.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f focusScenarioFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.Contract != "pg-desk.focus-state-scenarios/v1" || f.Period == "" {
		t.Fatalf("state scenarios: contract %q period %q", f.Contract, f.Period)
	}
	return f
}

// TestFocusStateScenariosMatchThePlan runs decide.DecideWith over each
// scenario's view, once as a strike (annotations.focus_selected none) and once
// as a select (the period), and checks the transition or the skip the shared
// list says the decider plans.
func TestFocusStateScenariosMatchThePlan(t *testing.T) {
	file := focusScenarioLoad(t)
	cfg := &config.Config{BeadIDPattern: file.Config.BeadIDPattern}
	rows := map[string]bool{}
	plans := 0
	for _, sc := range file.Scenarios {
		if sc.Decider == nil {
			continue
		}
		for _, mode := range []struct {
			name       string
			annotation string
			plan       focusScenarioPlan
		}{
			{"strike", "none", sc.Decider.Strike},
			{"select", file.Period, sc.Decider.Select},
		} {
			t.Run(sc.Name+"/"+mode.name, func(t *testing.T) {
				var m map[string]any
				if err := json.Unmarshal(sc.View, &m); err != nil {
					t.Fatal(err)
				}
				suiteAnnotations(m)["focus_selected"] = mode.annotation
				o := focusDecide(t, focusParse(t, m), cfg)
				plans++
				if mode.plan.Transition == "" {
					if o.act != nil {
						t.Fatalf("want a skip %q (%s), got action %+v", mode.plan.SkipReason, mode.plan.Cause, *o.act)
					}
					if o.skip.Reason != mode.plan.SkipReason {
						t.Errorf("skip reason = %q, want %q (facts %v)", o.skip.Reason, mode.plan.SkipReason, o.skip.Facts)
					}
					if got := focusCause(o); got != mode.plan.Cause {
						t.Errorf("skip cause = %q, want %q", got, mode.plan.Cause)
					}
					return
				}
				if o.act == nil {
					t.Fatalf("want a %s action, got skip %+v", mode.plan.Transition, *o.skip)
				}
				if got := focusTransition(o); got != mode.plan.Transition {
					t.Errorf("transition = %q, want %q", got, mode.plan.Transition)
				}
				if marker, _ := o.act.Facts["marker_only"].(bool); marker != mode.plan.MarkerOnly {
					t.Errorf("marker_only = %v, want %v", marker, mode.plan.MarkerOnly)
				}
				switch mode.plan.Transition {
				case "mint":
					if o.act.Op != action.OpCreate {
						t.Errorf("a mint is op %q, want create", o.act.Op)
					}
				default:
					if o.act.Op != action.OpUpdate {
						t.Errorf("%s is op %q, want update", mode.plan.Transition, o.act.Op)
					}
				}
			})
		}
		rows[sc.Row] = true
	}
	// The list covers every row of the state table the decider can see: all
	// of them but the apply-time stale hold and the run machinery's escalation.
	if len(rows) < 15 || plans < 2*len(rows) {
		t.Fatalf("only %d rows and %d plans checked: a vacuous scenario list", len(rows), plans)
	}
}

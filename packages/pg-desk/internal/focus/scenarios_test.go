package focus

// The shared bead-state scenarios: ONE list of scenarios (testdata/
// state-scenarios.json) that pg-desk reads for its echo prediction and
// pg-decider reads for its actual plan over the same view, so "the echo says
// will hold" and "the decider holds" stay true together. The decider's copy
// lives at packages/pg-decider/internal/rules/testdata/focus/state-scenarios.json
// (the two Go modules cannot import each other and are built from separate
// source trees), and TestStateScenarioCopiesAreByteIdentical keeps the two
// equal in the repository checkout.
//
// Telemetry (D24): test code; it emits and logs nothing.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const (
	scenariosFile = "testdata/state-scenarios.json"
	// deciderScenariosRel is the decider's copy, relative to this module's root.
	deciderScenariosRel = "../pg-decider/internal/rules/testdata/focus/state-scenarios.json"
	scenarioContract    = "pg-desk.focus-state-scenarios/v1"
)

type scenarioEffect struct {
	Effect  string `json:"effect"`
	Because string `json:"because"`
}

type scenarioPGDesk struct {
	State        BeadState      `json:"state"`
	Shown        string         `json:"shown"`
	OpenChildren bool           `json:"open_children"`
	Unwatched    bool           `json:"unwatched"`
	Strike       scenarioEffect `json:"strike"`
	Select       scenarioEffect `json:"select"`
}

// scenarioPlan is what the decider plans for one mode: a transition (an
// action) or a skip with its reason and cause. MarkerOnly marks a release
// that rewrites only the marker; ApplySkips marks a hold the decider plans
// and the apply step then skips (the bead has open children).
type scenarioPlan struct {
	Transition string `json:"transition"`
	SkipReason string `json:"skip_reason"`
	Cause      string `json:"cause"`
	MarkerOnly bool   `json:"marker_only"`
	ApplySkips bool   `json:"apply_skips"`
}

type scenarioDecider struct {
	Strike scenarioPlan `json:"strike"`
	Select scenarioPlan `json:"select"`
}

type scenario struct {
	Name      string           `json:"name"`
	Row       BeadState        `json:"row"`
	View      json.RawMessage  `json:"view"`
	PGDesk    *scenarioPGDesk  `json:"pg_desk"`
	Decider   *scenarioDecider `json:"decider"`
	Na        string           `json:"na"`
	DeciderNa string           `json:"decider_na"`
}

type scenarioFile struct {
	Contract string `json:"contract"`
	Period   string `json:"period"`
	Config   struct {
		BeadIDPattern  string `json:"bead_id_pattern"`
		SuppressedKind string `json:"suppressed_kind"`
	} `json:"config"`
	Scenarios []scenario `json:"scenarios"`
}

func loadScenarios(t *testing.T, raw []byte) scenarioFile {
	t.Helper()
	var f scenarioFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("state scenarios: %v", err)
	}
	if f.Contract != scenarioContract {
		t.Fatalf("state scenarios contract = %q, want %q", f.Contract, scenarioContract)
	}
	return f
}

func ownScenarios(t *testing.T) scenarioFile {
	t.Helper()
	raw, err := os.ReadFile(scenariosFile)
	if err != nil {
		t.Fatal(err)
	}
	return loadScenarios(t, raw)
}

// scenarioView is the part of the composite view pg-desk builds a store from.
type scenarioView struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Snapshot struct {
		State          string `json:"state"`
		Merged         bool   `json:"merged"`
		StatusCategory string `json:"status_category"`
	} `json:"snapshot"`
	Annotations struct {
		Hidden struct {
			Value bool `json:"value"`
		} `json:"hidden"`
		Suppress []string `json:"suppress"`
	} `json:"annotations"`
	Links []struct {
		ID       string            `json:"id"`
		Relation string            `json:"relation"`
		State    string            `json:"state"`
		Assignee string            `json:"assignee"`
		Labels   []string          `json:"labels"`
		Metadata map[string]string `json:"metadata"`
	} `json:"links"`
}

// classifyScenario stores the scenario's source and linked beads as the
// gather stage would and returns the bead column the live view builds for the
// source: the production classification, over the same facts the decider's
// view carries.
func classifyScenario(t *testing.T, file scenarioFile, sc scenario) BeadCell {
	t.Helper()
	var v scenarioView
	if err := json.Unmarshal(sc.View, &v); err != nil {
		t.Fatalf("%s: view: %v", sc.Name, err)
	}
	f := newFixture(t)
	f.cfg.BeadIDPattern = file.Config.BeadIDPattern
	f.cfg.Focus.SuppressedKind = file.Config.SuppressedKind

	var src Key
	switch v.Type {
	case "pr":
		src = f.pr(v.ID, prSpec{state: v.Snapshot.State, merged: v.Snapshot.Merged, ownership: "mine"})
	case "issue":
		src = f.issue(v.ID, issueSpec{state: v.Snapshot.State, category: v.Snapshot.StatusCategory})
	default:
		t.Fatalf("%s: unsupported source type %q", sc.Name, v.Type)
	}
	if v.Annotations.Hidden.Value {
		f.hide(src)
	}
	for _, kind := range v.Annotations.Suppress {
		if err := f.st.SetAnnotation(store.KVAnnotation{
			Repo: testRepo, EntityType: src.Type, EntityID: src.ID, Key: store.KeySuppress(kind),
			Value: "true", Origin: "pg-desk", SetBy: "operator", SetAt: fixtureAt,
		}); err != nil {
			t.Fatalf("%s: suppress: %v", sc.Name, err)
		}
	}
	for _, l := range v.Links {
		b := f.issue(l.ID, issueSpec{state: l.State, assignee: l.Assignee, labels: l.Labels, metadata: l.Metadata})
		f.link(b, src, l.Relation)
		if sc.PGDesk.OpenChildren {
			f.issue(l.ID+"-child", issueSpec{state: "open", parent: l.ID})
		}
	}
	opt := LiveOptions{}
	if sc.PGDesk.Unwatched {
		opt.Watched = Watched{src.Type: WatchSet{IDs: map[string]bool{}, Known: true}}
	}
	live := BuildLive(f.inputs(), []Key{src}, Ranking{}, opt)
	return live.Beads[src]
}

// TestStateScenariosPredictTheEcho runs every scenario through the live view
// pg-desk builds and checks the bead column and the strike and select
// effects the echo would print against the scenario's expectation.
func TestStateScenariosPredictTheEcho(t *testing.T) {
	file := ownScenarios(t)
	ran := 0
	for _, sc := range file.Scenarios {
		if sc.PGDesk == nil {
			continue
		}
		t.Run(sc.Name, func(t *testing.T) {
			ran++
			cell := classifyScenario(t, file, sc)
			if cell.State != sc.PGDesk.State {
				t.Fatalf("classified as %q, want %q", cell.State, sc.PGDesk.State)
			}
			if cell.Shown != sc.PGDesk.Shown {
				t.Errorf("shown = %q, want %q", cell.Shown, sc.PGDesk.Shown)
			}
			row := RowOf(cell.State)
			if got := (scenarioEffect{row.StrikeEffect, row.StrikeBecause}); got != sc.PGDesk.Strike {
				t.Errorf("strike = %+v, want %+v", got, sc.PGDesk.Strike)
			}
			if got := (scenarioEffect{row.SelectEffect, row.SelectBecause}); got != sc.PGDesk.Select {
				t.Errorf("select = %+v, want %+v", got, sc.PGDesk.Select)
			}
		})
	}
	if ran < len(BeadStates)-3 {
		t.Fatalf("only %d scenarios ran against pg-desk: a vacuous list", ran)
	}
}

// TestStateScenariosCoverEveryTableRow: every row of the state table has at
// least one scenario, a scenario names a real row, and a scenario that cannot
// be a view says why.
func TestStateScenariosCoverEveryTableRow(t *testing.T) {
	file := ownScenarios(t)
	covered := map[BeadState]bool{}
	for _, sc := range file.Scenarios {
		if RowOf(sc.Row).State == "" {
			t.Errorf("scenario %q names the unknown row %q", sc.Name, sc.Row)
		}
		covered[sc.Row] = true
		if string(sc.View) == "null" || len(sc.View) == 0 {
			if sc.Na == "" {
				t.Errorf("scenario %q has no view and no na reason", sc.Name)
			}
			if sc.PGDesk != nil || sc.Decider != nil {
				t.Errorf("scenario %q has no view but expects an outcome", sc.Name)
			}
			continue
		}
		if sc.PGDesk == nil {
			t.Errorf("scenario %q has a view but no pg_desk expectation", sc.Name)
		}
		if sc.Decider == nil && sc.DeciderNa == "" {
			t.Errorf("scenario %q has no decider expectation and no decider_na reason", sc.Name)
		}
	}
	for _, row := range BeadStates {
		if !covered[row.State] {
			t.Errorf("no scenario exercises the table row %q", row.State)
		}
	}
}

// TestStateScenarioEffectsAgreeWithTheDeciderPlans: for every scenario and
// both modes, the effect the echo prints (read from the table) and the
// transition the decider plans say the same thing, and a skip the decider
// reports uses the skip reason of the table row. This is the half of the
// parity that needs the pg-desk table; the decider's own test checks that
// decide.Decide really plans what the scenario says.
func TestStateScenarioEffectsAgreeWithTheDeciderPlans(t *testing.T) {
	file := ownScenarios(t)
	checked := 0
	for _, sc := range file.Scenarios {
		if sc.PGDesk == nil || sc.Decider == nil {
			continue
		}
		row := RowOf(sc.PGDesk.State)
		for _, m := range []struct {
			mode   string
			effect scenarioEffect
			plan   scenarioPlan
		}{
			{"strike", sc.PGDesk.Strike, sc.Decider.Strike},
			{"select", sc.PGDesk.Select, sc.Decider.Select},
		} {
			checked++
			want := ""
			switch m.plan.Transition {
			case "":
				want = "none"
			case "mint":
				want = "mint"
			case "hold", "hold_terminal":
				want = "hold"
				if m.plan.ApplySkips {
					want = "none"
				}
			case "release":
				want = "release"
				if m.plan.MarkerOnly {
					want = "none"
				}
			default:
				t.Errorf("%s/%s: unknown transition %q", sc.Name, m.mode, m.plan.Transition)
			}
			if m.effect.Effect != want {
				t.Errorf("%s/%s: the echo says %q but the decider plans %q (transition %q)",
					sc.Name, m.mode, m.effect.Effect, want, m.plan.Transition)
			}
			if m.plan.SkipReason != "" && row.SkipReason != skipNA && m.plan.SkipReason != row.SkipReason {
				t.Errorf("%s/%s: the decider skips with %q but the table row %q says %q",
					sc.Name, m.mode, m.plan.SkipReason, row.State, row.SkipReason)
			}
			if (m.plan.Transition == "") == (m.plan.SkipReason == "") {
				t.Errorf("%s/%s: a plan is exactly one of a transition and a skip: %+v", sc.Name, m.mode, m.plan)
			}
		}
	}
	if checked < 2*(len(BeadStates)-3) {
		t.Fatalf("checked only %d plans: a vacuous list", checked)
	}
}

// TestStateScenarioCopiesAreByteIdentical: the two copies of the scenario
// list are one file. It needs the decider's copy, which is a sibling module,
// so it runs in the repository checkout and skips, with a message, inside the
// nix build sandbox (see readOutsideModule).
func TestStateScenarioCopiesAreByteIdentical(t *testing.T) {
	mine, err := os.ReadFile(scenariosFile)
	if err != nil {
		t.Fatal(err)
	}
	theirs := readOutsideModule(t, "pg-decider's state-scenarios.json", filepath.Join(moduleRootDir(t), deciderScenariosRel))
	if len(mine) < 1000 {
		t.Fatalf("pg-desk's copy is %d bytes: a vacuous list", len(mine))
	}
	if !bytes.Equal(mine, theirs) {
		t.Errorf("the two copies of state-scenarios.json differ (%d and %d bytes): edit both together, "+
			"packages/pg-desk/internal/focus/testdata and packages/pg-decider/internal/rules/testdata/focus", len(mine), len(theirs))
	}
}

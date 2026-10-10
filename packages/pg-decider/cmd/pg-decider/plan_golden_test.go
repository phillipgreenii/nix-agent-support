package main

// Golden `plan` output (text and --json) over the shared synthetic view
// fixtures in ../../testdata/views, run through the real binary entry point
// with the real rule registry. Between them the scenarios produce all five
// skip reasons and every action op.
//
// Golden files live in testdata/plan/<scenario>.plan.txt and
// <scenario>.plan.json. The text goldens are compared byte for byte. The JSON
// goldens are compared with insignificant whitespace removed (key order and
// every value still must match exactly), because the repo's formatter reflows
// short JSON arrays onto one line. Regenerate them after an intended output
// change with:
//
//	go test ./cmd/pg-decider -run Golden -update
//
// then review the diff.

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/exitcode"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden plan files under testdata/plan")

const goldenEntityID = "acme/widgets#42"

// goldenScenario names a scenario (the golden file stem), its view fixture and
// the entity; typ and id default to the PR of goldenEntityID.
type goldenScenario struct{ name, fixture, typ, id string }

// goldenScenarios are the golden plan scenarios.
var goldenScenarios = []goldenScenario{
	// Every rule hidden.
	{name: "hidden", fixture: "pr_hidden"},
	// A new PR with a conflict, failing CI and unaddressed feedback: creates,
	// the anchor first.
	{name: "new_everything", fixture: "pr_new_everything"},
	// Two kinds suppressed; the other kinds still act.
	{name: "suppressed_kinds", fixture: "pr_all_kinds_suppressed"},
	// Anchor drift, adoption, a node_id rewrite and a ready_to_land
	// annotation; an open review item at the current head (review-pending)
	// and an open conflict item for the current context (already handled).
	{name: "anchor_drift", fixture: "pr_anchor_drift"},
	// A merged PR: the open children and the anchor close.
	{name: "merged_open_anchor", fixture: "pr_merged_open_anchor"},
	// A merged or closed PR with no anchor and no work items: every rule
	// skips, so nothing is created for a dead PR.
	{name: "merged_no_anchor", fixture: "pr_merged_no_anchor"},
	{name: "closed_no_anchor", fixture: "pr_closed_no_anchor"},
	// An open PR whose anchor was closed: the anchor reopens.
	{name: "reopened_anchor", fixture: "pr_reopened_anchor_closed"},
	// An ISSUE source selected into the focus plan with no focus bead: the one
	// focus.item create, the only rule of the issue rule set.
	{name: "issue_focus_mint", fixture: "issue_focus_selected", typ: "issue", id: "ACME-7"},
}

// entity is the scenario's entity type and id.
func (sc goldenScenario) entity() (typ, id string) {
	typ, id = sc.typ, sc.id
	if typ == "" {
		typ = "pr"
	}
	if id == "" {
		id = goldenEntityID
	}
	return typ, id
}

func goldenPath(scenario, ext string) string {
	return filepath.Join("testdata", "plan", scenario+".plan."+ext)
}

// goldenCompactJSON strips insignificant whitespace, so a formatter reflowing
// the golden file does not matter.
func goldenCompactJSON(t *testing.T, s string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(s)); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, s)
	}
	return buf.String()
}

func TestGoldenPlanOutput(t *testing.T) {
	for _, sc := range goldenScenarios {
		for _, form := range []struct {
			ext  string
			args []string
		}{
			{"txt", nil},
			{"json", []string{"--json"}},
		} {
			t.Run(sc.name+"/"+form.ext, func(t *testing.T) {
				withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture(filepath.Join("views", sc.fixture+".json")))
				typ, id := sc.entity()
				args := append([]string{"plan", typ, id}, form.args...)
				out, errOut, code := runCLI(t, args...)
				if code != exitcode.OK || errOut != "" {
					t.Fatalf("code=%d err=%q", code, errOut)
				}

				path := goldenPath(sc.name, form.ext)
				if *updateGolden {
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("%v (regenerate with -update)", err)
				}
				got, wantText := out, string(want)
				if form.ext == "json" {
					got, wantText = goldenCompactJSON(t, out), goldenCompactJSON(t, wantText)
				}
				if got != wantText {
					t.Errorf("%s differs from the plan output (regenerate with -update after an intended change):\n--- got ---\n%s\n--- want ---\n%s", path, out, want)
				}
			})
		}
	}
}

// Together the JSON goldens contain all five skip reasons and each of the
// five ops, and every registered rule appears in every golden.
func TestGoldenPlansCoverEverySkipReasonAndOp(t *testing.T) {
	reasons, ops := map[string]bool{}, map[string]bool{}
	for _, sc := range goldenScenarios {
		b, err := os.ReadFile(goldenPath(sc.name, "json"))
		if err != nil {
			t.Fatalf("%v (regenerate with -update)", err)
		}
		var plan struct {
			Actions []struct{ Op, Rule string }     `json:"actions"`
			Skipped []struct{ Rule, Reason string } `json:"skipped"`
		}
		if err := json.Unmarshal(b, &plan); err != nil {
			t.Fatalf("%s: %v", sc.name, err)
		}
		seen := map[string]bool{}
		for _, a := range plan.Actions {
			ops[a.Op] = true
			seen[a.Rule] = true
		}
		for _, s := range plan.Skipped {
			reasons[s.Reason] = true
			seen[s.Rule] = true
		}
		typ, _ := sc.entity()
		for _, r := range decide.RulesFor(typ) {
			if !seen[r.ID()] {
				t.Errorf("%s: rule %s is in neither actions nor skipped", sc.name, r.ID())
			}
		}
	}

	wantReasons := decide.SkipReasons()
	sort.Strings(wantReasons)
	var gotReasons []string
	for r := range reasons {
		gotReasons = append(gotReasons, r)
	}
	sort.Strings(gotReasons)
	if len(gotReasons) != len(wantReasons) {
		t.Errorf("skip reasons in the goldens = %v, want all of %v", gotReasons, wantReasons)
	}
	for _, r := range wantReasons {
		if !reasons[r] {
			t.Errorf("no golden plan has skip reason %q", r)
		}
	}
	for _, op := range []string{"create", "update", "reopen", "close", "annotate"} {
		if !ops[op] {
			t.Errorf("no golden plan has an %q action", op)
		}
	}
}

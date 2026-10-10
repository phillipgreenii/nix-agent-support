package main

// CLI-level behavior of the focus.item rule: the run fails closed on a view
// that lacks annotations.focus_selected (exit 3, nothing written, no rule or
// hook runs), and the decider configuration reaches the rule.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/exitcode"
	"github.com/phillipgreenii/pg-decider/internal/view"
)

// focusViewWithout writes view fixture name (under ../../testdata or
// ../../testdata/views) with annotations.focus_selected removed, and returns
// its path.
func focusViewWithout(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	ann := m["annotations"].(map[string]any)
	if _, ok := ann["focus_selected"]; !ok {
		t.Fatalf("%s has no focus_selected member to remove", name)
	}
	delete(ann, "focus_selected")
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "view.json")
	writeFile(t, p, string(out))
	return p
}

func TestPlanFailsClosedWhenFocusSelectedIsAbsent(t *testing.T) {
	for _, tc := range []struct{ typ, id, fixture string }{
		{"pr", "acme/widgets#42", fixture("pr_view_minimal.json")},
		{"issue", "ACME-7", fixture(filepath.Join("views", "issue_focus_selected.json"))},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			// Precondition: the unmodified fixture plans (exit 0), so the exit 3
			// below is the absent member and nothing else.
			withHelper(t, "GO_HELPER_STDOUT_FILE="+tc.fixture)
			if _, errOut, code := runCLI(t, "plan", tc.typ, tc.id); code != exitcode.OK {
				t.Fatalf("with the member: code=%d err=%q", code, errOut)
			}

			withHelper(t, "GO_HELPER_STDOUT_FILE="+focusViewWithout(t, tc.fixture))
			out, errOut, code := runCLI(t, "plan", tc.typ, tc.id)
			if code != exitcode.ViewUnreadable || out != "" ||
				!strings.Contains(errOut, "view-lacks-focus_selected") || !strings.Contains(errOut, "annotations.focus_selected") {
				t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
			}
		})
	}
}

func TestApplyFailsClosedWhenFocusSelectedIsAbsentAndWritesNothing(t *testing.T) {
	for _, tc := range []struct{ typ, id, fixture string }{
		{"pr", "acme/widgets#42", fixture("pr_view_minimal.json")},
		{"issue", "ACME-7", fixture(filepath.Join("views", "issue_focus_selected.json"))},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			withHelper(t, "GO_HELPER_STDOUT_FILE="+focusViewWithout(t, tc.fixture))
			recs := recordWrites(t, []action.Action{{Op: action.OpClose, Rule: "r", Target: str("wb-1")}}, `{"result":{}}`, "0")
			decided := false
			inner := decideFn
			decideFn = func(v *view.View, typ string, cfg *config.Config) action.PlanResult {
				decided = true
				return inner(v, typ, cfg)
			}
			_, errOut, code := runCLI(t, "apply", tc.typ, tc.id)
			if code != exitcode.ViewUnreadable || !strings.Contains(errOut, "view-lacks-focus_selected") {
				t.Fatalf("code=%d err=%q", code, errOut)
			}
			if len(*recs) != 0 || decided {
				t.Fatalf("a failed-closed run must write nothing and decide nothing: decided=%v execs=%+v", decided, *recs)
			}
			if strings.Contains(errOut, "run-counters") {
				t.Fatalf("no counters line is printed before the telemetry packet adds it: %q", errOut)
			}
		})
	}
}

// A present null is "unset", not absent: plan and apply proceed.
func TestPlanAndApplyAcceptAPresentNullFocusSelected(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	if _, errOut, code := runCLI(t, "plan", "pr", "acme/widgets#42"); code != exitcode.OK {
		t.Fatalf("plan: code=%d err=%q", code, errOut)
	}
	recordWrites(t, nil, `{"result":{}}`, "0")
	if _, errOut, code := runCLI(t, "apply", "pr", "acme/widgets#42"); code != exitcode.OK {
		t.Fatalf("apply: code=%d err=%q", code, errOut)
	}
}

// The configuration reaches the rule: bead_id_pattern keeps a bead-id source
// from being minted, and focus_priority_map sets the minted bead's priority.
func TestPlanPassesTheConfigurationToTheFocusRule(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture(filepath.Join("views", "issue_focus_selected.json")))

	planPriority := func(cfg string) (priority string, minted bool) {
		t.Helper()
		p := filepath.Join(t.TempDir(), "c.json")
		writeFile(t, p, cfg)
		t.Setenv("PG_DECIDER_CONFIG", p)
		out, errOut, code := runCLI(t, "plan", "issue", "ACME-7", "--json")
		if code != exitcode.OK {
			t.Fatalf("code=%d err=%q", code, errOut)
		}
		var plan struct {
			Actions []struct {
				Op     string
				Fields struct{ Priority string }
			}
		}
		if err := json.Unmarshal([]byte(out), &plan); err != nil {
			t.Fatal(err)
		}
		if len(plan.Actions) == 0 {
			return "", false
		}
		return plan.Actions[0].Fields.Priority, true
	}

	if pri, ok := planPriority(`{}`); !ok || pri != "P1" {
		t.Fatalf("default map: priority %q minted %v, want P1 true", pri, ok)
	}
	if pri, ok := planPriority(`{"focus_priority_map":{"High":"P0"}}`); !ok || pri != "P0" {
		t.Fatalf("configured map: priority %q minted %v, want P0 true", pri, ok)
	}
	if _, ok := planPriority(`{"bead_id_pattern":"^ACME-[0-9]+$"}`); ok {
		t.Fatal("an issue whose id matches bead_id_pattern must not be minted")
	}
}

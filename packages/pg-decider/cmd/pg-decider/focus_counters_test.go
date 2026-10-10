package main

// The focus.item observability of `apply`: the run-counters line carries the
// transitions, the skips and the routed item's seq and id, and the audit
// comment of every applied write names the rule and facts.transition. The
// rule's own planning is tested in internal/rules; here the decider is stubbed
// to a known plan so the wiring from plan and events to the line is exact.

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/exitcode"
	"github.com/phillipgreenii/pg-decider/internal/view"
)

func focusUpdateAction(id, transition string, f action.Fields) action.Action {
	return action.Action{
		Op: action.OpUpdate, Kind: "focus-item", Target: str(id), Rule: "focus.item", Fields: f,
		Facts: map[string]any{"transition": transition, "source_type": "pr", "source_id": "acme/widgets#42"},
	}
}

// counterLine returns the one run-counters line of stderr, parsed.
func counterLine(t *testing.T, errOut string) (string, map[string]any) {
	t.Helper()
	var lines []string
	for _, l := range strings.Split(errOut, "\n") {
		if strings.Contains(l, `"contract":"pg-decider.run-counters/v1"`) {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("want exactly one counters line, got %q (stderr %q)", lines, errOut)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatalf("the counters line must parse as JSON: %v: %q", err, lines[0])
	}
	return lines[0], m
}

func TestApplyRunCountersCarryFocusTransitionsAndTheAuditCommentsCarryFactsTransition(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	cfg := filepath.Join(t.TempDir(), "c.json")
	writeFile(t, cfg, `{"focus_beads_query":"focus-beads"}`)
	t.Setenv("PG_DECIDER_CONFIG", cfg)

	hold := action.Fields{Status: "deferred", Metadata: map[string]string{"focus_hold": "struck"}}
	release := action.Fields{Status: "open", ClearDefer: true, Metadata: map[string]string{"focus_hold": "released"}}
	recs := scriptedWrites(t,
		func(*view.View) []action.Action {
			return []action.Action{
				{
					Op: action.OpCreate, Kind: "focus-item", Rule: "focus.item",
					Fields: action.Fields{Title: "Focus x", Metadata: map[string]string{"dedup_key": "pr:acme/widgets#42:focus-item"}},
					Facts:  map[string]any{"transition": "mint", "source_type": "pr", "source_id": "acme/widgets#42"},
				},
				focusUpdateAction("bd-hold", "hold", hold),
				focusUpdateAction("bd-stale", "hold", hold),
				focusUpdateAction("bd-rel", "release", release),
				focusUpdateAction("bd-term", "hold_terminal", hold),
			}
		},
		func(name, args string) (string, string) {
			switch {
			case strings.HasPrefix(args, "issue list"):
				return `{"entities":[]}`, "0"
			case strings.HasPrefix(args, "issue create"):
				return `{"result":{"id":"bd-new"}}`, "0"
			case strings.HasPrefix(args, "issue show bd-stale"):
				return `{"result":{"id":"bd-stale","state":"in_progress","assignee":"w","served_from":"origin","stale":false}}`, "0"
			case strings.HasPrefix(args, "issue show bd-rel"):
				return `{"result":{"id":"bd-rel","state":"deferred","metadata":{"focus_hold":"struck"},"served_from":"origin","stale":false}}`, "0"
			case strings.HasPrefix(args, "issue show"):
				id := strings.Fields(args)[2]
				return `{"result":{"id":"` + id + `","state":"open","served_from":"origin","stale":false}}`, "0"
			case strings.HasPrefix(args, "issue children"):
				return `{"result":{"children":[]}}`, "0"
			}
			return `{"result":{}}`, "0"
		})
	_, errOut, code := runCLI(t, "apply", "pr", "acme/widgets#42")
	if code != exitcode.OK {
		t.Fatalf("exit %d err %q", code, errOut)
	}
	line, _ := counterLine(t, errOut)
	want := `{"contract":"pg-decider.run-counters/v1","type":"pr","id":"acme/widgets#42","rules":{"focus.item":{` +
		`"planned":5,"applied":4,"deduped":0,"failed":0,"skipped":1,"transitions":{` +
		`"hold":{"applied":1,"skipped-stale":1},"hold_terminal":{"applied":1},"mint":{"applied":1},"release":{"applied":1}}}},` +
		`"escalations":0}`
	if line != want {
		t.Fatalf("got  %q\nwant %q", line, want)
	}

	// One audit comment per applied write, each naming the rule and its
	// transition; the abandoned hold of bd-stale posts none.
	got := map[string]string{}
	for _, r := range *recs {
		if !strings.HasPrefix(r.args, "issue comment ") {
			continue
		}
		id := strings.Fields(r.args)[2]
		if !strings.Contains(r.args, "rule: focus.item") {
			t.Fatalf("comment without the rule id: %q", r.args)
		}
		got[id] = r.args
	}
	for id, transition := range map[string]string{"bd-new": "mint", "bd-hold": "hold", "bd-rel": "release", "bd-term": "hold_terminal"} {
		if c, ok := got[id]; !ok || !strings.Contains(c, `"transition":"`+transition+`"`) {
			t.Fatalf("comment for %s: %q (all %v)", id, c, got)
		}
	}
	if _, ok := got["bd-stale"]; ok || len(got) != 4 {
		t.Fatalf("the skipped-stale hold must post no comment: %v", got)
	}
}

// A source with a focus bead whose rule planned no action still emits a
// counter, and seq and from_item come from the routed item.
func TestApplyPlanSkipOnlyRunEmitsSkipsAndNoActionCountsWithSeqAndFromItem(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	t.Setenv("PG_DECIDER_CONFIG", "")
	recs := recordWrites(t, nil, `{"result":{}}`, "0")
	decideFn = func(*view.View, string, *config.Config) action.PlanResult {
		bead := map[string]any{"id": "bd-1", "state": "in_progress"}
		return action.PlanResult{Actions: []action.Action{}, Skipped: []action.Skip{
			{Rule: "focus.item", Reason: action.ReasonAlreadyHandled, Facts: map[string]any{"cause": "claimed", "bead": bead}},
			{Rule: "r.other", Reason: action.ReasonNotMatched},
		}}
	}
	_, errOut, code := runCLI(t, "apply", "pr", "acme/widgets#42", "--from-item", fixture("routed_item.json"))
	if code != exitcode.OK || len(*recs) != 0 {
		t.Fatalf("exit %d err %q execs %+v", code, errOut, *recs)
	}
	line, _ := counterLine(t, errOut)
	want := `{"contract":"pg-decider.run-counters/v1","type":"pr","id":"acme/widgets#42","rules":{"focus.item":{` +
		`"planned":0,"applied":0,"deduped":0,"failed":0,"skipped":0,"skips":{"claimed":1}}},` +
		`"escalations":0,"seq":17,"from_item":"item-1"}`
	if line != want {
		t.Fatalf("got  %q\nwant %q", line, want)
	}
}

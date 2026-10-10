package rules

// A focus bead is linked to its source by relation "source". The PR rules, the
// closing and reopening cascades and the adoption rules MUST NOT touch it, and
// a PR's plan MUST be what it would be without the bead (daily-focus design
// 8.1 and 12 (d)).

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/plan"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// suiteFocusLinks are the focus-bead links added to a PR view: a keyed bead
// whose parent names the PR's anchor, a keyed bead in the node_id-less key
// form that adoption.node-id would rewrite for another entity, and keyless
// beads whose titles look like the PR's own anchor and child titles.
func suiteFocusLinks(entityID, anchorID string) []map[string]any {
	focus := func(id, key, title string, md map[string]any) map[string]any {
		m := map[string]any{"source_type": "pr", "source_id": entityID}
		if key != "" {
			m["dedup_key"] = key
		}
		for k, v := range md {
			m[k] = v
		}
		return map[string]any{
			"type": "issue", "id": id, "relation": "source", "state": "open",
			"title": title, "labels": []string{"focus-item"}, "metadata": m,
		}
	}
	return []map[string]any{
		focus("bd-focus-1", "pr:"+entityID+":focus-item", "Focus "+entityID+" - a title", map[string]any{"parent": anchorID}),
		focus("bd-focus-2", "", entityID+": looks like the anchor", nil),
		focus("bd-focus-3", "", "review-pr: "+entityID, nil),
		focus("bd-focus-4", "", "fix-ci: "+entityID, nil),
	}
}

// suitePlanBytes renders the plan of every rule EXCEPT focus.item. That rule
// reads the focus bead by design (its skip carries the bead's id and state,
// and a view that shows no selection holds an open bead), so the invariant
// under test is about every OTHER rule.
func suitePlanBytes(t *testing.T, v *view.View) (js, text []byte) {
	t.Helper()
	res := suiteDecide(v)
	skipped, actions := res.Skipped[:0:0], res.Actions[:0:0]
	for _, s := range res.Skipped {
		if s.Rule != focusRuleID {
			skipped = append(skipped, s)
		}
	}
	for _, a := range res.Actions {
		if a.Rule != focusRuleID {
			actions = append(actions, a)
		}
	}
	res.Skipped, res.Actions = skipped, actions
	var jb, tb bytes.Buffer
	if err := plan.JSON(&jb, res); err != nil {
		t.Fatal(err)
	}
	if err := plan.Text(&tb, v, res); err != nil {
		t.Fatal(err)
	}
	return jb.Bytes(), tb.Bytes()
}

func TestFocusBeadLinkedToPRDoesNotChangePRPlan(t *testing.T) {
	// The fixtures between them make the anchor rules, the closing cascade,
	// the reopening rule and the adoption rules act, so an unchanged plan is
	// not a vacuous one.
	for _, name := range suiteTriggerFixtures {
		t.Run(name, func(t *testing.T) {
			base := suiteLoad(t, name)
			anchorID := ""
			if a, ok := workitem.BuildIndex(base).Anchor(); ok {
				anchorID = a.ID
			}
			with := suitePatch(t, name, func(m map[string]any) {
				links, _ := m["links"].([]any)
				for _, l := range suiteFocusLinks(base.ID, anchorID) {
					links = append(links, l)
				}
				m["links"] = links
			})
			if len(with.Links) != len(base.Links)+4 {
				t.Fatalf("links = %d, want %d", len(with.Links), len(base.Links)+4)
			}
			ix := workitem.BuildIndex(with)
			if got := ix.ByKind(workitem.KindFocusItem); len(got) != 1 || got[0].ID != "bd-focus-1" {
				t.Fatalf("the keyed focus bead must be indexed exactly once: %+v", got)
			}
			if got, want := len(ix.Adoptable()), len(workitem.BuildIndex(base).Adoptable()); got != want {
				t.Fatalf("adoptable = %d, want %d: a focus bead was adopted", got, want)
			}

			wantJSON, wantText := suitePlanBytes(t, base)
			gotJSON, gotText := suitePlanBytes(t, with)
			if !bytes.Equal(wantJSON, gotJSON) {
				t.Errorf("JSON plan changed:\n--- without ---\n%s\n--- with ---\n%s", wantJSON, gotJSON)
			}
			if !bytes.Equal(wantText, gotText) {
				t.Errorf("text plan changed:\n--- without ---\n%s\n--- with ---\n%s", wantText, gotText)
			}
			var p struct{ Actions []json.RawMessage }
			if err := json.Unmarshal(gotJSON, &p); err != nil || len(p.Actions) == 0 {
				t.Fatalf("the fixture's plan must hold actions (err %v)", err)
			}
		})
	}
}

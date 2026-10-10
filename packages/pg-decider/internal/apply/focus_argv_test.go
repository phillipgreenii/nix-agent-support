package apply

// The recorded-argv contract of the focus hold and release (daily-focus design
// section 10.1, decision D-F19). A fake connector proves argv SHAPE, not bd
// behavior (focus_realbd_test.go covers that), so this file pins the shape: it
// PLANS a hold and a release with the registered focus.item rule over a view,
// APPLIES the plan through the fake connector, and compares the one
// `pg-connector issue update` the apply layer exec'd against LITERAL golden
// vectors, flag order included. Every helper is prefixed "focusArgv" because
// sibling packets add test files to this package.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	_ "github.com/phillipgreenii/pg-decider/internal/rules" // registers focus.item
	"github.com/phillipgreenii/pg-decider/internal/view"
)

const (
	focusArgvPeriod = "2026-10-10"
	focusArgvBead   = "bd-focus-1"

	// The literal goldens: the whole argv of the update the apply layer sends.
	focusArgvHoldVector    = "pg-connector issue update bd-focus-1 --status deferred --metadata focus_hold=struck"
	focusArgvReleaseVector = "pg-connector issue update bd-focus-1 --status open --clear-defer --metadata focus_hold=released"
)

// focusArgvLink is the focus bead a view links to its source.
type focusArgvLink struct {
	id, state, assignee, marker string
}

// focusArgvView is the decider's view of the issue source ACME-7 selected for
// focusArgvPeriod (annotation nil means "not selected") whose linked focus
// bead is in the given state.
func focusArgvView(t *testing.T, annotation any, beadState, marker string) *view.View {
	t.Helper()
	return focusArgvViewFor(t, "ACME-7", annotation, &focusArgvLink{id: focusArgvBead, state: beadState, marker: marker})
}

// focusArgvViewFor builds the view of issue source source with the given
// focus_selected annotation and linked bead (nil: none linked). The source id
// is no bead id, so no bead pattern is needed.
func focusArgvViewFor(t *testing.T, source string, annotation any, bead *focusArgvLink) *view.View {
	t.Helper()
	links := []any{}
	if bead != nil {
		md := map[string]any{
			"source_type": "issue", "source_id": source, "dedup_key": "issue:" + source + ":focus-item",
		}
		if bead.marker != "" {
			md["focus_hold"] = bead.marker
		}
		links = append(links, map[string]any{
			"type": "issue", "id": bead.id, "relation": "source", "state": bead.state, "assignee": bead.assignee,
			"title": "Focus " + source, "labels": []string{"focus-item"}, "metadata": md,
		})
	}
	m := map[string]any{
		"contract": "pg-desk.view/v1", "type": "issue", "id": source, "version": 3,
		"as_of": "2026-10-10T09:00:00Z", "stale": false,
		"snapshot": map[string]any{
			"id": source, "title": "Crash on startup", "state": "In Progress",
			"url": "https://tracker.example/" + source, "status_category": "indeterminate",
			"priority": "High", "issue_type": "Bug", "labels": []string{"backend"},
		},
		"decorations": map[string]any{"relationship": "", "dispositions": []any{}, "urgency": "", "category": ""},
		"annotations": map[string]any{
			"hidden": map[string]any{"value": false, "reason": nil}, "wip": false, "suppress": []any{},
			"force_review": false, "force_review_sha": nil, "ready_to_land": nil,
			"focus_selected": annotation, "decider": map[string]any{},
		},
		"links": links,
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	v, err := view.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// focusArgvPlan plans v and returns the single focus.item action, failing when
// the rule skipped or planned anything else.
func focusArgvPlan(t *testing.T, v *view.View, cfg *config.Config) action.Action {
	t.Helper()
	res := decide.DecideWith(v, v.Type, cfg)
	var got []action.Action
	for _, a := range res.Actions {
		if a.Rule == "focus.item" {
			got = append(got, a)
		}
	}
	if len(got) != 1 {
		t.Fatalf("focus.item planned %d actions, want 1 (skipped: %+v)", len(got), res.Skipped)
	}
	return got[0]
}

// focusArgvApply applies the plan against a fake connector whose live reads
// answer with live (the `issue show --fresh` stdout) and no children, and
// returns every `issue update` the apply layer sent.
func focusArgvApply(t *testing.T, v *view.View, cfg *config.Config, a action.Action, live string) []string {
	t.Helper()
	d := newDouble(t, focusShowRule(live), focusChildrenRule(focusNoChildren))
	res := Run(context.Background(), Input{Type: v.Type, ID: v.ID, View: v, Actions: []action.Action{a}, Env: testEnv(d, cfg)})
	if res.ExitCode != 0 || len(res.Events) != 1 || res.Events[0].Outcome != OutcomeApplied {
		t.Fatalf("apply did not apply the plan: exit %d, events %+v", res.ExitCode, res.Events)
	}
	var updates []string
	for _, l := range d.lines() {
		if strings.HasPrefix(l, "pg-connector issue update") {
			updates = append(updates, l)
		}
	}
	return updates
}

func focusArgvCases() []struct {
	name   string
	cfg    *config.Config
	suffix string
} {
	return []struct {
		name   string
		cfg    *config.Config
		suffix string
	}{
		{name: "no backend pinned", cfg: &config.Config{}},
		{name: "backend pinned", cfg: &config.Config{AgentTrackerBackend: "trk"}, suffix: " --backend trk"},
	}
}

func TestFocusHoldArgvVector(t *testing.T) {
	for _, tc := range focusArgvCases() {
		t.Run(tc.name, func(t *testing.T) {
			// A strike (annotation none) of an open, unclaimed bead is a hold.
			v := focusArgvView(t, nil, "open", "")
			a := focusArgvPlan(t, v, tc.cfg)
			if got, _ := a.Facts["transition"].(string); got != "hold" {
				t.Fatalf("planned transition %q, want hold", got)
			}
			got := focusArgvApply(t, v, tc.cfg, a, focusLive("open", "", ""))
			want := []string{focusArgvHoldVector + tc.suffix}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("hold sent\n  %q\nwant exactly\n  %q", got, want)
			}
		})
	}
}

func TestFocusReleaseArgvVector(t *testing.T) {
	for _, tc := range focusArgvCases() {
		t.Run(tc.name, func(t *testing.T) {
			// A reselect (a real period key) of a HELD bead is a release.
			v := focusArgvView(t, focusArgvPeriod, "deferred", "struck")
			a := focusArgvPlan(t, v, tc.cfg)
			if got, _ := a.Facts["transition"].(string); got != "release" {
				t.Fatalf("planned transition %q, want release", got)
			}
			got := focusArgvApply(t, v, tc.cfg, a, focusLive("deferred", "", "struck"))
			want := []string{focusArgvReleaseVector + tc.suffix}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("release sent\n  %q\nwant exactly\n  %q", got, want)
			}
		})
	}
}

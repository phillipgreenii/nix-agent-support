package arealabels_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/arealabels"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	_ "github.com/phillipgreenii/pg-decider/internal/rules"
	"github.com/phillipgreenii/pg-decider/internal/view"
)

// cfgJSON is the deployment vocabulary under test; every scope and label is a
// neutral placeholder.
const cfgJSON = `{"area_labels":[
	{"pattern":"^feat\\(widgets\\)","labels":["widgets"]},
	{"pattern":"^[a-z]+\\(widgets/api\\)","labels":["widgets-api","widgets"]},
	{"pattern":"(?i)proj-[0-9]+","field":"branch","labels":["proj"]}]}`

func loadCfg(t *testing.T) *config.Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "decider.json")
	if err := os.WriteFile(p, []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvVar, p)
	c, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type link map[string]any

func mkView(t *testing.T, title, branch string, links ...link) *view.View {
	t.Helper()
	if links == nil {
		links = []link{}
	}
	m := map[string]any{
		"contract": "pg-desk.view/v1", "type": "pr", "id": "acme/widgets#42", "version": 1,
		"as_of": "2026-10-01T12:00:00Z", "stale": false,
		"snapshot": map[string]any{
			"id": "acme/widgets#42", "repo": "acme/widgets", "number": 42, "title": title,
			"state": "open", "branch": branch, "base": "main", "author": "teammate",
			"head_sha": "9f3c1e2", "base_sha": "b45e000", "node_id": "PR_node_42",
		},
		"decorations": map[string]any{
			"relationship": "mine", "urgency": "", "category": "",
			"dispositions": []map[string]any{{"comment_id": "c-101", "computed": "open", "override": nil}},
		},
		"annotations": map[string]any{
			"hidden": map[string]any{"value": false, "reason": nil}, "wip": false,
			"suppress": []string{}, "force_review": false, "force_review_sha": nil, "decider": map[string]any{},
		},
		"links": links,
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	v, err := view.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func anchorLink(labels ...string) link {
	return link{
		"type": "issue", "id": "bd-1", "state": "open", "title": "acme/widgets#42: Some title",
		"labels": labels, "metadata": map[string]string{"dedup_key": "pr:PR_node_42:anchor"},
	}
}

func reviewLink(head string, labels ...string) link {
	return link{
		"type": "issue", "id": "bd-2", "state": "open", "title": "review-pr: acme/widgets#42",
		"labels": labels, "metadata": map[string]string{
			"repo": "acme/widgets", "pr_number": "42", "branch": "feature/x", "head_sha": head,
			"ownership": "mine", "dedup_key": "pr:PR_node_42:review-pr",
		},
	}
}

func find(t *testing.T, res action.PlanResult, op action.Op, kind string) action.Action {
	t.Helper()
	for _, a := range res.Actions {
		if a.Op == op && a.Kind == kind {
			return a
		}
	}
	t.Fatalf("no %s %s action in %+v", op, kind, res.Actions)
	return action.Action{}
}

func has(labels []string, l string) bool {
	for _, x := range labels {
		if x == l {
			return true
		}
	}
	return false
}

func TestNewAnchorAndChildrenCarryTheAreaSet(t *testing.T) {
	cfg := loadCfg(t)
	v := mkView(t, "feat(widgets/api): add retry", "me.proj-7.retry")
	res := arealabels.Apply(decide.Decide(v, decide.EntityTypePR), v, cfg)

	want := []string{"proj", "widgets", "widgets-api"}
	anchor := find(t, res, action.OpCreate, "anchor")
	for _, l := range want {
		if !has(anchor.Fields.Labels, l) {
			t.Errorf("anchor create lacks %q: %v", l, anchor.Fields.Labels)
		}
	}
	review := find(t, res, action.OpCreate, "review-pr")
	if !reflect.DeepEqual(review.Fields.Labels, want) {
		t.Errorf("review-pr create labels = %v, want %v", review.Fields.Labels, want)
	}
	fb := find(t, res, action.OpCreate, "process-feedback")
	for _, l := range append([]string{"mine"}, want...) {
		if !has(fb.Fields.Labels, l) {
			t.Errorf("process-feedback create lacks %q: %v", l, fb.Fields.Labels)
		}
	}
	if !strings.HasPrefix(strings.Join(fb.Fields.Labels, " "), "mine fbsum:") {
		t.Errorf("existing labels must keep their order ahead of area labels: %v", fb.Fields.Labels)
	}
}

func TestUnconfiguredPlanIsUnchanged(t *testing.T) {
	v := mkView(t, "feat(widgets/api): add retry", "me.proj-7.retry")
	plan := decide.Decide(v, decide.EntityTypePR)
	for name, cfg := range map[string]*config.Config{"nil": nil, "empty": {}} {
		got := arealabels.Apply(plan, v, cfg)
		if !reflect.DeepEqual(got, plan) {
			t.Errorf("%s config changed the plan:\n got %+v\nwant %+v", name, got, plan)
		}
	}
}

func TestNoMatchAddsNothing(t *testing.T) {
	cfg := loadCfg(t)
	v := mkView(t, "fix: unrelated", "plain")
	plan := decide.Decide(v, decide.EntityTypePR)
	if got := arealabels.Apply(plan, v, cfg); !reflect.DeepEqual(got, plan) {
		t.Fatalf("a PR matching no rule must be unchanged:\n got %+v\nwant %+v", got, plan)
	}
}

func TestApplyDoesNotMutateTheInputPlan(t *testing.T) {
	cfg := loadCfg(t)
	v := mkView(t, "feat(widgets): x", "plain")
	plan := decide.Decide(v, decide.EntityTypePR)
	before, _ := json.Marshal(plan)
	_ = arealabels.Apply(plan, v, cfg)
	if after, _ := json.Marshal(plan); string(before) != string(after) {
		t.Fatalf("input plan mutated:\nbefore %s\nafter  %s", before, after)
	}
}

func TestChildCreateCopiesAreaLabelsAlreadyOnTheAnchor(t *testing.T) {
	cfg := loadCfg(t)
	// The title no longer matches any rule, but the anchor carries two area
	// vocabulary labels (one added by hand) and an unrelated operator label.
	v := mkView(t, "fix: renamed", "plain", anchorLink("widgets-api", "proj", "co-owned"))
	res := arealabels.Apply(decide.Decide(v, decide.EntityTypePR), v, cfg)
	review := find(t, res, action.OpCreate, "review-pr")
	if !reflect.DeepEqual(review.Fields.Labels, []string{"proj", "widgets-api"}) {
		t.Fatalf("review-pr labels = %v, want the anchor's area labels only", review.Fields.Labels)
	}
}

func TestExistingItemsGainOnlyMissingLabelsOnTheirNextWrite(t *testing.T) {
	cfg := loadCfg(t)
	// Anchor already has "widgets"; the review item is stale (older head) and
	// has none. The anchor's metadata is drifted so anchor.backfill writes it.
	anchor := anchorLink("widgets")
	v := mkView(t, "feat(widgets/api): x", "plain", anchor, reviewLink("0ld0000"))
	res := arealabels.Apply(decide.Decide(v, decide.EntityTypePR), v, cfg)

	upd := find(t, res, action.OpUpdate, "anchor")
	if !reflect.DeepEqual(upd.Fields.AddLabels, []string{"widgets-api"}) {
		t.Errorf("anchor add_labels = %v, want only the missing label", upd.Fields.AddLabels)
	}
	rev := find(t, res, action.OpUpdate, "review-pr")
	if !reflect.DeepEqual(rev.Fields.AddLabels, []string{"widgets", "widgets-api"}) {
		t.Errorf("review-pr add_labels = %v", rev.Fields.AddLabels)
	}
	// Labels are only ever added.
	for _, a := range res.Actions {
		if len(a.Fields.RemoveLabels) != 0 && a.Kind != "anchor" && a.Kind != "process-feedback" {
			t.Errorf("unexpected remove_labels on %+v", a)
		}
	}
}

func TestAnchorAlreadyCarryingTheSetGetsNoLabelWrite(t *testing.T) {
	cfg := loadCfg(t)
	v := mkView(t, "feat(widgets): x", "plain", anchorLink("widgets"), reviewLink("0ld0000", "widgets"))
	res := arealabels.Apply(decide.Decide(v, decide.EntityTypePR), v, cfg)
	for _, a := range res.Actions {
		if a.Op == action.OpUpdate && has(a.Fields.AddLabels, "widgets") {
			t.Errorf("label already present, must not be re-added: %+v", a)
		}
	}
}

func TestNoActionIsAddedJustForALabel(t *testing.T) {
	cfg := loadCfg(t)
	// A fully up-to-date anchor lacking the area label: the plan has no
	// anchor write, so none is invented.
	v := mkView(t, "feat(widgets): x", "plain", link{
		"type": "issue", "id": "bd-1", "state": "open", "title": "acme/widgets#42: x",
		"labels": []string{}, "metadata": map[string]string{
			"dedup_key": "pr:PR_node_42:anchor", "repo": "acme/widgets", "pr_number": "42",
			"draft": "false", "state": "open", "branch": "plain", "base": "main",
			"author": "teammate", "url": "",
		},
	}, reviewLink("9f3c1e2", "widgets"))
	plan := decide.Decide(v, decide.EntityTypePR)
	got := arealabels.Apply(plan, v, cfg)
	if len(got.Actions) != len(plan.Actions) {
		t.Fatalf("action count changed: %d -> %d", len(plan.Actions), len(got.Actions))
	}
	for _, a := range got.Actions {
		if a.Kind == "anchor" && a.Op == action.OpUpdate {
			t.Errorf("unexpected anchor write: %+v", a)
		}
	}
}

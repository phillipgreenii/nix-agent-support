package rules

// A property test of the whole rule set (docket pg2-6mo45.3, packet P4c-2): over
// generated PR views, with the work items already linked to them in whatever
// shape and state the tracker may hold, the plan the real registry makes, applied
// to a model of the tracker and the store, leaves a view for which the same
// registry plans nothing. It runs through decide.Decide only, like the rest of
// the cross-cutting suite, and uses rapid with a FIXED seed (a seed given on the
// command line or by RAPID_SEED still wins).

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

func init() {
	pinRapid("RAPID_SEED", "rapid.seed", "1")
	pinRapid("RAPID_NOFAILFILE", "rapid.nofailfile", "true")
}

// pinRapid sets a rapid flag's default unless its environment variable is set;
// a flag on the command line is parsed later and still wins.
func pinRapid(env, name, value string) {
	if os.Getenv(env) != "" {
		return
	}
	if f := flag.Lookup(name); f != nil {
		_ = flag.Set(name, value)
	}
}

// propView is a view as the generator builds it and the model changes it: the
// printed JSON as a map, so View.Raw stays consistent with the typed members.
type propView map[string]any

func (p propView) parse(t *rapid.T) *view.View {
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	v, err := view.Parse(b)
	if err != nil {
		t.Fatalf("the generated view does not parse: %v\n%s", err, b)
	}
	return v
}

func (p propView) obj(key string) map[string]any { return p[key].(map[string]any) }

func (p propView) links() []map[string]any {
	var out []map[string]any
	for _, l := range p["links"].([]any) {
		out = append(out, l.(map[string]any))
	}
	return out
}

func (p propView) addLink(l map[string]any) { p["links"] = append(p["links"].([]any), l) }

func (p propView) link(id string) map[string]any {
	for _, l := range p.links() {
		if l["id"] == id {
			return l
		}
	}
	return nil
}

// propPR is what the generator draws about the PR before the items.
type propPR struct {
	repo, id, node string
	number         int
}

func propViewGen() *rapid.Generator[propView] {
	return rapid.Custom(func(t *rapid.T) propView {
		number := rapid.SampledFrom([]int{1, 10, 100, 301}).Draw(t, "number")
		repo := "acme/api"
		id := fmt.Sprintf("%s#%d", repo, number)
		node := ""
		if rapid.Bool().Draw(t, "has node id") {
			node = fmt.Sprintf("PR_node_%d", number)
		}
		head := rapid.SampledFrom([]string{"aaaaaaa1", "bbbbbbb2", "ccccccc3"}).Draw(t, "head")
		state := rapid.SampledFrom([]string{"open", "open", "open", "closed", "merged"}).Draw(t, "state")
		snap := map[string]any{
			"id": id, "repo": repo, "number": number, "title": "Cache the lookups",
			"state":  map[string]string{"open": "open", "closed": "closed", "merged": "closed"}[state],
			"branch": "feature/cache", "base": "main", "author": "teammate",
			"draft":  rapid.Bool().Draw(t, "draft"),
			"merged": state == "merged", "head_sha": head, "base_sha": "b45e000",
			"mergeable":       rapid.SampledFrom([]string{"MERGEABLE", "CONFLICTING", "UNKNOWN"}).Draw(t, "mergeable"),
			"checks_rollup":   rapid.SampledFrom([]string{"SUCCESS", "FAILURE", "PENDING", "none"}).Draw(t, "rollup"),
			"review_decision": rapid.SampledFrom([]string{"APPROVED", "REVIEW_REQUIRED", "CHANGES_REQUESTED", ""}).Draw(t, "decision"),
		}
		if node != "" {
			snap["node_id"] = node
		}
		var dispositions []any
		for i := 0; i < rapid.IntRange(0, 3).Draw(t, "comments"); i++ {
			d := map[string]any{
				"comment_id": fmt.Sprintf("c-%d", 100+i),
				"computed":   rapid.SampledFrom([]string{"open", "addressed"}).Draw(t, "disposition"),
				"override":   nil,
			}
			dispositions = append(dispositions, d)
		}
		runs := []any{}
		if rapid.Bool().Draw(t, "has failing ci") {
			runs = append(runs, map[string]any{"id": "900", "attempt": 1, "name": "unit", "status": "completed", "conclusion": "failure"})
		} else if rapid.Bool().Draw(t, "has green ci") {
			runs = append(runs, map[string]any{"id": "901", "attempt": 1, "name": "unit", "status": "completed", "conclusion": "success"})
		}
		p := propView{
			"contract": view.Contract, "type": "pr", "id": id, "version": 7,
			"as_of": "2026-10-01T12:00:00Z", "stale": false,
			"snapshot": snap,
			"decorations": map[string]any{
				"relationship": rapid.SampledFrom([]string{"mine", "co-owned", "team"}).Draw(t, "relationship"),
				"dispositions": dispositions, "urgency": "", "category": "",
			},
			"annotations": map[string]any{
				"focus_selected": nil, "hidden": map[string]any{"value": false, "reason": nil},
				"wip": false, "suppress": []any{}, "force_review": false, "force_review_sha": nil, "decider": map[string]any{},
			},
			"links": []any{},
			"ci":    map[string]any{"runs": runs},
		}
		pr := propPR{repo: repo, id: id, node: node, number: number}
		genItems(t, p, pr, head)
		return p
	})
}

// genItems links work items to the view, in the shapes the tracker may hold:
// keyed (id form or node_id form) or keyless (legacy), at the current head or an
// older one, in any state.
func genItems(t *rapid.T, p propView, pr propPR, head string) {
	n := 0
	next := func() string { n++; return "wb-" + strconv.Itoa(n) }
	state := func(label string) string {
		return rapid.SampledFrom([]string{"open", "open", "in_progress", "blocked", "deferred", "closed"}).Draw(t, label)
	}
	ident := func(label string) string {
		if pr.node != "" && rapid.Bool().Draw(t, label) {
			return pr.node
		}
		return pr.id
	}
	key := func(kind workitem.Kind, suffix string, keyed bool, label string) map[string]string {
		md := map[string]string{"repo": pr.repo, "pr_number": strconv.Itoa(pr.number)}
		if keyed {
			md["dedup_key"] = "pr:" + ident(label) + ":" + string(kind) + suffix
		}
		return md
	}
	link := func(title, st string, labels []string, md map[string]string) map[string]any {
		l := map[string]any{"type": "issue", "id": next(), "relation": "work", "state": st, "title": title, "metadata": md}
		if labels != nil {
			l["labels"] = labels
		}
		if st == "in_progress" {
			l["assignee"] = "reviewer"
		}
		return l
	}

	if rapid.Bool().Draw(t, "has anchor") {
		keyed := rapid.Bool().Draw(t, "anchor keyed")
		md := key(workitem.KindAnchor, "", keyed, "anchor ident")
		md["state"] = rapid.SampledFrom([]string{"open", "closed"}).Draw(t, "anchor state md")
		p.addLink(link(pr.id+": Cache the lookups", state("anchor state"), nil, md))
	}
	for i := 0; i < rapid.IntRange(0, 3).Draw(t, "review items"); i++ {
		itemHead := rapid.SampledFrom([]string{head, "aaaaaaa1", "bbbbbbb2", "ddddddd4"}).Draw(t, "review head")
		shape := rapid.SampledFrom([]string{"suffixed", "legacy-keyed", "keyless"}).Draw(t, "review shape")
		md := key(workitem.KindReviewPR, ":"+itemHead, shape == "suffixed", "review ident")
		if shape == "legacy-keyed" {
			md = key(workitem.KindReviewPR, "", true, "review ident")
		}
		md["head_sha"] = itemHead
		title := "review-pr: " + pr.id
		if shape == "suffixed" {
			title = workitem.TitleWithHead(workitem.KindReviewPR, pr.repo, pr.number, itemHead)
		}
		p.addLink(link(title, state("review state"), nil, md))
	}
	if rapid.Bool().Draw(t, "has fix-ci item") {
		h := rapid.SampledFrom([]string{head, "aaaaaaa1"}).Draw(t, "fix-ci head")
		md := key(workitem.KindFixCI, ":"+h, true, "fix-ci ident")
		md["head_sha"] = h
		p.addLink(link(workitem.TitleWithHead(workitem.KindFixCI, pr.repo, pr.number, h), state("fix-ci state"), nil, md))
	}
}

// ---- the model of what an apply does ---------------------------------------------

// modelApply applies a plan to the view the way the apply step and the store's
// refresh leave it: a created item is linked, an update merges its fields, a
// reopen and a close move the state, an annotate sets or clears the annotation.
func modelApply(t *rapid.T, p propView, plan action.PlanResult, next *int) {
	for _, a := range plan.Actions {
		f := a.Fields
		switch a.Op {
		case action.OpCreate:
			*next++
			md := map[string]string{}
			for k, v := range f.Metadata {
				md[k] = v
			}
			l := map[string]any{"type": "issue", "id": fmt.Sprintf("new-%d", *next), "relation": "work", "state": "open", "title": f.Title, "metadata": md}
			if len(f.Labels) > 0 {
				l["labels"] = append([]string(nil), f.Labels...)
			}
			p.addLink(l)
		case action.OpUpdate, action.OpReopen, action.OpClose:
			if a.Target == nil {
				t.Fatalf("%s %s has no target", a.Op, a.Kind)
			}
			l := p.link(*a.Target)
			if l == nil {
				t.Fatalf("%s %s targets %s, which is not linked", a.Op, a.Kind, *a.Target)
			}
			switch a.Op {
			case action.OpReopen:
				l["state"] = "open"
				delete(l, "assignee")
			case action.OpClose:
				l["state"] = "closed"
			default:
				if f.Title != "" {
					l["title"] = f.Title
				}
				if f.Status != "" {
					l["state"] = f.Status
				}
				md, _ := l["metadata"].(map[string]string)
				if md == nil {
					md = map[string]string{}
					l["metadata"] = md
				}
				for k, v := range f.Metadata {
					md[k] = v
				}
				l["labels"] = relabel(l["labels"], f.AddLabels, f.RemoveLabels)
			}
		case action.OpAnnotate:
			modelAnnotate(t, p, a)
		}
	}
}

func relabel(cur any, add, remove []string) []string {
	set := map[string]bool{}
	if ls, ok := cur.([]string); ok {
		for _, l := range ls {
			set[l] = true
		}
	}
	for _, l := range add {
		set[l] = true
	}
	for _, l := range remove {
		delete(set, l)
	}
	var out []string
	for l := range set {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

func modelAnnotate(t *rapid.T, p propView, a action.Action) {
	if a.Target == nil {
		t.Fatalf("annotate has no target")
	}
	ann := p.obj("annotations")
	key := *a.Target
	switch {
	case key == "ready_to_land":
		ann["ready_to_land"] = a.Fields.Value != nil && *a.Fields.Value == "true"
	case key == "force_review":
		if !a.Fields.Clear {
			t.Fatalf("the model knows only the clear of force_review: %+v", a)
		}
		ann["force_review"], ann["force_review_sha"] = false, nil
	case strings.HasPrefix(key, "decider."):
		parts := strings.SplitN(key, ".", 3) // decider.<name>.<key>
		if len(parts) != 3 {
			t.Fatalf("decider annotation key %q", key)
		}
		dec := ann["decider"].(map[string]any)
		ns, _ := dec[parts[1]].(map[string]any)
		if ns == nil {
			ns = map[string]any{}
			dec[parts[1]] = ns
		}
		if a.Fields.Clear {
			delete(ns, parts[2])
		} else if a.Fields.Value != nil {
			ns[parts[2]] = *a.Fields.Value
		}
	default:
		t.Fatalf("the model does not know the annotation %q", key)
	}
}

// summarize names the plan's actions for a failure message.
func summarize(plan action.PlanResult) string {
	var out []string
	for _, a := range plan.Actions {
		target := ""
		if a.Target != nil {
			target = " " + *a.Target
		}
		out = append(out, fmt.Sprintf("%s %s%s [%s]", a.Op, a.Kind, target, a.Rule))
	}
	return strings.Join(out, "; ")
}

func TestPropertyReplanAfterApplyIsEmptyOverGeneratedViews(t *testing.T) {
	planned := 0
	rapid.Check(t, func(t *rapid.T) {
		p := propViewGen().Draw(t, "view")
		first := suiteDecide(p.parse(t))
		next := 0
		modelApply(t, p, first, &next)
		second := suiteDecide(p.parse(t))
		if len(second.Actions) != 0 {
			t.Fatalf("the first plan [%s] left a view whose plan is [%s]\nview after the apply:\n%s", summarize(first), summarize(second), dump(p))
		}
		if len(first.Actions) > 0 {
			planned++
		}
	})
	// A property over views whose first plan is always empty would prove nothing.
	if planned == 0 {
		t.Fatal("no generated view made a plan with an action: the property was vacuous")
	}
}

// Create then plan is empty (pg2-vxlzo2): a PR with a node_id whose first plan
// CREATES work items must, once that plan is applied, plan nothing at all in one
// pass: no adoption.node-id rewrite is left for a later event, so a PR that gets
// no further event holds no residual.
func TestCreateThenPlanIsEmptyForNodeIDViews(t *testing.T) {
	created := 0
	rapid.Check(t, func(t *rapid.T) {
		p := propViewGen().Draw(t, "view")
		if id, _ := p.obj("snapshot")["node_id"].(string); id == "" {
			t.Skip("no node_id")
		}
		first := suiteDecide(p.parse(t))
		hasCreate := false
		for _, a := range first.Actions {
			if a.Op == action.OpCreate {
				hasCreate = true
			}
		}
		if !hasCreate {
			t.Skip("no create planned")
		}
		next := 0
		modelApply(t, p, first, &next)
		if second := suiteDecide(p.parse(t)); len(second.Actions) != 0 {
			t.Fatalf("create then plan is not empty: first [%s], then [%s]\n%s", summarize(first), summarize(second), dump(p))
		}
		created++
	})
	if created == 0 {
		t.Fatal("no generated node_id view planned a create: the test was vacuous")
	}
}

// A plain negative case: a plan that is NOT applied (the model skips the create)
// leaves the same plan behind, so the property above does depend on the model
// applying what the plan says.
func TestPropertyNegativeAnUnappliedPlanIsPlannedAgain(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := propViewGen().Draw(t, "view")
		first := suiteDecide(p.parse(t))
		if len(first.Actions) == 0 {
			t.Skip("nothing planned")
		}
		again := suiteDecide(p.parse(t))
		if len(again.Actions) != len(first.Actions) {
			t.Fatalf("an unapplied plan changed: [%s] then [%s]", summarize(first), summarize(again))
		}
	})
}

func dump(p propView) string {
	raw, _ := json.MarshalIndent(p, "", " ")
	return string(raw)
}

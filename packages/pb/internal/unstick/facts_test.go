package unstick

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func factsWorld() *Graph {
	main := Row{
		ID: "f-main", Title: "Main bead", Description: "desc", Status: StatusBlocked,
		Priority: 2, IssueType: "task", Labels: []string{"x", "y"}, Notes: "n",
		AcceptanceCriteria: "ac", Assignee: "", CreatedAt: "2026-10-01T00:00:00Z",
		UpdatedAt: "2026-10-02T00:00:00Z",
		Comments:  []Comment{{ID: "1", IssueID: "f-main", Author: "bot", Text: "hi", CreatedAt: "2026-10-02T00:00:00Z"}},
		Dependencies: []Dep{
			{IssueID: "f-main", DependsOnID: "f-blk", Type: DepBlocks, Metadata: `{"k":"v"}`},
			{IssueID: "f-main", DependsOnID: "f-ghost", Type: DepBlocks},
			{IssueID: "f-main", DependsOnID: "f-par", Type: DepParentChild},
			{IssueID: "f-main", DependsOnID: "f-rel", Type: "related"},
		},
	}
	kid := Row{ID: "f-kid", Title: "Kid", Status: StatusOpen, Dependencies: []Dep{{IssueID: "f-kid", DependsOnID: "f-main", Type: DepParentChild}}}
	dep := Row{ID: "f-dep", Title: "Dependent", Status: StatusOpen, Dependencies: []Dep{{IssueID: "f-dep", DependsOnID: "f-main", Type: DepBlocks}}}
	blk := Row{ID: "f-blk", Title: "Blocker", Status: StatusClosed, Labels: []string{"l"}, DeferUntil: "2026-12-01T00:00:00Z", ClosedAt: "2026-10-03T00:00:00Z"}
	par := Row{ID: "f-par", Title: "Parent", Status: StatusOpen}
	return NewGraph([]Row{main, kid, dep, blk, par})
}

func TestFactsProjectionAndRefs(t *testing.T) {
	g := factsWorld()
	facts := Facts([]string{"f-main"}, g)
	if len(facts) != 1 {
		t.Fatalf("got %d facts", len(facts))
	}
	f := facts[0]
	if f.ID != "f-main" || f.Priority != 2 || !reflect.DeepEqual(f.Labels, []string{"x", "y"}) || len(f.Comments) != 1 {
		t.Errorf("row fields not projected: %+v", f)
	}
	wantBlk := []Ref{
		{ID: "f-blk", Title: "Blocker", Status: StatusClosed, Labels: []string{"l"}, DeferUntil: "2026-12-01T00:00:00Z", ClosedAt: "2026-10-03T00:00:00Z"},
		{ID: "f-ghost", Status: StatusUnknown, Labels: []string{}},
	}
	if !reflect.DeepEqual(f.Blockers, wantBlk) {
		t.Errorf("blockers %+v want %+v", f.Blockers, wantBlk)
	}
	if len(f.BlockedDependents) != 1 || f.BlockedDependents[0].ID != "f-dep" {
		t.Errorf("dependents %+v", f.BlockedDependents)
	}
	if f.Parent == nil || f.Parent.ID != "f-par" || f.Parent.Status != StatusOpen {
		t.Errorf("parent %+v", f.Parent)
	}
	if len(f.Children) != 1 || f.Children[0].ID != "f-kid" {
		t.Errorf("children %+v", f.Children)
	}
}

func TestFactsUnknownParentAndDefaults(t *testing.T) {
	g := NewGraph([]Row{{ID: "d1", Dependencies: []Dep{{IssueID: "d1", DependsOnID: "nope", Type: DepParentChild}}}})
	f := Facts([]string{"d1"}, g)[0]
	if f.Parent == nil || f.Parent.Status != StatusUnknown {
		t.Errorf("absent parent must be UNKNOWN ref: %+v", f.Parent)
	}
	raw, err := MarshalFacts([]Fact{f})
	if err != nil {
		t.Fatal(err)
	}
	var m []map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"labels", "comments", "blockers", "blocked_dependents", "children", "notes", "defer_until", "assignee"} {
		v, ok := m[0][k]
		if !ok || v == nil {
			t.Errorf("default for %q must be present and non-null, got %v", k, v)
		}
	}
	if arr, _ := m[0]["labels"].([]any); len(arr) != 0 {
		t.Error("labels default must be empty array")
	}
}

func TestFactsNoParentIsNull(t *testing.T) {
	f := Facts([]string{"f-par"}, factsWorld())[0]
	raw, _ := MarshalFacts([]Fact{f})
	var m []map[string]any
	_ = json.Unmarshal(raw, &m)
	if v, ok := m[0]["parent"]; !ok || v != nil {
		t.Errorf("parent must be present and null, got %v (present=%v)", v, ok)
	}
}

func TestFactsExcludesDependenciesAndMetadata(t *testing.T) {
	raw, err := MarshalFacts(Facts([]string{"f-main", "f-kid"}, factsWorld()))
	if err != nil {
		t.Fatal(err)
	}
	var m []map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, o := range m {
		for _, banned := range []string{"dependencies", "metadata", "_type"} {
			if _, has := o[banned]; has {
				t.Errorf("%s must not be projected", banned)
			}
		}
	}
	if strings.Contains(string(raw), `"k":"v"`) || strings.Contains(string(raw), "related") {
		t.Error("raw dependency metadata leaked")
	}
}

func TestFactsSortedAndSkipsMissing(t *testing.T) {
	g := factsWorld()
	got := Facts([]string{"f-par", "nope", "f-blk", "f-main"}, g)
	var ids []string
	for _, f := range got {
		ids = append(ids, f.ID)
	}
	if !reflect.DeepEqual(ids, []string{"f-blk", "f-main", "f-par"}) {
		t.Errorf("ids %v", ids)
	}
	if m := MissingIDs([]string{"z", "nope", "f-par"}, g); !reflect.DeepEqual(m, []string{"nope", "z"}) {
		t.Errorf("missing %v", m)
	}
}

func TestFactsGolden(t *testing.T) {
	raw, err := MarshalFacts(Facts([]string{"f-main", "f-kid", "f-blk"}, factsWorld()))
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "cluster/facts.golden.txt", raw)
}

func TestWriteBatchesDeterministic(t *testing.T) {
	g := factsWorld()
	batches := [][]string{{"f-par", "f-main"}, {"f-kid"}}
	read := func(w Workdir) map[string]string {
		out := map[string]string{}
		for _, rel := range []string{"batches/B01", "batches/B02", "facts/B01.json", "facts/B02.json"} {
			b, err := os.ReadFile(w.Join(rel))
			if err != nil {
				t.Fatal(err)
			}
			out[rel] = string(b)
		}
		return out
	}
	var runs []map[string]string
	for i := 0; i < 2; i++ {
		w, err := CreateWorkdir(t.TempDir()+"/w", fixedNow)
		if err != nil {
			t.Fatal(err)
		}
		names, err := WriteBatches(w, batches, g)
		if err != nil || !reflect.DeepEqual(names, []string{"B01", "B02"}) {
			t.Fatalf("names %v err %v", names, err)
		}
		runs = append(runs, read(w))
	}
	if !reflect.DeepEqual(runs[0], runs[1]) {
		t.Error("output not byte-identical across runs")
	}
	if runs[0]["batches/B01"] != "f-main\nf-par\n" {
		t.Errorf("batch file %q", runs[0]["batches/B01"])
	}
}

func TestWriteBatchRejectsBadInput(t *testing.T) {
	g := factsWorld()
	w, err := CreateWorkdir(t.TempDir()+"/w", fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteBatch(w, "../x", []string{"f-par"}, g); err == nil {
		t.Error("path in name must be rejected")
	}
	if err := WriteBatch(w, "FOLLOWUPS", []string{"f-par", "ghost"}, g); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("missing id must be rejected, got %v", err)
	}
	if err := WriteBatch(w, "FOLLOWUPS", []string{"f-par"}, g); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.Join(FactsDir, "FOLLOWUPS.json")); err != nil {
		t.Error(err)
	}
}

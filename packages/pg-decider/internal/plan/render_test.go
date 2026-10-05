package plan

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/view"
)

func str(s string) *string { return &s }

func loadView(t *testing.T, name string) *view.View {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	v, err := view.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func sampleResult() action.PlanResult {
	return action.PlanResult{
		Actions: []action.Action{
			{
				Op: action.OpReopen, Kind: "review-pr", Target: str("bd-2"), Rule: "review.head-advanced",
				Facts: map[string]any{"head_sha": "9f3c1e2", "reviewed_head": "4b7a0d1"},
			},
			{
				Op: action.OpUpdate, Kind: "process-feedback", Target: str("bd-3"), Rule: "feedback.digest-changed",
				Facts: map[string]any{"unaddressed": []string{"c-881", "c-902"}},
			},
			{
				Op: action.OpCreate, Kind: "fix-ci", Rule: "fixci.failing-on-head",
				Facts: map[string]any{"check": "unit"},
			},
			{Op: action.OpAnnotate, Target: str("ready_to_land"), Rule: "land.ready"},
		},
		Skipped: []action.Skip{
			{Rule: "conflict.present", Reason: action.ReasonNotMatched, Facts: map[string]any{"mergeable": "MERGEABLE"}},
			{Rule: "fixci.failing-on-head", Reason: action.ReasonSuppressed, Facts: map[string]any{"kind": "fix-ci"}},
			{Rule: "all.closed", Reason: action.ReasonNotMatched},
		},
	}
}

func TestTextMatchesGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := Text(&buf, loadView(t, "pr_view.json"), sampleResult()); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "plan.golden.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != string(want) {
		t.Fatalf("text plan differs from golden.\n--- got ---\n%s\n--- want ---\n%s", buf.String(), want)
	}
}

func TestTextEmptyPlanHasHeaderAndEmptyBlocks(t *testing.T) {
	var buf bytes.Buffer
	if err := Text(&buf, loadView(t, "pr_view.json"), action.PlanResult{}); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "plan_empty.golden.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != string(want) {
		t.Fatalf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestHeaderVariants(t *testing.T) {
	v := loadView(t, "pr_view.json")
	v.Snapshot.Draft = true
	v.Snapshot.Mergeable = "CONFLICTING"
	v.Snapshot.ChecksRollup = ""
	v.Snapshot.HeadSHA = ""
	got := Header(v)
	want := "pr acme/widgets#42  co-owned  open  draft  head=-  ci=unknown  conflict=yes"
	if got != want {
		t.Fatalf("header = %q, want %q", got, want)
	}
}

func TestJSONIsExactlyActionsAndSkipped(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, sampleResult()); err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(buf.Bytes(), &top); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, 2)
	for k := range top {
		keys = append(keys, k)
	}
	if len(keys) != 2 || top["actions"] == nil || top["skipped"] == nil {
		t.Fatalf("top-level members = %v", keys)
	}
	var skipped []map[string]any
	if err := json.Unmarshal(top["skipped"], &skipped); err != nil {
		t.Fatal(err)
	}
	if skipped[0]["rule"] != "conflict.present" || skipped[0]["reason"] != "not matched" {
		t.Fatalf("skipped[0] = %v", skipped[0])
	}
}

func TestJSONEmptyPlanIsNonNilArrays(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, action.PlanResult{}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"actions": []any{}, "skipped": []any{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if !strings.Contains(buf.String(), `"actions": []`) {
		t.Fatalf("expected indented empty arrays: %q", buf.String())
	}
}

func TestFactSummaryIsSortedAndStable(t *testing.T) {
	got := FactSummary(map[string]any{"b": 2, "a": "x", "list": []string{"p", "q"}, "ptr": nil})
	want := "a=x b=2 list=[p, q] ptr=null"
	if got != want {
		t.Fatalf("FactSummary = %q, want %q", got, want)
	}
	if FactSummary(nil) != "" {
		t.Fatal("no facts, no summary")
	}
}

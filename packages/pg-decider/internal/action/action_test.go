package action

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestReasonConstantsAreTheFiveExactStrings(t *testing.T) {
	got := []string{ReasonHidden, ReasonSuppressed, ReasonAlreadyHandled, ReasonReviewPending, ReasonNotMatched}
	want := []string{"hidden", "suppressed", "already handled", "review-pending", "not matched"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reason constants = %v, want %v", got, want)
	}
}

func TestOpConstants(t *testing.T) {
	got := []Op{OpCreate, OpUpdate, OpReopen, OpClose, OpAnnotate}
	want := []Op{"create", "update", "reopen", "close", "annotate"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("op constants = %v, want %v", got, want)
	}
	if AnchorParent != "$anchor" {
		t.Fatalf("AnchorParent = %q", AnchorParent)
	}
}

func TestEmptyPlanResultMarshalsEmptyArraysNotNull(t *testing.T) {
	b, err := json.Marshal(PlanResult{})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"actions":[],"skipped":[]}` {
		t.Fatalf("empty PlanResult JSON = %s", b)
	}
}

func TestCreateActionHasNullTarget(t *testing.T) {
	a := Action{Op: OpCreate, Kind: "fix-ci", Fields: Fields{Title: "t", Parent: AnchorParent, Labels: []string{"x"}}, Rule: "r"}
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if v, ok := m["target"]; !ok || v != nil {
		t.Fatalf("create action target = %v (present=%v), want explicit null", v, ok)
	}
	if m["op"] != "create" || m["kind"] != "fix-ci" || m["rule"] != "r" {
		t.Fatalf("unexpected JSON: %s", b)
	}
	f := m["fields"].(map[string]any)
	if f["parent"] != "$anchor" || f["title"] != "t" {
		t.Fatalf("fields JSON: %v", f)
	}
	if _, ok := m["requires_prior"]; ok {
		t.Fatalf("requires_prior must be omitted when false: %s", b)
	}
}

func TestAnnotateActionRoundTrip(t *testing.T) {
	key := "pr.last_seq"
	val := "9"
	in := PlanResult{
		Actions: []Action{
			{
				Op: OpAnnotate, Target: &key, Fields: Fields{Value: &val}, Rule: "r", RequiresPrior: true,
				Facts: map[string]any{"n": float64(1)},
			},
			{Op: OpAnnotate, Target: &key, Fields: Fields{Clear: true}, Rule: "r"},
		},
		Skipped: []Skip{{Rule: "r2", Reason: ReasonHidden, Facts: map[string]any{"why": "x"}}},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out PlanResult
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip mismatch:\n in=%+v\nout=%+v\njson=%s", in, out, b)
	}
}

func TestPlanResultJSONKeys(t *testing.T) {
	b, _ := json.Marshal(PlanResult{Skipped: []Skip{{Rule: "r", Reason: ReasonNotMatched}}})
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["actions"]; !ok {
		t.Fatalf("missing actions key: %s", b)
	}
	var sk []map[string]any
	if err := json.Unmarshal(m["skipped"], &sk); err != nil || sk[0]["rule"] != "r" || sk[0]["reason"] != "not matched" {
		t.Fatalf("skipped JSON: %s (%v)", m["skipped"], err)
	}
}

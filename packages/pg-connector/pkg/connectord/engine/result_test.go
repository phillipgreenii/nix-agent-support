package engine

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
)

// The kinds below are invented: the shared engine takes its kind order and its
// roles as data, so a registration of made-up names exercises every path.
const (
	kindAlpha   = "alpha"
	kindBeta    = "beta"
	kindGamma   = "gamma"
	kindDelta   = "delta"
	kindEnter   = "enter_set"
	kindLeave   = "leave_set"
	kindRemoved = "gone"
)

var order = []string{kindAlpha, kindBeta, kindGamma, kindDelta, kindRemoved, kindEnter, kindLeave}

var roles = registry.Roles{Added: "first", Removed: kindRemoved, EnteredQuery: kindEnter, LeftQuery: kindLeave}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func f(name, before, after string) Field {
	var b, a []byte
	if before != "" {
		b = []byte(before)
	}
	if after != "" {
		a = []byte(after)
	}
	return Field{Name: name, Before: b, After: a}
}

func fieldNames(r Result) []string {
	var out []string
	for _, fl := range r.Fields {
		out = append(out, fl.Name)
	}
	return out
}

func wantResult(t *testing.T, got Result, kinds, names []string) {
	t.Helper()
	if !slices.Equal(got.Kinds, kinds) {
		t.Errorf("kinds = %v, want %v (fields %v)", got.Kinds, kinds, fieldNames(got))
	}
	if gn := fieldNames(got); !slices.Equal(gn, names) {
		t.Errorf("field names = %v, want %v", gn, names)
	}
}

func TestFieldJSONNamesAndNullSides(t *testing.T) {
	fl := Field{Name: "title", Before: raw(`"a"`)}
	b, err := json.Marshal(fl)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"name":"title","before":"a","after":null}` {
		t.Fatalf("Field JSON = %s", b)
	}
	var back Field
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if string(back.Before) != `"a"` || back.After != nil {
		t.Fatalf("round trip = %+v: a null side must be the empty RawMessage", back)
	}
}

func TestEncodeDecodeFields(t *testing.T) {
	if got, err := EncodeFields(nil); err != nil || string(got) != "[]" {
		t.Fatalf("EncodeFields(nil) = %s, %v; an empty list encodes as []", got, err)
	}
	in := []Field{
		{Name: "a", Before: raw(`1`), After: raw(`2`)},
		{Name: "b", After: raw(` {"x": 1} `)},
		{Name: "c", Before: raw(`"gone"`)},
	}
	enc, err := EncodeFields(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeFields(enc)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || out[0].Name != "a" || string(out[1].After) != `{"x":1}` || out[1].Before != nil || out[2].After != nil {
		t.Fatalf("decoded = %+v", out)
	}
	if out, err := DecodeFields(raw(`[]`)); err != nil || out == nil || len(out) != 0 {
		t.Fatalf("DecodeFields([]) = %v, %v", out, err)
	}
	for _, empty := range []json.RawMessage{nil, raw(``), raw(`null`)} {
		if out, err := DecodeFields(empty); err != nil || len(out) != 0 {
			t.Errorf("DecodeFields(%q) = %v, %v", empty, out, err)
		}
	}
	if _, err := DecodeFields(raw(`{"not":"a list"}`)); err == nil {
		t.Error("DecodeFields accepted an object")
	}
	if _, err := EncodeFields([]Field{{Name: "bad", After: raw(`{nope`)}}); err == nil {
		t.Error("EncodeFields accepted a value that is not JSON")
	}
}

func TestNormalizeDropsUnchangedAndSortsByName(t *testing.T) {
	got := Normalize([]Field{f("b", `1`, `2`), f("a", `{"x": 1}`, `{ "x":1 }`), f("c", "", `3`)})
	if names := fieldNames(Result{Fields: got}); !slices.Equal(names, []string{"b", "c"}) {
		t.Fatalf("Normalize kept %v, want [b c]", names)
	}
}

func TestResultEmpty(t *testing.T) {
	if !(Result{}).Empty() {
		t.Error("zero Result is not Empty")
	}
	if (Result{Kinds: []string{kindBeta}}).Empty() {
		t.Error("a Result with a kind is Empty")
	}
	if (Result{Fields: []Field{{Name: "x"}}}).Empty() {
		t.Error("a Result with a field is Empty")
	}
}

func TestOrderKinds(t *testing.T) {
	got := OrderKinds(order, []string{kindLeave, kindBeta, "zzz", kindBeta, kindAlpha, "aaa"})
	want := []string{kindAlpha, kindBeta, kindLeave, "aaa", "zzz"}
	if !slices.Equal(got, want) {
		t.Fatalf("OrderKinds = %v, want %v", got, want)
	}
	if OrderKinds(order, nil) != nil {
		t.Error("OrderKinds(nil) is not nil")
	}
	// The order is the argument, not a package constant: a reversed order reverses the result.
	if got := OrderKinds([]string{kindBeta, kindAlpha}, []string{kindAlpha, kindBeta}); !slices.Equal(got, []string{kindBeta, kindAlpha}) {
		t.Fatalf("OrderKinds with a reversed order = %v", got)
	}
}

func TestMergeKeepsEarliestBeforeAndLatestAfter(t *testing.T) {
	earlier := Result{Kinds: []string{kindAlpha}, Fields: []Field{f("f1", `"a"`, `"b"`), f("title", `"t1"`, `"t2"`)}}
	later := Result{Kinds: []string{kindAlpha}, Fields: []Field{f("f1", `"b"`, `"c"`), f("flag", `false`, `true`)}}
	got := Merge(order, earlier, later)
	want := Result{
		Kinds:  []string{kindAlpha},
		Fields: []Field{f("f1", `"a"`, `"c"`), f("flag", `false`, `true`), f("title", `"t1"`, `"t2"`)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Merge = %+v\nwant   %+v", got, want)
	}
}

func TestMergeDropsAFieldChangedAndChangedBack(t *testing.T) {
	earlier := Result{Kinds: []string{kindBeta}, Fields: []Field{f("flag", `false`, `true`)}}
	later := Result{Kinds: []string{kindBeta}, Fields: []Field{f("flag", `true`, `false`)}}
	got := Merge(order, earlier, later)
	if len(got.Fields) != 0 {
		t.Fatalf("the field that came back survived: %+v", got.Fields)
	}
	// Equal by compacted JSON, not by spelling.
	earlier = Result{Fields: []Field{f("x", `{"a": 1}`, `2`)}}
	later = Result{Fields: []Field{f("x", `2`, `{ "a":1 }`)}}
	if got := Merge(order, earlier, later); len(got.Fields) != 0 {
		t.Fatalf("whitespace kept a field: %+v", got.Fields)
	}
	// A value present then absent is a change, not equality.
	earlier = Result{Fields: []Field{f("x", "", `1`)}}
	later = Result{Fields: []Field{f("x", `1`, "")}}
	if got := Merge(order, earlier, later); len(got.Fields) != 0 {
		t.Fatalf("absent -> 1 -> absent is no change: %+v", got.Fields)
	}
}

func TestMergeUnionsKindsInTheGivenOrder(t *testing.T) {
	earlier := Result{Kinds: []string{kindGamma, kindBeta}}
	later := Result{Kinds: []string{kindAlpha, kindBeta, kindLeave}}
	want := []string{kindAlpha, kindBeta, kindGamma, kindLeave}
	if got := Merge(order, earlier, later); !slices.Equal(got.Kinds, want) {
		t.Fatalf("kinds = %v, want %v", got.Kinds, want)
	}
	// The order is a parameter: another registration orders the same kinds differently.
	reversed := []string{kindLeave, kindGamma, kindBeta, kindAlpha}
	if got := Merge(reversed, earlier, later); !slices.Equal(got.Kinds, reversed) {
		t.Fatalf("kinds under a reversed order = %v, want %v", got.Kinds, reversed)
	}
	if got := Merge(order, Result{}, Result{}); !got.Empty() {
		t.Fatalf("Merge of empties = %+v", got)
	}
}

func TestMergeWithEmptyIsTheIdentity(t *testing.T) {
	r := Result{Kinds: []string{kindDelta}, Fields: []Field{f("a", `1`, `2`), f("b", "", `3`)}}
	if got := Merge(order, Result{}, r); !reflect.DeepEqual(got, r) {
		t.Errorf("Merge(empty, r) = %+v", got)
	}
	if got := Merge(order, r, Result{}); !reflect.DeepEqual(got, r) {
		t.Errorf("Merge(r, empty) = %+v", got)
	}
}

// Three results of one entity chain: each later before is the earlier after.
func TestMergeIsAssociativeOnThreeResults(t *testing.T) {
	a := Result{Kinds: []string{kindAlpha, kindGamma}, Fields: []Field{
		f("f1", `"h1"`, `"h2"`), f("flag", `false`, `true`), f("title", `"t"`, `"u"`), f("run:1", "", `{"s":1}`),
	}}
	b := Result{Kinds: []string{kindBeta}, Fields: []Field{
		f("f1", `"h2"`, `"h3"`), f("flag", `true`, `false`), f("run:1", `{"s":1}`, `{"s":2}`),
	}}
	c := Result{Kinds: []string{kindDelta}, Fields: []Field{
		f("f1", `"h3"`, `"h1"`), f("flag", `false`, `true`), f("run:1", `{"s":2}`, ""),
	}}
	left := Merge(order, Merge(order, a, b), c)
	right := Merge(order, a, Merge(order, b, c))
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("(a.b).c = %+v\na.(b.c) = %+v", left, right)
	}
	// f1 went h1 -> h1 (dropped), flag false -> true, run:1 absent -> absent.
	if got, want := fieldNames(left), []string{"flag", "title"}; !slices.Equal(got, want) {
		t.Fatalf("merged fields = %v, want %v", got, want)
	}
	if want := []string{kindAlpha, kindBeta, kindGamma, kindDelta}; !slices.Equal(left.Kinds, want) {
		t.Fatalf("kinds = %v, want %v in the given order", left.Kinds, want)
	}
}

func TestMembershipResultTakesItsKindsFromTheRoles(t *testing.T) {
	in := MembershipResult(roles, kindEnter, "mine")
	wantResult(t, in, []string{kindEnter}, []string{QueryFieldPrefix + "mine"})
	if in.Fields[0].Before != nil || string(in.Fields[0].After) != `"mine"` {
		t.Errorf("arrival = %+v", in.Fields[0])
	}
	out := MembershipResult(roles, kindLeave, "mine")
	wantResult(t, out, []string{kindLeave}, []string{QueryFieldPrefix + "mine"})
	if string(out.Fields[0].Before) != `"mine"` || out.Fields[0].After != nil {
		t.Errorf("departure = %+v", out.Fields[0])
	}
	if res := MembershipResult(roles, kindBeta, "mine"); !res.Empty() {
		t.Errorf("a non-membership kind produced %+v", res)
	}
	if res := MembershipResult(registry.Roles{}, "", "mine"); !res.Empty() {
		t.Errorf("an empty kind under empty roles produced %+v", res)
	}
	// The roles decide: another registration's names work the same way.
	other := registry.Roles{EnteredQuery: "joined", LeftQuery: "parted"}
	wantResult(t, MembershipResult(other, "joined", "q"), []string{"joined"}, []string{QueryFieldPrefix + "q"})
	if res := MembershipResult(other, kindEnter, "q"); !res.Empty() {
		t.Errorf("a kind that is not in these roles produced %+v", res)
	}
}

func TestRemovedResultCollapsesDuplicateQueries(t *testing.T) {
	rm := RemovedResult(kindRemoved, []string{"b", "a", "b"})
	wantResult(t, rm, []string{kindRemoved}, []string{QueryFieldPrefix + "a", QueryFieldPrefix + "b"})
	for _, fl := range rm.Fields {
		if fl.After != nil || fl.Before == nil {
			t.Errorf("removed field %+v: Before must be set, After empty", fl)
		}
	}
	wantResult(t, RemovedResult(kindRemoved, nil), []string{kindRemoved}, nil)
	wantResult(t, RemovedResult("other_gone", []string{"q"}), []string{"other_gone"}, []string{QueryFieldPrefix + "q"})
}

func TestMergedMembershipRowsKeepOneFieldPerQuery(t *testing.T) {
	// Two queries never collapse in a merge by field name.
	both := Merge(order, MembershipResult(roles, kindEnter, "q1"), MembershipResult(roles, kindEnter, "q2"))
	wantResult(t, both, []string{kindEnter}, []string{QueryFieldPrefix + "q1", QueryFieldPrefix + "q2"})
	// An arrival and a departure for one query that merge cancel out the field.
	cancel := Merge(order, MembershipResult(roles, kindEnter, "q1"), MembershipResult(roles, kindLeave, "q1"))
	wantResult(t, cancel, []string{kindEnter, kindLeave}, nil)
}

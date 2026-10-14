package classify

import (
	"reflect"
	"slices"
	"testing"
)

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

func TestMergeKeepsEarliestBeforeAndLatestAfter(t *testing.T) {
	earlier := Result{Kinds: []string{KindHeadChanged}, Fields: []Field{f("head_sha", `"a"`, `"b"`), f("title", `"t1"`, `"t2"`)}}
	later := Result{Kinds: []string{KindHeadChanged}, Fields: []Field{f("head_sha", `"b"`, `"c"`), f("draft", `false`, `true`)}}
	got := Merge(earlier, later)
	want := Result{
		Kinds:  []string{KindHeadChanged},
		Fields: []Field{f("draft", `false`, `true`), f("head_sha", `"a"`, `"c"`), f("title", `"t1"`, `"t2"`)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Merge = %+v\nwant   %+v", got, want)
	}
}

func TestMergeDropsAFieldChangedAndChangedBack(t *testing.T) {
	earlier := Result{Kinds: []string{KindDraftChanged}, Fields: []Field{f("draft", `false`, `true`)}}
	later := Result{Kinds: []string{KindDraftChanged}, Fields: []Field{f("draft", `true`, `false`)}}
	got := Merge(earlier, later)
	if len(got.Fields) != 0 {
		t.Fatalf("the field that came back survived: %+v", got.Fields)
	}
	// Equal by compacted JSON, not by spelling.
	earlier = Result{Fields: []Field{f("x", `{"a": 1}`, `2`)}}
	later = Result{Fields: []Field{f("x", `2`, `{ "a":1 }`)}}
	if got := Merge(earlier, later); len(got.Fields) != 0 {
		t.Fatalf("whitespace kept a field: %+v", got.Fields)
	}
	// A value present then absent is a change, not equality.
	earlier = Result{Fields: []Field{f("x", "", `1`)}}
	later = Result{Fields: []Field{f("x", `1`, "")}}
	if got := Merge(earlier, later); len(got.Fields) != 0 {
		t.Fatalf("absent -> 1 -> absent is no change: %+v", got.Fields)
	}
}

func TestMergeUnionsKindsInCanonicalOrder(t *testing.T) {
	earlier := Result{Kinds: []string{KindRenamed, KindClosed}}
	later := Result{Kinds: []string{KindReopened, KindClosed, KindLeftQuery}}
	want := []string{KindReopened, KindClosed, KindRenamed, KindLeftQuery}
	if got := Merge(earlier, later); !slices.Equal(got.Kinds, want) {
		t.Fatalf("kinds = %v, want %v", got.Kinds, want)
	}
	if got := Merge(Result{}, Result{}); !got.Empty() {
		t.Fatalf("Merge of empties = %+v", got)
	}
}

func TestMergeWithEmptyIsTheIdentity(t *testing.T) {
	r := Result{Kinds: []string{KindMerged}, Fields: []Field{f("a", `1`, `2`), f("b", "", `3`)}}
	if got := Merge(Result{}, r); !reflect.DeepEqual(got, r) {
		t.Errorf("Merge(empty, r) = %+v", got)
	}
	if got := Merge(r, Result{}); !reflect.DeepEqual(got, r) {
		t.Errorf("Merge(r, empty) = %+v", got)
	}
}

// Three results of one entity chain: each later before is the earlier after.
func TestMergeIsAssociativeOnThreeResults(t *testing.T) {
	a := Result{Kinds: []string{KindHeadChanged, KindCIChanged}, Fields: []Field{
		f("head_sha", `"h1"`, `"h2"`), f("draft", `false`, `true`), f("title", `"t"`, `"u"`), f("run:1", "", `{"s":1}`),
	}}
	b := Result{Kinds: []string{KindDraftChanged}, Fields: []Field{
		f("head_sha", `"h2"`, `"h3"`), f("draft", `true`, `false`), f("run:1", `{"s":1}`, `{"s":2}`),
	}}
	c := Result{Kinds: []string{KindClosed}, Fields: []Field{
		f("head_sha", `"h3"`, `"h1"`), f("draft", `false`, `true`), f("run:1", `{"s":2}`, ""),
	}}
	left := Merge(Merge(a, b), c)
	right := Merge(a, Merge(b, c))
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("(a.b).c = %+v\na.(b.c) = %+v", left, right)
	}
	// head_sha went h1 -> h1 (dropped), draft false -> true, run:1 absent -> absent.
	wantFields := []string{"draft", "title"}
	if got := fieldNames(left); !slices.Equal(got, wantFields) {
		t.Fatalf("merged fields = %v, want %v", got, wantFields)
	}
	if want := []string{KindClosed, KindHeadChanged, KindDraftChanged, KindCIChanged}; !slices.Equal(left.Kinds, []string{KindClosed, KindDraftChanged, KindHeadChanged, KindCIChanged}) {
		t.Fatalf("kinds = %v, want %v in canonical order", left.Kinds, want)
	}
}

func TestMembershipAndRemovedQueryFieldConvention(t *testing.T) {
	in := Membership(KindEnteredQuery, "mine")
	wantResult(t, in, []string{KindEnteredQuery}, []string{"query:mine"})
	if in.Fields[0].Before != nil || string(in.Fields[0].After) != `"mine"` {
		t.Errorf("entered_query = %+v", in.Fields[0])
	}
	out := Membership(KindLeftQuery, "mine")
	wantResult(t, out, []string{KindLeftQuery}, []string{"query:mine"})
	if string(out.Fields[0].Before) != `"mine"` || out.Fields[0].After != nil {
		t.Errorf("left_query = %+v", out.Fields[0])
	}
	if res := Membership(KindClosed, "mine"); !res.Empty() {
		t.Errorf("a non-membership kind produced %+v", res)
	}

	rm := Removed([]string{"b", "a", "b"})
	wantResult(t, rm, []string{KindRemoved}, []string{"query:a", "query:b"})
	for _, fl := range rm.Fields {
		if fl.After != nil || fl.Before == nil {
			t.Errorf("removed field %+v: Before must be set, After empty", fl)
		}
	}
	wantResult(t, Removed(nil), []string{KindRemoved}, nil)

	// Two queries never collapse in a merge by field name.
	both := Merge(Membership(KindEnteredQuery, "q1"), Membership(KindEnteredQuery, "q2"))
	wantResult(t, both, []string{KindEnteredQuery}, []string{"query:q1", "query:q2"})
	// An entered row and a left row for one query that merge cancel out the field.
	cancel := Merge(Membership(KindEnteredQuery, "q1"), Membership(KindLeftQuery, "q1"))
	wantResult(t, cancel, []string{KindEnteredQuery, KindLeftQuery}, nil)
}

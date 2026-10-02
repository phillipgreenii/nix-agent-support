package classify

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// fakeClassifier returns a fixed record list and counts invocations.
type fakeClassifier struct {
	records []Record
	calls   int
}

func (f *fakeClassifier) Classify(old, new Snapshot) []Record {
	f.calls++
	out := make([]Record, len(f.records))
	copy(out, f.records)
	return out
}

// registerFake registers a fake classifier under a name unique to the test
// (never pr, issue or thread: sibling packets register those from init()).
func registerFake(t *testing.T, records ...Kind) (string, *fakeClassifier) {
	t.Helper()
	name := "fake_" + strings.ReplaceAll(t.Name(), "/", "_")
	f := &fakeClassifier{}
	for _, k := range records {
		f.records = append(f.records, Record{Kind: k})
	}
	Register(name, f)
	return name, f
}

func observed(typ, payload string) Snapshot {
	return Snapshot{Type: typ, ID: "1", Exists: true, Active: true, Payload: json.RawMessage(payload)}
}

func kindsOf(rs []Record) []Kind {
	var out []Kind
	for _, r := range rs {
		out = append(out, r.Kind)
	}
	return out
}

func TestClassifyIdenticalSnapshotsIsEmpty(t *testing.T) {
	name, f := registerFake(t, KindHeadChanged)
	s := observed(name, `{"a":1}`)
	if got := Classify(s, s); len(got) != 0 {
		t.Fatalf("Classify(s, s) = %v, want empty", got)
	}
	if f.calls != 0 {
		t.Fatalf("per-type classifier called %d times for identical snapshots, want 0", f.calls)
	}
}

func TestIdenticalIgnoresAsOfContentHashHeadSHA(t *testing.T) {
	name, f := registerFake(t, KindHeadChanged)
	a := observed(name, `{"a":1}`)
	b := a
	b.AsOf, b.ContentHash, b.HeadSHA = "2026-01-01T00:00:00Z", "h2", "sha2"
	if got := Classify(a, b); len(got) != 0 {
		t.Fatalf("differing AsOf/ContentHash/HeadSHA alone yielded %v, want empty", got)
	}
	if f.calls != 0 {
		t.Fatalf("per-type classifier called %d times, want 0", f.calls)
	}
}

func TestDifferingDecorationsAreNotIdentical(t *testing.T) {
	name, f := registerFake(t, KindLinkChanged)
	a := observed(name, `{"a":1}`)
	b := a
	b.Decorations = json.RawMessage(`{"d":1}`)
	got := Classify(a, b)
	if !reflect.DeepEqual(kindsOf(got), []Kind{KindLinkChanged}) || f.calls != 1 {
		t.Fatalf("got %v calls=%d, want [link_changed] with 1 call", got, f.calls)
	}
}

func TestFirstObservationYieldsOnlyReconcile(t *testing.T) {
	name, f := registerFake(t, KindOpened, KindHeadChanged)
	want := []Kind{KindReconcile}

	t.Run("no previous snapshot", func(t *testing.T) {
		old := Snapshot{Type: name, ID: "1"} // Exists=false
		got := Classify(old, observed(name, `{"a":1}`))
		if !reflect.DeepEqual(kindsOf(got), want) {
			t.Fatalf("got %v, want %v", kindsOf(got), want)
		}
	})
	t.Run("previous snapshot inactive", func(t *testing.T) {
		old := observed(name, `{"a":0}`)
		old.Active = false
		got := Classify(old, observed(name, `{"a":1}`))
		if !reflect.DeepEqual(kindsOf(got), want) {
			t.Fatalf("got %v, want %v", kindsOf(got), want)
		}
	})
	t.Run("unobserved rule runs before identical rule", func(t *testing.T) {
		new := observed(name, `{"a":1}`)
		old := new
		old.Exists = false
		got := Classify(old, new)
		if !reflect.DeepEqual(kindsOf(got), want) {
			t.Fatalf("got %v, want exactly [reconcile]", kindsOf(got))
		}
		old = new
		old.Active = false
		got = Classify(old, new)
		if !reflect.DeepEqual(kindsOf(got), want) {
			t.Fatalf("inactive+equal: got %v, want exactly [reconcile]", kindsOf(got))
		}
	})
	if f.calls != 0 {
		t.Fatalf("per-type classifier called %d times on first observation, want 0", f.calls)
	}
}

func TestKindsArePureFunctionOfOldAndNew(t *testing.T) {
	name, _ := registerFake(t, KindReviewChanged, KindClosed, KindHeadChanged, KindClosed)
	old := observed(name, `{"a":0}`)
	new := observed(name, `{"a":1}`)
	first := Classify(old, new)
	want := []Kind{KindClosed, KindHeadChanged, KindReviewChanged} // sorted, de-duplicated
	if !reflect.DeepEqual(kindsOf(first), want) {
		t.Fatalf("got %v, want %v", kindsOf(first), want)
	}
	for i := 0; i < 20; i++ {
		if got := Classify(old, new); !reflect.DeepEqual(got, first) {
			t.Fatalf("call %d: got %v, want %v", i, got, first)
		}
	}
}

func TestDegradedBackendNeverYieldsRemovedOrClosed(t *testing.T) {
	name, _ := registerFake(t, KindRemoved, KindClosed, KindHeadChanged)
	old := observed(name, `{"a":0}`)
	new := observed(name, `{"a":1}`)

	got := Classify(old, new)
	if !reflect.DeepEqual(kindsOf(got), []Kind{KindClosed, KindHeadChanged, KindRemoved}) {
		t.Fatalf("non-degraded control: got %v", kindsOf(got))
	}

	new.Degraded = true
	got = Classify(old, new)
	if !reflect.DeepEqual(kindsOf(got), []Kind{KindHeadChanged}) {
		t.Fatalf("degraded: got %v, want [head_changed]", kindsOf(got))
	}
}

func TestUnregisteredTypeYieldsNothing(t *testing.T) {
	old := observed("no_such_type", `{"a":0}`)
	new := observed("no_such_type", `{"a":1}`)
	if got := Classify(old, new); len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
	if _, ok := Lookup("no_such_type"); ok {
		t.Fatal("Lookup reported an unregistered type")
	}
}

func TestRegisterPanicsOnDuplicate(t *testing.T) {
	name, f := registerFake(t)
	if c, ok := Lookup(name); !ok || c != Classifier(f) {
		t.Fatalf("Lookup(%q) = %v, %v", name, c, ok)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate Register did not panic")
		}
	}()
	Register(name, &fakeClassifier{})
}

func TestClassifyIgnoresNewExistsAndActive(t *testing.T) {
	name, _ := registerFake(t, KindHeadChanged)
	old := observed(name, `{"a":0}`)
	new := observed(name, `{"a":1}`)
	new.Exists, new.Active = false, false
	if got := Classify(old, new); !reflect.DeepEqual(kindsOf(got), []Kind{KindHeadChanged}) {
		t.Fatalf("got %v, want [head_changed]", kindsOf(got))
	}
}

func TestKindCatalogueValues(t *testing.T) {
	want := map[Kind]string{
		KindAdded: "added", KindRemoved: "removed", KindReconcile: "reconcile",
		KindAnnotationChanged: "annotation_changed", KindLinkChanged: "link_changed",
		KindOpened: "opened", KindReopened: "reopened", KindClosed: "closed", KindMerged: "merged",
		KindDraftChanged: "draft_changed", KindHeadChanged: "head_changed", KindBaseChanged: "base_changed",
		KindCiChanged: "ci_changed", KindMergeabilityChanged: "mergeability_changed",
		KindReviewChanged: "review_changed", KindFeedbackChanged: "feedback_changed",
		KindWorkChanged: "work_changed", KindStatusChanged: "status_changed",
		KindAssigneeChanged: "assignee_changed", KindCommentsChanged: "comments_changed",
		KindDepsChanged: "deps_changed", KindMessageAdded: "message_added", KindResolved: "resolved",
	}
	if len(want) != 23 {
		t.Fatalf("catalogue has %d entries, want 23", len(want))
	}
	for k, s := range want {
		if string(k) != s {
			t.Errorf("%q != %q", k, s)
		}
	}
}

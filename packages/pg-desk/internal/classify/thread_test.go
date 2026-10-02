package classify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// threadFixture loads testdata/thread_<name>.json as an observed, active
// thread snapshot.
func threadFixture(t *testing.T, name string) Snapshot {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "thread_"+name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return Snapshot{Type: "thread", ID: "1700000000.000100", Exists: true, Active: true, Payload: json.RawMessage(b)}
}

// threadFacts builds an observed thread snapshot from raw thread_show fields.
func threadFacts(replyCount string, lastReplyAt string) Snapshot {
	show := fmt.Sprintf(`{"id":"t1","channel":"C-sample","reply_count":%s,"last_reply_at":%q}`, replyCount, lastReplyAt)
	return Snapshot{
		Type: "thread", ID: "t1", Exists: true, Active: true,
		Payload: json.RawMessage(`{"thread_show":` + show + `}`),
	}
}

func TestThreadClassifierIsRegistered(t *testing.T) {
	if _, ok := Lookup("thread"); !ok {
		t.Fatal(`Lookup("thread") not registered`)
	}
}

func TestThreadClassifyMessageAdded(t *testing.T) {
	base := func(t *testing.T) Snapshot { return threadFixture(t, "base") }
	cases := []struct {
		name string
		old  func(t *testing.T) Snapshot
		new  func(t *testing.T) Snapshot
		want []Kind
	}{
		{"reply count and last reply moved forward", base, func(t *testing.T) Snapshot { return threadFixture(t, "reply_added") }, []Kind{KindMessageAdded}},
		{"participants only change", base, func(t *testing.T) Snapshot { return threadFixture(t, "participants_only") }, nil},
		{"reply count shrank (partial read)", base, func(t *testing.T) Snapshot { return threadFixture(t, "shrunk") }, nil},
		{"rfc3339 last reply moved forward", func(t *testing.T) Snapshot { return threadFixture(t, "rfc3339_base") }, func(t *testing.T) Snapshot { return threadFixture(t, "rfc3339_later") }, []Kind{KindMessageAdded}},
		{"count grew, no last reply", func(*testing.T) Snapshot { return threadFacts("1", "") }, func(*testing.T) Snapshot { return threadFacts("2", "") }, []Kind{KindMessageAdded}},
		{"count equal, slack ts moved forward", func(*testing.T) Snapshot { return threadFacts("2", "1700000100.000200") }, func(*testing.T) Snapshot { return threadFacts("2", "1700000100.000300") }, []Kind{KindMessageAdded}},
		{"count equal, slack ts same", func(*testing.T) Snapshot { return threadFacts("2", "1700000100.000200") }, func(*testing.T) Snapshot { return threadFacts("2", "1700000100.0002") }, nil},
		{"count equal, epoch seconds moved forward", func(*testing.T) Snapshot { return threadFacts("2", "1700000100") }, func(*testing.T) Snapshot { return threadFacts("2", "1700000101") }, []Kind{KindMessageAdded}},
		{"count equal, rfc3339 moved forward", func(*testing.T) Snapshot { return threadFacts("2", "2026-01-01T00:00:00Z") }, func(*testing.T) Snapshot { return threadFacts("2", "2026-01-01T00:00:01Z") }, []Kind{KindMessageAdded}},
		{"count equal, mixed forms compared as times", func(*testing.T) Snapshot { return threadFacts("2", "1767225600.000000") }, func(*testing.T) Snapshot { return threadFacts("2", "2026-01-01T00:00:01Z") }, []Kind{KindMessageAdded}},
		{"count equal, mixed forms same instant", func(*testing.T) Snapshot { return threadFacts("2", "1767225600.000000") }, func(*testing.T) Snapshot { return threadFacts("2", "2026-01-01T00:00:00Z") }, nil},
		{"string order would mislead: 9 < 10 as integers", func(*testing.T) Snapshot { return threadFacts("9", "") }, func(*testing.T) Snapshot { return threadFacts("10", "") }, []Kind{KindMessageAdded}},
		{"last reply moved backward", func(*testing.T) Snapshot { return threadFacts("2", "1700000200.000000") }, func(*testing.T) Snapshot { return threadFacts("2", "1700000100.000000") }, nil},
		{"last reply forward but count shrank", func(*testing.T) Snapshot { return threadFacts("3", "1700000100.000000") }, func(*testing.T) Snapshot { return threadFacts("2", "1700000200.000000") }, nil},
		{"unparseable last reply is no signal", func(*testing.T) Snapshot { return threadFacts("2", "not-a-time") }, func(*testing.T) Snapshot { return threadFacts("2", "still-not-a-time") }, nil},
		{"unparseable new last reply, count unchanged", func(*testing.T) Snapshot { return threadFacts("2", "1700000100.000000") }, func(*testing.T) Snapshot { return threadFacts("2", "garbage") }, nil},
		{"unparseable last reply, count grew still counts", func(*testing.T) Snapshot { return threadFacts("2", "garbage") }, func(*testing.T) Snapshot { return threadFacts("3", "garbage") }, []Kind{KindMessageAdded}},
		{"first reply: empty last reply to a value", func(*testing.T) Snapshot { return threadFacts("0", "") }, func(*testing.T) Snapshot { return threadFacts("1", "1700000100.000000") }, []Kind{KindMessageAdded}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old, new := tc.old(t), tc.new(t)
			got := kindsOf(Classify(old, new))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Classify = %v, want %v", got, tc.want)
			}
		})
	}
}

// reply_count absent from the payload is unknown, not zero.
func TestThreadClassifyMissingReplyCountIsNoCountSignal(t *testing.T) {
	old := Snapshot{Type: "thread", ID: "t1", Exists: true, Active: true, Payload: json.RawMessage(`{"thread_show":{"id":"t1"}}`)}
	new := threadFacts("2", "")
	if got := Classify(old, new); len(got) != 0 {
		t.Fatalf("Classify = %v, want none (old count unknown)", got)
	}
}

func TestThreadClassifyNeverEmitsOtherKinds(t *testing.T) {
	pairs := [][2]Snapshot{
		{threadFacts("1", "1700000100.000000"), threadFacts("5", "1700009999.000000")},
		{threadFacts("5", "1700009999.000000"), threadFacts("1", "1700000100.000000")},
		{threadFacts("1", ""), threadFacts("1", "garbage")},
	}
	for _, p := range pairs {
		for _, r := range Classify(p[0], p[1]) {
			if r.Kind != KindMessageAdded {
				t.Fatalf("emitted %q, want only message_added", r.Kind)
			}
		}
	}
}

func TestThreadClassifyIdenticalSnapshotsIsEmpty(t *testing.T) {
	s := threadFixture(t, "base")
	if got := Classify(s, s); len(got) != 0 {
		t.Fatalf("Classify(s, s) = %v, want empty", got)
	}
	// Direct classifier call on equal content is also empty.
	c, _ := Lookup("thread")
	if got := c.Classify(s, s); len(got) != 0 {
		t.Fatalf("thread classifier on equal snapshots = %v, want empty", got)
	}
}

func TestThreadFirstObservationYieldsOnlyReconcile(t *testing.T) {
	newSnap := threadFixture(t, "reply_added")
	first := Snapshot{Type: "thread", ID: newSnap.ID} // Exists=false
	if got := kindsOf(Classify(first, newSnap)); !reflect.DeepEqual(got, []Kind{KindReconcile}) {
		t.Fatalf("first observation = %v, want [reconcile]", got)
	}
	inactive := threadFixture(t, "base")
	inactive.Active = false
	if got := kindsOf(Classify(inactive, newSnap)); !reflect.DeepEqual(got, []Kind{KindReconcile}) {
		t.Fatalf("return after removed = %v, want [reconcile]", got)
	}
}

func TestThreadKindsArePureFunctionOfOldAndNew(t *testing.T) {
	old, new := threadFixture(t, "base"), threadFixture(t, "reply_added")
	want := kindsOf(Classify(old, new))
	if !reflect.DeepEqual(want, []Kind{KindMessageAdded}) {
		t.Fatalf("baseline = %v, want [message_added]", want)
	}
	for i := 0; i < 5; i++ {
		if got := kindsOf(Classify(old, new)); !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d = %v, want %v", i, got, want)
		}
	}
	// Informational fields do not influence the result.
	o2, n2 := old, new
	o2.AsOf, o2.ContentHash, o2.HeadSHA = "x", "y", "z"
	n2.AsOf, n2.ContentHash, n2.HeadSHA = "p", "q", "r"
	if got := kindsOf(Classify(o2, n2)); !reflect.DeepEqual(got, want) {
		t.Fatalf("with differing informational fields = %v, want %v", got, want)
	}
	// Inputs are not mutated.
	if string(old.Payload) != string(threadFixture(t, "base").Payload) {
		t.Fatal("old payload mutated")
	}
}

func TestThreadDegradedBackendNeverYieldsRemovedOrClosed(t *testing.T) {
	old, grown := threadFixture(t, "base"), threadFixture(t, "reply_added")
	for _, tc := range []struct {
		name     string
		old, new Snapshot
	}{
		{"degraded new, reply growth", old, withDegraded(grown)},
		{"degraded old, reply growth", withDegraded(old), grown},
		{"degraded new, shrunk", old, withDegraded(threadFixture(t, "shrunk"))},
		{"degraded new, empty payload", old, withDegraded(withPayload(grown, ""))},
		{"empty payload new", old, withPayload(grown, "")},
		{"empty payload old", withPayload(old, ""), grown},
		{"null payload new", old, withPayload(grown, "null")},
		{"empty object new", old, withPayload(grown, "{}")},
		{"malformed payload new", old, withPayload(grown, "{not json")},
		{"missing thread_show new", old, withPayload(grown, `{"thread_show":null}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, r := range Classify(tc.old, tc.new) {
				switch r.Kind {
				case KindRemoved, KindClosed, KindMessageAdded:
					t.Fatalf("emitted %q, want none of removed/closed/message_added", r.Kind)
				}
			}
		})
	}
}

func withDegraded(s Snapshot) Snapshot { s.Degraded = true; return s }

func withPayload(s Snapshot, payload string) Snapshot {
	s.Payload = json.RawMessage(payload)
	return s
}

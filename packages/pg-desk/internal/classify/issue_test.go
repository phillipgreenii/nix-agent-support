package classify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// issueFixture loads testdata/issue_<name>.json (a marshaled
// gather.IssueFacts) as an observed, active issue snapshot.
func issueFixture(t *testing.T, name string) Snapshot {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "issue_"+name+".json"))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	if !json.Valid(b) {
		t.Fatalf("fixture %s is not valid JSON", name)
	}
	return Snapshot{Type: "issue", ID: "iss-1", Exists: true, Active: true, Payload: json.RawMessage(b)}
}

func issueKinds(old, new Snapshot) []Kind {
	return kindsOf(Classify(old, new))
}

// The issue signal-to-kind mapping, pinned by one row per kind below:
//   - opened:  state moves from another non-terminal state to "open".
//   - closed / reopened: state crosses into / out of a terminal state
//     (closed, done, resolved, cancelled, canceled, wontfix).
//   - status_changed: the state string differs between two known states.
//   - assignee_changed: the assignee differs.
//   - deps_changed: the set of (id, type) edges in issue_show.deps differs, or
//     the id set of issue_deps differs when BOTH snapshots carry it.
//   - comments_changed: schema.Issue carries no comment list, so updated_at
//     advancing while every other issue_show field is unchanged is the proxy.
func TestIssueClassifyKinds(t *testing.T) {
	cases := []struct {
		name     string
		old, new string
		want     []Kind
	}{
		{"opened", "deferred", "open", []Kind{KindOpened, KindStatusChanged}},
		{"reopened", "closed", "open", []Kind{KindReopened, KindStatusChanged}},
		{"closed", "open", "closed", []Kind{KindClosed, KindStatusChanged}},
		{"status_changed", "open", "in_progress", []Kind{KindStatusChanged}},
		{"assignee_changed", "open", "assigned", []Kind{KindAssigneeChanged}},
		{"comments_changed", "open", "touched", []Kind{KindCommentsChanged}},
		{"deps_changed", "open", "dep_added", []Kind{KindDepsChanged}},
		{"deps_changed via issue_deps", "with_deps", "with_more_deps", []Kind{KindDepsChanged}},
		{"labels and priority only", "open", "relabeled", []Kind{KindFieldsChanged}},
		{"no change", "open", "open", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := issueKinds(issueFixture(t, tc.old), issueFixture(t, tc.new))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("kinds = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIssueNeverEmitsLinkChanged(t *testing.T) {
	names := []string{"open", "in_progress", "deferred", "closed", "assigned", "touched", "relabeled", "dep_added", "with_deps", "with_more_deps", "no_show"}
	for _, a := range names {
		for _, b := range names {
			for _, k := range issueKinds(issueFixture(t, a), issueFixture(t, b)) {
				if k == KindLinkChanged || k == KindAnnotationChanged {
					t.Fatalf("%s -> %s emitted %s", a, b, k)
				}
			}
		}
	}
}

func TestIssueClassifyIdenticalSnapshotsIsEmpty(t *testing.T) {
	for _, n := range []string{"open", "closed", "dep_added", "with_deps", "no_show"} {
		s := issueFixture(t, n)
		if got := Classify(s, s); len(got) != 0 {
			t.Fatalf("Classify(%s, %s) = %v, want empty", n, n, got)
		}
	}
}

func TestIssueFirstObservationYieldsAdded(t *testing.T) {
	for _, n := range []string{"open", "closed", "dep_added", "no_show"} {
		fresh := issueFixture(t, n)
		none := Snapshot{Type: "issue", ID: "iss-1"} // Exists=false
		if got := kindsOf(Classify(none, fresh)); !reflect.DeepEqual(got, []Kind{KindAdded}) {
			t.Fatalf("first observation of %s = %v, want [added]", n, got)
		}
		inactive := issueFixture(t, "closed")
		inactive.Active = false
		if got := kindsOf(Classify(inactive, fresh)); !reflect.DeepEqual(got, []Kind{KindAdded}) {
			t.Fatalf("return after removal (%s) = %v, want [added]", n, got)
		}
	}
}

func TestIssueKindsArePureFunctionOfOldAndNew(t *testing.T) {
	names := []string{"open", "in_progress", "closed", "assigned", "touched", "dep_added", "with_deps", "with_more_deps"}
	for _, a := range names {
		for _, b := range names {
			o, n := issueFixture(t, a), issueFixture(t, b)
			first := issueKinds(o, n)
			for i := 0; i < 3; i++ {
				if again := issueKinds(o, n); !reflect.DeepEqual(first, again) {
					t.Fatalf("%s -> %s not deterministic: %v vs %v", a, b, first, again)
				}
			}
			// Informational fields must not influence the result.
			n2 := n
			n2.AsOf, n2.ContentHash, n2.HeadSHA = "2030-01-01T00:00:00Z", "other", "sha"
			if again := issueKinds(o, n2); !reflect.DeepEqual(first, again) {
				t.Fatalf("%s -> %s depends on AsOf/ContentHash/HeadSHA: %v vs %v", a, b, first, again)
			}
		}
	}
}

func TestIssueDegradedBackendNeverYieldsRemovedOrClosed(t *testing.T) {
	old := issueFixture(t, "open")

	// A degraded new snapshot, even one that decodes as closed, yields nothing.
	deg := issueFixture(t, "closed")
	deg.Degraded = true
	if got := issueKinds(old, deg); len(got) != 0 {
		t.Fatalf("degraded closed snapshot yielded %v, want empty", got)
	}

	// An empty payload, an empty facts object and a missing issue_show are not
	// read as a state transition.
	for _, p := range []json.RawMessage{nil, json.RawMessage(`{}`), json.RawMessage(`{"issue_show":null}`), json.RawMessage(`{"issue_show":{}}`)} {
		n := issueFixture(t, "no_show")
		n.Payload = p
		for _, k := range issueKinds(old, n) {
			if k == KindClosed || k == KindRemoved || k == KindStatusChanged || k == KindReopened {
				t.Fatalf("payload %q yielded %v", p, k)
			}
		}
		// ... and the same in the other direction.
		for _, k := range issueKinds(n, old) {
			if k == KindClosed || k == KindRemoved || k == KindStatusChanged || k == KindReopened || k == KindOpened {
				t.Fatalf("old payload %q yielded %v", p, k)
			}
		}
	}
}

func TestIssueAbsentIssueDepsNeverYieldsDepsChangedByItself(t *testing.T) {
	// issue_deps present before, absent now (switch turned off or read failed).
	if got := issueKinds(issueFixture(t, "with_deps"), issueFixture(t, "open")); len(got) != 0 {
		t.Fatalf("issue_deps disappearing yielded %v, want empty", got)
	}
	// ... and appearing.
	if got := issueKinds(issueFixture(t, "open"), issueFixture(t, "with_deps")); len(got) != 0 {
		t.Fatalf("issue_deps appearing yielded %v, want empty", got)
	}
}

// The pipeline hands the classifier a normalised payload (no updated_at) and
// the raw timestamp in Snapshot.Updated; the beads comments proxy reads it
// from there, and an Updated-only difference is not "identical".
func TestIssueCommentsProxyReadsSnapshotUpdated(t *testing.T) {
	payload := json.RawMessage(`{"issue_show":{"id":"iss-1","state":"open","title":"t"}}`)
	snap := func(updated string) Snapshot {
		return Snapshot{Type: "issue", ID: "iss-1", Exists: true, Active: true, Payload: payload, Updated: updated}
	}
	if got := issueKinds(snap("2026-09-30T00:00:00Z"), snap("2026-09-30T05:00:00Z")); !reflect.DeepEqual(got, []Kind{KindCommentsChanged}) {
		t.Errorf("updated advanced alone: kinds = %v, want [comments_changed]", got)
	}
	if got := issueKinds(snap("2026-09-30T05:00:00Z"), snap("2026-09-30T00:00:00Z")); len(got) != 0 {
		t.Errorf("updated moved backwards: kinds = %v, want none", got)
	}
	if got := issueKinds(snap("2026-09-30T00:00:00Z"), snap("2026-09-30T00:00:00Z")); len(got) != 0 {
		t.Errorf("identical snapshots: kinds = %v, want none", got)
	}
	if got := issueKinds(snap(""), snap("")); len(got) != 0 {
		t.Errorf("no timestamps: kinds = %v, want none", got)
	}
}

package classify

import (
	"encoding/json"
	"reflect"
	"testing"
)

// fieldsOf returns the Fields of the single fields_changed record in rs, and
// fails when there is not exactly one.
func fieldsOf(t *testing.T, rs []Record) []string {
	t.Helper()
	var got []string
	n := 0
	for _, r := range rs {
		if r.Kind == KindFieldsChanged {
			n++
			got = r.Fields
		}
	}
	if n != 1 {
		t.Fatalf("records = %+v, want exactly one fields_changed", rs)
	}
	return got
}

// INV-ICD-5 (T-3, classifier level): a labels-only and a priority-only edit
// each yield exactly one fields_changed naming that field, never nothing.
func TestIssueFieldsChanged_LabelsOnlyAndPriorityOnly(t *testing.T) {
	base := issueFixture(t, "open")
	for _, tc := range []struct{ fixture, field string }{
		{"labels_only", "labels"},
		{"priority_only", "priority"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			rs := Classify(base, issueFixture(t, tc.fixture))
			if len(rs) != 1 || rs[0].Kind != KindFieldsChanged {
				t.Fatalf("records = %+v, want exactly one fields_changed", rs)
			}
			if got := fieldsOf(t, rs); !reflect.DeepEqual(got, []string{tc.field}) {
				t.Fatalf("Fields = %v, want [%s]", got, tc.field)
			}
		})
	}
}

// The residual names the fields no EMITTED kind covers: a status change plus a
// labels change gives status_changed and one fields_changed naming labels only.
func TestIssueFieldsChanged_ResidualExcludesCoveredFields(t *testing.T) {
	old := issueFixture(t, "open")
	nw := issueFixture(t, "labels_only")
	show := map[string]any{}
	var facts map[string]json.RawMessage
	_ = json.Unmarshal(nw.Payload, &facts)
	_ = json.Unmarshal(facts["issue_show"], &show)
	show["state"] = "in_progress"
	show["status_category"] = "indeterminate"
	b, _ := json.Marshal(map[string]any{"issue_show": show})
	nw.Payload = b

	rs := Classify(old, nw)
	if got := kindsOf(rs); !reflect.DeepEqual(got, []Kind{KindFieldsChanged, KindStatusChanged}) {
		t.Fatalf("kinds = %v, want [fields_changed status_changed]", got)
	}
	if got := fieldsOf(t, rs); !reflect.DeepEqual(got, []string{"labels"}) {
		t.Fatalf("Fields = %v, want [labels] (state and status_category are covered)", got)
	}
}

// Several residual fields yield ONE record with sorted Fields.
func TestIssueFieldsChanged_OneRecordSortedFields(t *testing.T) {
	rs := Classify(issueFixture(t, "open"), issueFixture(t, "relabeled"))
	if len(rs) != 1 {
		t.Fatalf("records = %+v, want one", rs)
	}
	if got := fieldsOf(t, rs); !reflect.DeepEqual(got, []string{"labels", "priority"}) {
		t.Fatalf("Fields = %v, want [labels priority]", got)
	}
}

// A matched specific kind is not additionally given fields_changed, and no
// emitted kind ever collapses into fields_changed.
func TestIssueFieldsChanged_NotAddedToSpecificKinds(t *testing.T) {
	cases := []struct {
		old, new string
		want     []Kind
	}{
		{"deferred", "open", []Kind{KindOpened, KindStatusChanged}},
		{"closed", "open", []Kind{KindReopened, KindStatusChanged}},
		{"open", "closed", []Kind{KindClosed, KindStatusChanged}},
		{"open", "in_progress", []Kind{KindStatusChanged}},
		{"open", "assigned", []Kind{KindAssigneeChanged}},
		{"open", "touched", []Kind{KindCommentsChanged}},
		{"open", "dep_added", []Kind{KindDepsChanged}},
		{"with_deps", "with_more_deps", []Kind{KindDepsChanged}},
	}
	for _, tc := range cases {
		got := issueKinds(issueFixture(t, tc.old), issueFixture(t, tc.new))
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s -> %s: kinds = %v, want exactly %v", tc.old, tc.new, got, tc.want)
		}
	}
}

// A volatile-only difference (as_of, updated_at, served_from, entity_meta, ...)
// is never a residual field.
func TestIssueFieldsChanged_VolatileOnlyDifferenceYieldsNothing(t *testing.T) {
	old := issueFixture(t, "open")
	nw := issueFixture(t, "open")
	nw.Payload = json.RawMessage(`{"issue_show":{"id":"iss-1","title":"Synthetic issue","state":"open",` +
		`"url":"https://tracker.example.test/iss-1","priority":"P2","labels":["alpha"],"issue_type":"task",` +
		`"assignee":"","owner":"teammate","as_of":"2030-01-01T00:00:00Z","stale":true,"updated_at":"2026-10-01T10:00:00Z",` +
		`"served_from":"cache","age_seconds":9,"entity_meta":{"watched":true}}}`)
	if got := Classify(old, nw); len(got) != 0 {
		t.Fatalf("records = %+v, want none", got)
	}
}

// INV-ICD-6: the comments proxy is kept for beads and retired for an owning
// backend; with the normalised payloads equal, an owning backend yields no
// record at all and a beads one yields comments_changed from Snapshot.Updated.
func TestIssueCommentsProxy_OwningBackendRetiresIt(t *testing.T) {
	old := issueFixture(t, "open")
	nw := issueFixture(t, "open") // normalised payloads equal: updated_at is not in them
	old.Updated, nw.Updated = "2026-10-01T10:00:00Z", "2026-10-01T14:00:00Z"

	if got := kindsOf(Classify(old, nw)); !reflect.DeepEqual(got, []Kind{KindCommentsChanged}) {
		t.Fatalf("beads updated_at-only advance = %v, want [comments_changed]", got)
	}
	old.OwningBackend, nw.OwningBackend = true, true
	if got := Classify(old, nw); len(got) != 0 {
		t.Fatalf("owning updated_at-only advance = %+v, want none", got)
	}
	// Only the new snapshot's flag decides (the pipeline sets both alike).
	old.OwningBackend = false
	if got := Classify(old, nw); len(got) != 0 {
		t.Fatalf("owning (new snapshot) updated_at-only advance = %+v, want none", got)
	}
}

// Even when the payloads differ in raw volatile keys (an un-normalised
// fixture), the owning backend never emits comments_changed.
func TestIssueCommentsProxy_OwningBackendNeverEmitsCommentsChanged(t *testing.T) {
	old, nw := issueFixture(t, "open"), issueFixture(t, "touched")
	if got := kindsOf(Classify(old, nw)); !reflect.DeepEqual(got, []Kind{KindCommentsChanged}) {
		t.Fatalf("beads = %v, want [comments_changed]", got)
	}
	nw.OwningBackend = true
	if got := Classify(old, nw); len(got) != 0 {
		t.Fatalf("owning = %+v, want none", got)
	}
}

// T-12 (beads, classifier level): a decider write (a label) comes back as one
// content row, and classifying the resulting snapshot against itself yields
// nothing (a second hydration of the unchanged result writes nothing).
func TestIssueDeciderLabelWrite_OneRowThenNone(t *testing.T) {
	before := issueFixture(t, "open")
	after := issueFixture(t, "labels_only") // the decider added a label
	rs := Classify(before, after)
	if len(rs) != 1 || rs[0].Kind != KindFieldsChanged {
		t.Fatalf("decider label write = %+v, want one fields_changed", rs)
	}
	if got := Classify(after, after); len(got) != 0 {
		t.Fatalf("unchanged re-hydration = %+v, want none", got)
	}
}

// KindAdded is declared for the first-observation rewrite of a later phase and
// is emitted by no issue classification today.
func TestIssueNeverEmitsAdded(t *testing.T) {
	names := []string{
		"open", "in_progress", "deferred", "closed", "assigned", "touched", "relabeled",
		"labels_only", "priority_only", "dep_added", "with_deps", "with_more_deps", "no_show",
	}
	for _, a := range names {
		for _, b := range names {
			for _, k := range issueKinds(issueFixture(t, a), issueFixture(t, b)) {
				if k == KindAdded {
					t.Fatalf("%s -> %s emitted added", a, b)
				}
			}
		}
	}
}

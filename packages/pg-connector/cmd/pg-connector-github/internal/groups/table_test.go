package groups

import (
	"reflect"
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

func TestFieldTableCoversSchema(t *testing.T) {
	for _, tc := range []struct {
		kind string
		typ  reflect.Type
	}{
		{KindPR, reflect.TypeOf(schema.PR{})},
		{KindCI, reflect.TypeOf(schema.CIRun{})},
	} {
		unassigned, unknown, duplicated := AuditFields(tc.kind, tc.typ)
		if len(unassigned) > 0 {
			t.Errorf("%s: fields with no group (add them to FieldTable): %v", tc.kind, unassigned)
		}
		if len(unknown) > 0 {
			t.Errorf("%s: FieldTable names fields the type lacks: %v", tc.kind, unknown)
		}
		if len(duplicated) > 0 {
			t.Errorf("%s: fields assigned more than once: %v", tc.kind, duplicated)
		}
	}
}

// A field added to a type without a group fails the audit; a table that names a
// field twice, or a group-assigned field the type lacks, fails it too.
func TestAuditFieldsCatchesDrift(t *testing.T) {
	type copyOfRun struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		URL        string `json:"url"`
		Provider   string `json:"provider"`
		HeadSHA    string `json:"head_sha,omitempty"`
		Repo       string `json:"repo"`
		PRID       string `json:"pr_id"`
		Attempt    int    `json:"attempt,omitempty"`
		Jobs       []int  `json:"jobs,omitempty"`
		AsOf       string `json:"as_of"`
		Stale      bool   `json:"stale"`
		Skipped    string `json:"-"`
		hidden     string
		NewField   string `json:"new_field,omitempty"`
	}
	_ = copyOfRun{}.hidden
	unassigned, unknown, duplicated := AuditFields(KindCI, reflect.TypeOf(copyOfRun{}))
	if !slices.Equal(unassigned, []string{"new_field"}) {
		t.Errorf("unassigned = %v, want [new_field]", unassigned)
	}
	// The copy has no served_from, age_seconds or groups: derived entries MAY
	// name fields the type does not have yet.
	if len(unknown) != 0 || len(duplicated) != 0 {
		t.Errorf("unknown = %v, duplicated = %v, want none", unknown, duplicated)
	}

	type missingJobs struct {
		ID string `json:"id"`
	}
	_, unknown, _ = AuditFields(KindCI, reflect.TypeOf(missingJobs{}))
	if !slices.Contains(unknown, "jobs") || slices.Contains(unknown, "served_from") {
		t.Errorf("unknown = %v, want group-assigned jobs but no derived entry", unknown)
	}

	saved := FieldTable
	t.Cleanup(func() { FieldTable = saved })
	FieldTable = append(slices.Clone(saved), FieldAssignment{KindCI, Detail, []string{"name"}})
	_, _, duplicated = AuditFields(KindCI, reflect.TypeOf(copyOfRun{}))
	if !slices.Equal(duplicated, []string{"name"}) {
		t.Errorf("duplicated = %v, want [name]", duplicated)
	}
}

func TestGroupOf(t *testing.T) {
	for _, tc := range []struct {
		kind, field, group string
		ok                 bool
	}{
		{KindPR, "title", Summary, true},
		{KindPR, "branch", Summary, true},
		{KindPR, "base_sha", Summary, true},
		{KindPR, "additions", Detail, true},
		{KindPR, "review_requests", Detail, true},
		{KindPR, "reviews", Conversation, true},
		{KindPR, "connections", Conversation, true},
		{KindPR, "as_of", "", false},
		{KindPR, "groups", "", false},
		{KindPR, "nope", "", false},
		{KindCI, "jobs", Runs, true},
		{KindCI, "head_sha", Runs, true},
		{KindCI, "stale", "", false},
		{"issue", "title", "", false},
	} {
		g, ok := GroupOf(tc.kind, tc.field)
		if g != tc.group || ok != tc.ok {
			t.Errorf("GroupOf(%s, %s) = %q, %v; want %q, %v", tc.kind, tc.field, g, ok, tc.group, tc.ok)
		}
	}
	if !IsDerived(KindPR, "as_of") || IsDerived(KindPR, "title") || IsDerived(KindPR, "nope") {
		t.Error("IsDerived wrong")
	}
}

func TestAllIsTheClosedSet(t *testing.T) {
	want := []string{"summary", "detail", "conversation", "files", "commits", "pending", "runs"}
	if !slices.Equal(All(), want) {
		t.Errorf("All() = %v, want %v", All(), want)
	}
	// Every group a field is assigned to is in the closed set.
	for _, a := range FieldTable {
		if a.Group != Derived && !slices.Contains(All(), a.Group) {
			t.Errorf("FieldTable names group %q outside the closed set", a.Group)
		}
	}
	for _, a := range ResultTypes {
		if !slices.Contains(All(), a.Group) {
			t.Errorf("ResultTypes names group %q outside the closed set", a.Group)
		}
	}
	// The schema result types the table names exist under those names.
	for _, rt := range []reflect.Type{
		reflect.TypeOf(schema.PRFilesResult{}), reflect.TypeOf(schema.PRFile{}),
		reflect.TypeOf(schema.PRCommitsResult{}), reflect.TypeOf(schema.PRCommit{}),
	} {
		name := "schema." + rt.Name()
		if !slices.ContainsFunc(ResultTypes, func(a ResultTypeAssignment) bool { return a.Type == name }) {
			t.Errorf("ResultTypes lacks %s", name)
		}
	}
}

package groups

import (
	"reflect"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
)

// The kinds the field table covers. A CI run group is keyed by the PR it was
// fetched for, but its fields belong to the CI run schema.
const (
	KindPR = entity.KindPR
	KindCI = "ci"
)

// Derived is the pseudo-group of a field no group stores: it is computed at
// read time from the groups served (as_of, stale and the answer annotations).
const Derived = ""

// FieldAssignment assigns JSON fields of one entity kind's schema type to a
// group (or to Derived).
type FieldAssignment struct {
	Kind   string
	Group  string
	Fields []string
}

// FieldTable is the spec's field-to-group table ("Field groups": every field of
// schema.PR and of the CI run schema is assigned to exactly one group). The
// JSON names are the json tags of the top-level fields of schema.PR (KindPR)
// and schema.CIRun (KindCI). TestFieldTableCoversSchema walks both types, so a
// field added without an assignment fails the build.
var FieldTable = []FieldAssignment{
	// summary: branch is headRefName, base is baseRefName, head_sha is
	// headRefOid, base_sha is baseRefOid, mergeable is the carried-forward value.
	{KindPR, Summary, []string{
		"id", "repo", "number", "title", "state", "branch", "base", "author", "url", "draft",
		"body", "labels", "head_sha", "mergeable", "checks_rollup", "node_id", "updated_at",
		"review_decision", "comment_count", "review_count", "review_thread_count", "label_count",
		"base_sha",
	}},
	{KindPR, Detail, []string{
		"merged", "additions", "deletions", "changed_files", "merge_state_status", "review_requests",
	}},
	{KindPR, Conversation, []string{"comments", "reviews", "connections"}},
	// Derived at read time from the groups served; the last three are the
	// answer annotations and may not exist on the type yet.
	{KindPR, Derived, []string{"as_of", "stale", "served_from", "age_seconds", "groups"}},
	// runs: jobs come from the job table by (run id, attempt) and belong to the
	// group.
	{KindCI, Runs, []string{
		"id", "name", "status", "conclusion", "url", "provider", "head_sha", "repo", "pr_id",
		"attempt", "jobs",
	}},
	{KindCI, Derived, []string{"as_of", "stale", "served_from", "age_seconds", "groups"}},
}

// ResultTypeAssignment assigns every field of a group's own result type to the
// group: a field such as the files result's files list belongs to the group
// that fetches it. Type is the Go type name, package-qualified.
type ResultTypeAssignment struct {
	Type  string
	Group string
}

// ResultTypes lists the result types a group stores whole.
var ResultTypes = []ResultTypeAssignment{
	{"schema.PRFilesResult", Files},
	{"schema.PRFile", Files},
	{"schema.PRCommitsResult", Commits},
	{"schema.PRCommit", Commits},
	// Computed at read time from the stored pending-review data and the live head.
	{"pr.PendingReviewResult", Pending},
}

type fieldKey struct{ kind, field string }

var fieldIndex = func() map[fieldKey]string {
	m := make(map[fieldKey]string)
	for _, a := range FieldTable {
		for _, f := range a.Fields {
			m[fieldKey{a.Kind, f}] = a.Group
		}
	}
	return m
}()

// GroupOf returns the group that stores the JSON field of the entity kind's
// schema type. ok is false for an unknown field and for a derived one (derived
// fields are stored by no group; see IsDerived).
func GroupOf(kind, jsonField string) (group string, ok bool) {
	g, found := fieldIndex[fieldKey{kind, jsonField}]
	if !found || g == Derived {
		return "", false
	}
	return g, true
}

// IsDerived reports whether the field is computed at read time and stored by
// no group.
func IsDerived(kind, jsonField string) bool {
	g, found := fieldIndex[fieldKey{kind, jsonField}]
	return found && g == Derived
}

// JSONFields returns the JSON names of the top-level fields of the struct type
// t (its json tags; a field tagged "-" is skipped, an untagged one uses its Go
// name).
func JSONFields(t reflect.Type) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out = append(out, name)
	}
	return out
}

// AuditFields checks the struct type t against FieldTable for the kind:
// unassigned lists the fields of t no entry names, unknown the group-assigned
// entries (not derived ones, which MAY name fields the type does not have yet)
// that t lacks, and duplicated the field names the table assigns more than
// once. All three are sorted; a clean table gives three nil slices.
func AuditFields(kind string, t reflect.Type) (unassigned, unknown, duplicated []string) {
	have := make(map[string]bool)
	for _, f := range JSONFields(t) {
		have[f] = true
	}
	seen := make(map[string]int)
	for _, a := range FieldTable {
		if a.Kind != kind {
			continue
		}
		for _, f := range a.Fields {
			seen[f]++
			if a.Group != Derived && !have[f] {
				unknown = append(unknown, f)
			}
		}
	}
	for f := range have {
		if seen[f] == 0 {
			unassigned = append(unassigned, f)
		}
	}
	for f, n := range seen {
		if n > 1 {
			duplicated = append(duplicated, f)
		}
	}
	sort.Strings(unassigned)
	sort.Strings(unknown)
	sort.Strings(duplicated)
	return unassigned, unknown, duplicated
}

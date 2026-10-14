package classify

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/conversation"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/detail"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/prfacts"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/runs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// How a field reaches a row:
//
//	derived   it feeds a kind (and is recorded as a field too)
//	recorded  a change to it yields a row with no kind
//	excluded  a change to it alone yields nothing (the documented exclusion list)
type disposition int

const (
	derived disposition = iota + 1
	recorded
	excluded
)

func (d disposition) String() string { return [...]string{"?", "derived", "recorded", "excluded"}[d] }

type policy map[string]disposition

// The explicit lists. Adding a field to one of these types fails
// TestFieldCoverage until the field is classified here (and in doc.go).
var (
	summaryPolicy = policy{
		"NodeID": excluded, "Gone": excluded, "URL": excluded, "UpdatedAt": excluded, "BaseRefOid": excluded,
		"Repo": derived, "Number": derived, "State": derived, "IsDraft": derived, "ReviewDecision": derived,
		"Mergeable": derived, "CommentCount": derived, "ReviewCount": derived, "HeadRefOid": derived,
		"BaseRefName": derived, "ChecksState": derived,
		"Title": recorded, "Body": recorded, "Author": recorded, "LabelCount": recorded, "Labels": recorded,
		"ReviewThreadCount": recorded, "HeadRefName": recorded,
	}
	detailPolicy = policy{
		"MergeStateStatus": excluded,
		"Merged":           derived,
		"ReviewRequests":   recorded, "Additions": recorded, "Deletions": recorded, "ChangedFiles": recorded,
	}
	conversationPolicy = policy{"Comments": derived, "Reviews": derived, "Connections": excluded}
	commentPolicy      = policy{
		"Author": excluded, "Path": excluded, "Line": excluded, "ThreadID": excluded,
		"ReviewThreadID": excluded, "ThreadOutdated": excluded,
		"ID": derived, "Resolved": derived,
		"Body": recorded,
	}
	reviewPolicy = policy{
		"Author": excluded, "SubmittedAt": excluded, "CommitOID": excluded,
		"ID": derived, "State": derived, "Comments": derived,
		"Body": recorded,
	}
	connectionsPolicy = policy{"Reviews": excluded, "Threads": excluded, "Comments": excluded}
	reportPolicy      = policy{"Truncated": excluded, "Total": excluded, "Returned": excluded}
	runPolicy         = policy{
		"URL": excluded, "Provider": excluded, "Repo": excluded, "PRID": excluded, "AsOf": excluded,
		"Stale": excluded, "ServedFrom": excluded, "AgeSeconds": excluded, "Groups": excluded,
		"ID": derived, "Name": derived, "Status": derived, "Conclusion": derived, "HeadSHA": derived,
		"Attempt": derived, "Jobs": derived,
	}
	jobPolicy    = policy{"ID": excluded, "URL": excluded, "Name": derived, "Status": derived, "Conclusion": derived}
	filePolicy   = policy{"Path": recorded, "Additions": recorded, "Deletions": recorded}
	commitPolicy = policy{"SHA": recorded, "Author": recorded, "Message": recorded}
)

func exportedFields(typ reflect.Type) []string {
	var out []string
	for i := range typ.NumField() {
		if typ.Field(i).IsExported() {
			out = append(out, typ.Field(i).Name)
		}
	}
	sort.Strings(out)
	return out
}

// mutate changes v to a different, non-zero value: it is how a probe moves one
// field. A slice gains an element, a pointer is allocated, a struct has its
// first exported field moved.
func mutate(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + "~")
	case reflect.Int, reflect.Int64:
		v.SetInt(v.Int() + 1)
	case reflect.Bool:
		v.SetBool(!v.Bool())
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		mutate(v.Elem())
	case reflect.Slice:
		e := reflect.New(v.Type().Elem()).Elem()
		mutate(e)
		v.Set(reflect.Append(v, e))
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				mutate(v.Field(i))
				return
			}
		}
	default:
		panic(fmt.Sprintf("mutate: unsupported kind %s", v.Kind()))
	}
}

func mutateField(ptr any, name string, overrides map[string]func(reflect.Value)) {
	fv := reflect.ValueOf(ptr).Elem().FieldByName(name)
	if !fv.IsValid() {
		panic("no field " + name)
	}
	if o := overrides[name]; o != nil {
		o(fv)
		return
	}
	mutate(fv)
}

func setString(s string) func(reflect.Value) {
	return func(v reflect.Value) { v.SetString(s) }
}

// auditPolicy fails when the type has an exported field the policy does not
// classify, or the policy names a field the type lacks, then probes every
// field and holds it to its disposition.
func auditPolicy(t *testing.T, name string, typ reflect.Type, p policy, probe func(field string) Result) {
	t.Helper()
	var want []string
	for f := range p {
		want = append(want, f)
	}
	sort.Strings(want)
	have := exportedFields(typ)
	if missing := diffStrings(have, want); len(missing) > 0 {
		t.Errorf("%s: fields with no classification (add them to the policy and to doc.go): %v", name, missing)
	}
	if stale := diffStrings(want, have); len(stale) > 0 {
		t.Errorf("%s: classified fields the type no longer has: %v", name, stale)
	}
	for _, field := range have {
		d, ok := p[field]
		if !ok {
			continue
		}
		res := probe(field)
		switch d {
		case derived:
			if len(res.Kinds) == 0 {
				t.Errorf("%s.%s is derived into a kind but moving it yields kinds %v", name, field, res.Kinds)
			}
		case recorded:
			if res.Empty() {
				t.Errorf("%s.%s is recorded but moving it yields nothing", name, field)
			} else if len(res.Kinds) != 0 {
				t.Errorf("%s.%s is recorded only but yields kinds %v", name, field, res.Kinds)
			}
		case excluded:
			if !res.Empty() {
				t.Errorf("%s.%s is excluded but moving it alone yields %+v", name, field, res)
			}
		}
	}
}

// diffStrings returns the members of a not in b (both sorted).
func diffStrings(a, b []string) []string {
	var out []string
	for _, s := range a {
		if !slices.Contains(b, s) {
			out = append(out, s)
		}
	}
	return out
}

func richDetail() detail.Detail {
	return detail.Detail{
		Merged: false, MergeStateStatus: "CLEAN", ReviewRequests: []string{"alice", "team-a"},
		Additions: 10, Deletions: 5, ChangedFiles: 3,
	}
}

func richConversation() conversation.Content {
	return conversation.Content{
		Comments: []schema.PRComment{{
			ID: "c1", Author: "someone", Body: "first", Path: "a.go", Line: 3, ThreadID: "t1",
			ReviewThreadID: "RT1", Resolved: false, ThreadOutdated: false,
		}},
		Reviews: []schema.PRReview{{
			ID: "r1", Author: "reviewer", State: "COMMENTED", Body: "ok", SubmittedAt: "2026-01-01T00:00:00Z",
			Comments: []schema.PRComment{{ID: "c2", Body: "inline"}},
		}},
		Connections: schema.PRConnections{
			Reviews: schema.PRConnectionReport{Total: 1, Returned: 1}, Threads: schema.PRConnectionReport{Total: 1, Returned: 1},
			Comments: schema.PRConnectionReport{Total: 2, Returned: 2},
		},
	}
}

func richRuns() runs.Content {
	return runs.Content{Runs: []schema.CIRun{{
		ID: "100", Name: "ci", Status: "completed", Conclusion: "failure", URL: "https://example.invalid/r/100",
		Provider: "github-actions", HeadSHA: "aaa111", Repo: "example/widgets", PRID: "PR_node_1", Attempt: 1,
		Jobs:  []schema.CIJob{{ID: "11", Name: "build", Status: "completed", Conclusion: "failure", URL: "https://example.invalid/j/11"}},
		AsOf:  "2026-01-01T00:00:00Z",
		Stale: false,
	}}}
}

// TestFieldCoverage lists every exported field of the summary, detail,
// conversation and CI run and job types and asserts each is recorded, derived
// into a kind, or on the documented exclusion list. A field added later fails
// until someone classifies it; and each classification is proved by moving the
// field alone and looking at what comes out.
func TestFieldCoverage(t *testing.T) {
	t.Run("summary.Summary", func(t *testing.T) {
		overrides := map[string]func(reflect.Value){"State": setString("MERGED"), "Mergeable": setString("CONFLICTING")}
		auditPolicy(t, "summary.Summary", reflect.TypeOf(summary.Summary{}), summaryPolicy, func(field string) Result {
			prev, next := richSummary(), richSummary()
			mutateField(&next, field, overrides)
			st := summary.Stored{Summary: next}
			if field == "Mergeable" { // the persister's verdict, not the value, makes the kind
				st.MergeabilityChanged, st.PrevMergeable = true, prev.Mergeable
			}
			return Summary(&prev, st, 100)
		})
	})
	t.Run("detail.Detail", func(t *testing.T) {
		auditPolicy(t, "detail.Detail", reflect.TypeOf(detail.Detail{}), detailPolicy, func(field string) Result {
			prev, next := richDetail(), richDetail()
			mutateField(&next, field, nil)
			return mustGroup(t, groups.Detail, mustJSON(t, prev), mustJSON(t, next), 100)
		})
	})
	conv := func(t *testing.T, mutateIn func(*conversation.Content)) Result {
		prev, next := richConversation(), richConversation()
		mutateIn(&next)
		return mustGroup(t, groups.Conversation, mustJSON(t, prev), mustJSON(t, next), 100)
	}
	t.Run("conversation.Content", func(t *testing.T) {
		auditPolicy(t, "conversation.Content", reflect.TypeOf(conversation.Content{}), conversationPolicy, func(field string) Result {
			return conv(t, func(c *conversation.Content) { mutateField(c, field, nil) })
		})
	})
	t.Run("schema.PRComment", func(t *testing.T) {
		auditPolicy(t, "schema.PRComment", reflect.TypeOf(schema.PRComment{}), commentPolicy, func(field string) Result {
			return conv(t, func(c *conversation.Content) { mutateField(&c.Comments[0], field, nil) })
		})
	})
	t.Run("schema.PRReview", func(t *testing.T) {
		auditPolicy(t, "schema.PRReview", reflect.TypeOf(schema.PRReview{}), reviewPolicy, func(field string) Result {
			return conv(t, func(c *conversation.Content) { mutateField(&c.Reviews[0], field, nil) })
		})
	})
	t.Run("schema.PRConnections", func(t *testing.T) {
		auditPolicy(t, "schema.PRConnections", reflect.TypeOf(schema.PRConnections{}), connectionsPolicy, func(field string) Result {
			return conv(t, func(c *conversation.Content) { mutateField(&c.Connections, field, nil) })
		})
	})
	t.Run("schema.PRConnectionReport", func(t *testing.T) {
		auditPolicy(t, "schema.PRConnectionReport", reflect.TypeOf(schema.PRConnectionReport{}), reportPolicy, func(field string) Result {
			return conv(t, func(c *conversation.Content) { mutateField(&c.Connections.Reviews, field, nil) })
		})
	})
	runsProbe := func(t *testing.T, mutateIn func(*runs.Content)) Result {
		prev, next := richRuns(), richRuns()
		mutateIn(&next)
		return mustGroup(t, groups.Runs, mustJSON(t, prev), mustJSON(t, next), 100)
	}
	t.Run("schema.CIRun", func(t *testing.T) {
		auditPolicy(t, "schema.CIRun", reflect.TypeOf(schema.CIRun{}), runPolicy, func(field string) Result {
			return runsProbe(t, func(c *runs.Content) { mutateField(&c.Runs[0], field, nil) })
		})
	})
	t.Run("schema.CIJob", func(t *testing.T) {
		auditPolicy(t, "schema.CIJob", reflect.TypeOf(schema.CIJob{}), jobPolicy, func(field string) Result {
			return runsProbe(t, func(c *runs.Content) { mutateField(&c.Runs[0].Jobs[0], field, nil) })
		})
	})
	t.Run("schema.PRFile", func(t *testing.T) {
		auditPolicy(t, "schema.PRFile", reflect.TypeOf(schema.PRFile{}), filePolicy, func(field string) Result {
			prev := prfacts.Files{Files: []schema.PRFile{{Path: "a.go", Additions: 1, Deletions: 2}}}
			next := prfacts.Files{Files: []schema.PRFile{{Path: "a.go", Additions: 1, Deletions: 2}}}
			mutateField(&next.Files[0], field, nil)
			return mustGroup(t, groups.Files, mustJSON(t, prev), mustJSON(t, next), 100)
		})
	})
	t.Run("schema.PRCommit", func(t *testing.T) {
		auditPolicy(t, "schema.PRCommit", reflect.TypeOf(schema.PRCommit{}), commitPolicy, func(field string) Result {
			prev := prfacts.Commits{Commits: []schema.PRCommit{{SHA: "abc", Author: "phillipgreenii", Message: "m"}}}
			next := prfacts.Commits{Commits: []schema.PRCommit{{SHA: "abc", Author: "phillipgreenii", Message: "m"}}}
			mutateField(&next.Commits[0], field, nil)
			return mustGroup(t, groups.Commits, mustJSON(t, prev), mustJSON(t, next), 100)
		})
	})
}

// The audit itself must catch drift: a field the policy does not name, and a
// policy entry the type lacks, are both reported.
func TestAuditCatchesDrift(t *testing.T) {
	type sample struct {
		Known string
		Added string
	}
	have := exportedFields(reflect.TypeOf(sample{}))
	if got := diffStrings(have, []string{"Known"}); !slices.Equal(got, []string{"Added"}) {
		t.Errorf("unclassified = %v", got)
	}
	if got := diffStrings([]string{"Known", "Gone"}, have); !slices.Equal(got, []string{"Gone"}) {
		t.Errorf("stale = %v", got)
	}
}

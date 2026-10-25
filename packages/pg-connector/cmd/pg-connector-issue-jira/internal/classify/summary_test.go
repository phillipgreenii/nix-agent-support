package classify

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

const testCap = 64

// rich is a fully populated stored summary, so a mutation of one field is
// compared against real values on both sides.
func rich() groups.SummaryContent {
	return groups.Stored(groups.SummaryContent{
		Issue: schema.Issue{
			ID: "PROJ-12", Title: "Add widget", State: "In Progress", URL: "https://example.invalid/browse/PROJ-12",
			StatusCategory: "indeterminate", Priority: "High", Labels: []string{"bug", "docs"},
			IssueType: "Task", Tracker: "jira", Assignee: "ada", Parent: "PROJ-1",
			CreatedAt: "2026-01-01T00:00:00Z", DueDate: "2026-12-01",
			CommentCount: 2, LatestCommentAt: "2026-10-05T10:00:00Z",
			AsOf: "2026-10-05T11:00:00Z", Stale: false, Version: 3, UpdatedAt: "2026-10-05T10:11:12Z",
		},
		Updated: "2026-10-05T10:11:12.000+0000",
	})
}

func names(fs []Field) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return out
}

func TestSummaryKindsAndFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*schema.Issue)
		kinds  []string
		fields []string
	}{
		{
			"state only", func(i *schema.Issue) { i.State = "In Review" },
			[]string{KindStatusChanged},
			[]string{"state"},
		},
		{
			"state and category within not-done", func(i *schema.Issue) { i.State = "Backlog"; i.StatusCategory = "new" },
			[]string{KindStatusChanged},
			[]string{"state", "status_category"},
		},
		{
			"close", func(i *schema.Issue) { i.State = "Done"; i.StatusCategory = "done" },
			[]string{KindClosed, KindStatusChanged},
			[]string{"state", "status_category"},
		},
		{
			"category crosses into done with state unchanged", func(i *schema.Issue) { i.StatusCategory = "done" },
			[]string{KindClosed},
			[]string{"status_category"},
		},
		{
			"category alone, not crossing done", func(i *schema.Issue) { i.StatusCategory = "new" },
			[]string{KindFieldsChanged},
			[]string{"status_category"},
		},
		{
			"assignee", func(i *schema.Issue) { i.Assignee = "grace" },
			[]string{KindAssigneeChanged},
			[]string{"assignee"},
		},
		{
			"assignee cleared", func(i *schema.Issue) { i.Assignee = "" },
			[]string{KindAssigneeChanged},
			[]string{"assignee"},
		},
		{
			"priority only", func(i *schema.Issue) { i.Priority = "Low" },
			[]string{KindPriorityChanged},
			[]string{"priority"},
		},
		{
			"labels only", func(i *schema.Issue) { i.Labels = []string{"bug", "docs", "new"} },
			[]string{KindLabelsChanged},
			[]string{"labels"},
		},
		{
			"labels removed to none", func(i *schema.Issue) { i.Labels = nil },
			[]string{KindLabelsChanged},
			[]string{"labels"},
		},
		{
			"parent", func(i *schema.Issue) { i.Parent = "PROJ-2" },
			[]string{KindParentChanged},
			[]string{"parent"},
		},
		{
			"title", func(i *schema.Issue) { i.Title = "Add gadget" },
			[]string{KindTitleChanged},
			[]string{"title"},
		},
		{
			"comment count", func(i *schema.Issue) { i.CommentCount = 3 },
			[]string{KindCommentsChanged},
			[]string{"comment_count"},
		},
		{
			"comment count and latest time", func(i *schema.Issue) { i.CommentCount = 3; i.LatestCommentAt = "2026-10-06T10:00:00Z" },
			[]string{KindCommentsChanged},
			[]string{"comment_count", "latest_comment_at"},
		},
		{
			"latest comment time only", func(i *schema.Issue) { i.LatestCommentAt = "2026-10-06T10:00:00Z" },
			[]string{KindCommentsChanged},
			[]string{"latest_comment_at"},
		},
		{
			"key rename", func(i *schema.Issue) { i.ID = "NEWP-12" },
			[]string{KindRenamed},
			[]string{"id"},
		},
		{
			"issue type", func(i *schema.Issue) { i.IssueType = "Bug" },
			[]string{KindFieldsChanged},
			[]string{"issue_type"},
		},
		{"several uncovered fields", func(i *schema.Issue) {
			i.IssueType = "Bug"
			i.Tracker = "other"
			i.URL = "https://example.invalid/browse/PROJ-13"
			i.DueDate = "2027-01-01"
			i.CreatedAt = "2025-01-01T00:00:00Z"
		}, []string{KindFieldsChanged}, []string{"created_at", "due_date", "issue_type", "tracker", "url"}},
		{
			"metadata", func(i *schema.Issue) { i.Metadata = map[string]string{"k": "v"} },
			[]string{KindFieldsChanged},
			[]string{"metadata"},
		},
		{
			"title and an uncovered field", func(i *schema.Issue) { i.Title = "x"; i.IssueType = "Bug" },
			[]string{KindTitleChanged, KindFieldsChanged},
			[]string{"issue_type", "title"},
		},
		{"everything specific at once", func(i *schema.Issue) {
			i.State = "Done"
			i.StatusCategory = "done"
			i.Assignee = "grace"
			i.Priority = "Low"
			i.Labels = []string{"x"}
			i.Parent = "PROJ-9"
			i.Title = "t"
			i.CommentCount = 9
			i.ID = "PROJ-99"
		}, []string{
			KindClosed, KindStatusChanged, KindAssigneeChanged, KindPriorityChanged, KindLabelsChanged,
			KindParentChanged, KindTitleChanged, KindCommentsChanged, KindRenamed,
		}, []string{"assignee", "comment_count", "id", "labels", "parent", "priority", "state", "status_category", "title"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev := rich()
			next := prev
			tc.mutate(&next.Issue)
			res, deps := Summary(prev, next, testCap)
			if !slices.Equal(res.Kinds, tc.kinds) {
				t.Errorf("kinds = %v, want %v", res.Kinds, tc.kinds)
			}
			if got := names(res.Fields); !slices.Equal(got, tc.fields) {
				t.Errorf("fields = %v, want %v", got, tc.fields)
			}
			if !slices.Equal(deps, []string{groups.Detail}) {
				t.Errorf("dependents = %v, want [detail]", deps)
			}
		})
	}
}

func TestReopenYieldsReopenedAndStatusChanged(t *testing.T) {
	prev := rich()
	prev.Issue.State, prev.Issue.StatusCategory = "Done", "done"
	next := rich()
	res, _ := Summary(prev, next, testCap)
	if want := []string{KindReopened, KindStatusChanged}; !slices.Equal(res.Kinds, want) {
		t.Fatalf("kinds = %v, want %v", res.Kinds, want)
	}
	if got, want := names(res.Fields), []string{"state", "status_category"}; !slices.Equal(got, want) {
		t.Fatalf("fields = %v, want %v", got, want)
	}
}

func TestLabelsOnlyAndPriorityOnlyEditsYieldExactlyOneKindAndOneField(t *testing.T) {
	prev := rich()
	for _, mutate := range []func(*schema.Issue){
		func(i *schema.Issue) { i.Labels = []string{"bug"} },
		func(i *schema.Issue) { i.Priority = "Low" },
	} {
		next := prev
		mutate(&next.Issue)
		res, _ := Summary(prev, next, testCap)
		if len(res.Kinds) != 1 || len(res.Fields) != 1 {
			t.Fatalf("result = %+v, want one kind and one field", res)
		}
	}
}

func TestFieldValuesAreBeforeAndAfter(t *testing.T) {
	prev := rich()
	next := prev
	next.Issue.Priority = "Low"
	next.Issue.Labels = []string{"docs", "bug", "x"}
	next.Issue.Assignee = ""
	res, _ := Summary(prev, next, testCap)
	want := map[string][2]string{
		"priority": {`"High"`, `"Low"`},
		"labels":   {`["bug","docs"]`, `["bug","docs","x"]`},
		"assignee": {`"ada"`, ``},
	}
	for _, f := range res.Fields {
		w, ok := want[f.Name]
		if !ok {
			t.Fatalf("unexpected field %q", f.Name)
		}
		if string(f.Before) != w[0] || string(f.After) != w[1] {
			t.Errorf("%s: before %q after %q, want %q %q", f.Name, f.Before, f.After, w[0], w[1])
		}
	}
	if len(res.Fields) != len(want) {
		t.Fatalf("fields = %v", names(res.Fields))
	}
}

func TestLabelsAreComparedAsASet(t *testing.T) {
	prev := rich()
	for _, ls := range [][]string{{"docs", "bug"}, {"bug", "docs", "bug"}} {
		next := prev
		next.Issue.Labels = ls
		res, deps := Summary(prev, next, testCap)
		if !res.Empty() {
			t.Fatalf("labels %v: result = %+v, want empty", ls, res)
		}
		_ = deps
	}
	// nil and empty labels are the same set.
	a, b := prev, prev
	a.Issue.Labels, b.Issue.Labels = nil, []string{}
	if res, _ := Summary(a, b, testCap); !res.Empty() {
		t.Fatalf("nil vs empty labels: %+v", res)
	}
}

func TestUpdatedAloneYieldsNoRowButQueuesDetail(t *testing.T) {
	prev := rich()
	next := prev
	next.Updated = "2026-10-06T10:11:12.000+0000"
	res, deps := Summary(prev, next, testCap)
	if !res.Empty() {
		t.Fatalf("result = %+v, want empty", res)
	}
	if !slices.Equal(deps, []string{groups.Detail}) {
		t.Fatalf("dependents = %v, want [detail]", deps)
	}
}

func TestIdenticalContentsYieldNothing(t *testing.T) {
	prev := rich()
	res, deps := Summary(prev, rich(), testCap)
	if !res.Empty() || len(deps) != 0 {
		t.Fatalf("result = %+v, dependents = %v", res, deps)
	}
}

func TestVolatileFieldsAreNeverCompared(t *testing.T) {
	prev := rich()
	next := prev
	next.Issue.AsOf = "2030-01-01T00:00:00Z"
	next.Issue.Stale = true
	next.Issue.Version = 99
	next.Issue.UpdatedAt = "2030-01-01T00:00:00Z"
	next.Issue.ServedFrom = "store"
	age := int64(12)
	next.Issue.AgeSeconds = &age
	next.Issue.Groups = []schema.GroupInfo{{Group: "summary"}}
	next.Gone = true
	res, deps := Summary(prev, next, testCap)
	if !res.Empty() || len(deps) != 0 {
		t.Fatalf("result = %+v, dependents = %v, want nothing", res, deps)
	}
}

func TestEveryDifferenceCarriesAKind(t *testing.T) {
	prev := rich()
	for _, mutate := range []func(*schema.Issue){
		func(i *schema.Issue) { i.Owner = "o" },
		func(i *schema.Issue) { i.ExternalRefs = []string{"r"} },
		func(i *schema.Issue) { i.Description = "d" },
		func(i *schema.Issue) { i.StatusChangedAt = "2026-10-01T00:00:00Z" },
		func(i *schema.Issue) { i.OperatorUpdatedAt = "2026-10-01T00:00:00Z" },
		func(i *schema.Issue) { i.Deps = []schema.IssueDependency{{ID: "A-1", Type: "blocks"}} },
	} {
		next := prev
		mutate(&next.Issue)
		res, _ := Summary(prev, next, testCap)
		if len(res.Fields) == 0 || !slices.Contains(res.Kinds, KindFieldsChanged) {
			t.Fatalf("result = %+v: a real difference must carry fields_changed naming its field", res)
		}
	}
}

func TestSummaryIsDeterministicAndDoesNotModifyItsInput(t *testing.T) {
	prev := rich()
	next := rich()
	next.Issue.State, next.Issue.StatusCategory = "Done", "done"
	next.Issue.Labels = []string{"z", "a"}
	next.Issue.Title = "other"
	next.Updated = "2026-10-06T10:11:12.000+0000"
	prevJSON, _ := json.Marshal(prev)
	nextJSON, _ := json.Marshal(next)

	r1, d1 := Summary(prev, next, testCap)
	r2, d2 := Summary(prev, next, testCap)
	b1, _ := json.Marshal(struct {
		R Result
		D []string
	}{r1, d1})
	b2, _ := json.Marshal(struct {
		R Result
		D []string
	}{r2, d2})
	if string(b1) != string(b2) {
		t.Fatalf("runs differ:\n%s\n%s", b1, b2)
	}
	if a, _ := json.Marshal(prev); string(a) != string(prevJSON) {
		t.Fatal("prev modified")
	}
	if a, _ := json.Marshal(next); string(a) != string(nextJSON) {
		t.Fatal("next modified")
	}
}

func TestUnstoredFormIsNormalisedInternally(t *testing.T) {
	// The arguments are normalised here too, so volatile fields differing in the
	// input never produce a row even when the caller passed the raw form.
	prev := rich()
	next := rich()
	next.Issue.AsOf = "2031-01-01T00:00:00Z"
	next.Issue.Version = 7
	if res, deps := Summary(prev, next, testCap); !res.Empty() || len(deps) != 0 {
		t.Fatalf("result = %+v, dependents = %v", res, deps)
	}
}

package changes

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

func encode(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func summaryOf(labels ...string) groups.SummaryContent {
	return groups.SummaryContent{
		Issue:   schema.Issue{ID: "PROJ-1", Title: "A title", State: "To Do", Labels: labels},
		Updated: "2026-10-05T10:11:12.000+0000",
	}
}

func TestDescribeIsTheGroupsDescriber(t *testing.T) {
	raw := encode(t, summaryOf())
	got, err := classifier{}.Describe(raw)
	if err != nil {
		t.Fatal(err)
	}
	want, err := groups.Describer{}.Describe(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ref != want.Ref || got.Ref != "PROJ-1" || !got.OriginAt.Equal(want.OriginAt) || got.Gone != want.Gone {
		t.Errorf("Describe = %+v, want %+v", got, want)
	}
	if _, err := (classifier{}).Describe([]byte("not json")); err == nil {
		t.Error("an undecodable content was described")
	}
}

func TestPrimaryReturnsClassifySummarysResultAndDependents(t *testing.T) {
	prev, next := summaryOf("a"), summaryOf("a", "b")
	res, deps, err := classifier{}.Primary(encode(t, prev), encode(t, next), 64)
	if err != nil {
		t.Fatal(err)
	}
	wantRes, wantDeps := classify.Summary(prev, next, 64)
	if !slices.Equal(res.Kinds, wantRes.Kinds) || !slices.Equal(deps, wantDeps) {
		t.Errorf("kinds = %v deps = %v, want %v %v", res.Kinds, deps, wantRes.Kinds, wantDeps)
	}
	if !slices.Equal(res.Kinds, []string{classify.KindLabelsChanged}) || !slices.Equal(deps, []string{groups.Detail}) {
		t.Errorf("kinds = %v deps = %v", res.Kinds, deps)
	}
}

func TestPrimaryOfAnUpdatedTokenAloneHasNoKindsButQueuesDetail(t *testing.T) {
	prev, next := summaryOf("a"), summaryOf("a")
	next.Updated = "2026-10-05T10:11:13.000+0000"
	res, deps, err := classifier{}.Primary(encode(t, prev), encode(t, next), 64)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Kinds) != 0 || !slices.Equal(deps, []string{groups.Detail}) {
		t.Errorf("kinds = %v deps = %v", res.Kinds, deps)
	}
}

func TestPrimaryWithAPreviousThatDoesNotDecodeIsABaseline(t *testing.T) {
	for _, prev := range [][]byte{nil, []byte(""), []byte("not json"), []byte("[1]")} {
		res, deps, err := classifier{}.Primary(prev, encode(t, summaryOf("a")), 64)
		if err != nil || len(res.Kinds) != 0 || len(res.Fields) != 0 || deps != nil {
			t.Errorf("prev %q: res = %+v deps = %v err = %v, want the zero result", prev, res, deps, err)
		}
	}
}

func TestPrimaryWithANextThatDoesNotDecodeIsAnError(t *testing.T) {
	if _, _, err := (classifier{}).Primary(encode(t, summaryOf()), []byte("not json"), 64); err == nil {
		t.Error("an undecodable next summary was classified")
	}
}

func TestGroupClassifiesTheDetailGroupAndRejectsAnyOther(t *testing.T) {
	prev := groups.DetailContent{Description: "one"}
	next := groups.DetailContent{Description: "two"}
	res, err := classifier{}.Group(groups.Detail, encode(t, prev), encode(t, next), 64)
	if err != nil {
		t.Fatal(err)
	}
	want := classify.Detail(prev, next, 64)
	if !slices.Equal(res.Kinds, want.Kinds) || !slices.Equal(res.Kinds, []string{classify.KindDescriptionChanged}) {
		t.Errorf("kinds = %v, want %v", res.Kinds, want.Kinds)
	}
	if _, err := (classifier{}).Group("comments", []byte("{}"), []byte("{}"), 64); err == nil {
		t.Error("an unknown group was classified")
	}
	if _, err := (classifier{}).Group(groups.Detail, []byte("not json"), []byte("{}"), 64); err == nil {
		t.Error("an undecodable previous detail was classified")
	}
	if _, err := (classifier{}).Group(groups.Detail, []byte("{}"), []byte("not json"), 64); err == nil {
		t.Error("an undecodable detail was classified")
	}
}

func TestCountsIsTrueOnlyForTheAwaitedGroupFetchedAtOrAfterSince(t *testing.T) {
	since := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	aw := engine.Awaiting{Group: groups.Detail, Since: since}
	detail := store.GroupRow{Group: groups.Detail}
	other := store.GroupRow{Group: groups.Summary}
	cases := []struct {
		name    string
		fetched store.GroupRow
		started time.Time
		want    bool
	}{
		{"started at since", detail, since, true},
		{"started after since", detail, since.Add(time.Second), true},
		{"started before since", detail, since.Add(-time.Nanosecond), false},
		{"another group", other, since.Add(time.Hour), false},
	}
	for _, c := range cases {
		if got := (classifier{}).Counts(aw, c.fetched, store.PendingChange{}, c.started); got != c.want {
			t.Errorf("%s: Counts = %v, want %v", c.name, got, c.want)
		}
	}
}

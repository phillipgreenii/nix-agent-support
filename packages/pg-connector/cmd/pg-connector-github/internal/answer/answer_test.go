package answer_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/answer"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var now = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func res(group string, status groupsvc.Status, origin bool, fetchedAgo time.Duration) groupsvc.GroupResult {
	return groupsvc.GroupResult{
		Group:      group,
		Status:     status,
		FromOrigin: origin,
		Row: store.GroupRow{
			Group:      group,
			FetchedAt:  now.Add(-fetchedAgo),
			FreshUntil: now.Add(-fetchedAgo).Add(time.Minute),
		},
	}
}

func TestFrom_ServedFrom(t *testing.T) {
	cases := []struct {
		name string
		in   []groupsvc.GroupResult
		want string
	}{
		{"one group fetched in this call", []groupsvc.GroupResult{
			res("summary", groupsvc.Fresh, false, 5*time.Second),
			res("files", groupsvc.Fresh, true, 0),
		}, "origin"},
		{"every group from the store", []groupsvc.GroupResult{
			res("summary", groupsvc.Fresh, false, 5*time.Second),
			res("files", groupsvc.Fresh, false, 9*time.Second),
		}, "cache"},
		{"a stale group is cache", []groupsvc.GroupResult{
			res("summary", groupsvc.Stale, false, 5*time.Second),
		}, "cache"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := answer.From(c.in, now).ServedFrom; got != c.want {
				t.Errorf("ServedFrom = %q, want %q", got, c.want)
			}
		})
	}
}

func TestFrom_Stale(t *testing.T) {
	fresh := res("summary", groupsvc.Fresh, false, time.Second)
	stale := res("files", groupsvc.Stale, false, time.Hour)
	if answer.From([]groupsvc.GroupResult{fresh}, now).Stale {
		t.Error("all-fresh answer is stale")
	}
	p := answer.From([]groupsvc.GroupResult{fresh, stale}, now)
	if !p.Stale || p.ServedFrom != "cache" {
		t.Errorf("a group served past fresh_until: %+v, want stale cache", p)
	}
}

func TestFrom_AgeIsOldestFetchedAt(t *testing.T) {
	p := answer.From([]groupsvc.GroupResult{
		res("summary", groupsvc.Fresh, false, 30*time.Second),
		res("files", groupsvc.Fresh, false, 400*time.Second),
	}, now)
	if p.AgeSeconds != 400 {
		t.Errorf("AgeSeconds = %d, want 400", p.AgeSeconds)
	}
	if want := "2026-10-12T11:53:20Z"; p.AsOf != want {
		t.Errorf("AsOf = %q, want %q", p.AsOf, want)
	}
}

func TestFrom_AgeTruncatesAndNeverNegative(t *testing.T) {
	if got := answer.From([]groupsvc.GroupResult{res("summary", groupsvc.Fresh, false, 1999*time.Millisecond)}, now).AgeSeconds; got != 1 {
		t.Errorf("1.999 s old = %d, want 1 (truncated toward zero)", got)
	}
	if got := answer.From([]groupsvc.GroupResult{res("summary", groupsvc.Fresh, false, -time.Hour)}, now).AgeSeconds; got != 0 {
		t.Errorf("a fetched_at in the future = %d, want 0", got)
	}
}

func TestFrom_GroupsEntries(t *testing.T) {
	withAll := res("summary", groupsvc.Fresh, false, 10*time.Second)
	withAll.Row.MaxAgeAt = now.Add(time.Hour)
	withAll.Row.LastError = "boom"
	bare := res("files", groupsvc.Fresh, false, 20*time.Second)
	p := answer.From([]groupsvc.GroupResult{withAll, bare}, now)
	want := []schema.GroupInfo{
		{Group: "summary", FetchedAt: "2026-10-12T11:59:50Z", FreshUntil: "2026-10-12T12:00:50Z", MaxAgeAt: "2026-10-12T13:00:00Z", LastError: "boom"},
		{Group: "files", FetchedAt: "2026-10-12T11:59:40Z", FreshUntil: "2026-10-12T12:00:40Z"},
	}
	if !reflect.DeepEqual(p.Groups, want) {
		t.Errorf("Groups = %+v, want %+v", p.Groups, want)
	}
	raw, _ := json.Marshal(p.Groups[1])
	if want := `{"group":"files","fetched_at":"2026-10-12T11:59:40Z","fresh_until":"2026-10-12T12:00:40Z"}`; string(raw) != want {
		t.Errorf("unset max_age_at and empty last_error must be omitted: %s", raw)
	}
}

func TestFrom_TimesAreUTC(t *testing.T) {
	r := res("summary", groupsvc.Fresh, false, 10*time.Second)
	r.Row.FetchedAt = r.Row.FetchedAt.In(time.FixedZone("x", 5*3600))
	if got := answer.From([]groupsvc.GroupResult{r}, now).Groups[0].FetchedAt; got != "2026-10-12T11:59:50Z" {
		t.Errorf("FetchedAt = %q, want UTC", got)
	}
}

func TestFrom_UnavailableGroupsAreNotServed(t *testing.T) {
	p := answer.From([]groupsvc.GroupResult{
		res("summary", groupsvc.Fresh, false, 10*time.Second),
		{Group: "files", Status: groupsvc.Unavailable},
	}, now)
	if len(p.Groups) != 1 || p.AgeSeconds != 10 || p.Stale {
		t.Errorf("unavailable group leaked into the answer: %+v", p)
	}
	empty := answer.From(nil, now)
	if empty.ServedFrom != "cache" || empty.Stale || empty.AgeSeconds != 0 || empty.AsOf != "" || empty.Groups != nil {
		t.Errorf("no groups served: %+v", empty)
	}
}

func prov() answer.Provenance {
	return answer.From([]groupsvc.GroupResult{
		res("summary", groupsvc.Fresh, true, 30*time.Second),
		res("files", groupsvc.Stale, false, 400*time.Second),
	}, now)
}

func TestApplyPR(t *testing.T) {
	r := schema.PR{ID: "pr-1", AsOf: "old"}
	answer.ApplyPR(&r, prov())
	if r.ServedFrom != "origin" || !r.Stale || r.AgeSeconds == nil || *r.AgeSeconds != 400 || len(r.Groups) != 2 || r.AsOf != "2026-10-12T11:53:20Z" {
		t.Errorf("%+v", r)
	}
}

func TestApplyFilesAndCommits(t *testing.T) {
	p := prov()
	f := schema.PRFilesResult{ID: "pr-1"}
	answer.ApplyFiles(&f, p)
	if f.ServedFrom != "origin" || f.Stale == nil || !*f.Stale || f.AgeSeconds == nil || *f.AgeSeconds != 400 || len(f.Groups) != 2 {
		t.Errorf("%+v", f)
	}
	c := schema.PRCommitsResult{ID: "pr-1"}
	answer.ApplyCommits(&c, p)
	if c.ServedFrom != "origin" || c.Stale == nil || !*c.Stale || c.AgeSeconds == nil || *c.AgeSeconds != 400 || len(c.Groups) != 2 {
		t.Errorf("%+v", c)
	}
}

func TestApply_ZeroAgeAndFalseStaleAreEmitted(t *testing.T) {
	p := answer.From([]groupsvc.GroupResult{res("files", groupsvc.Fresh, true, 0)}, now)
	f := schema.PRFilesResult{ID: "pr-1", Files: []schema.PRFile{}}
	answer.ApplyFiles(&f, p)
	raw, _ := json.Marshal(f)
	want := `{"id":"pr-1","files":[],"served_from":"origin","stale":false,"age_seconds":0,"groups":[{"group":"files","fetched_at":"2026-10-12T12:00:00Z","fresh_until":"2026-10-12T12:01:00Z"}]}`
	if string(raw) != want {
		t.Errorf("got  %s\nwant %s", raw, want)
	}
}

func TestApplyPending(t *testing.T) {
	r := pr.PendingReviewResult{HeadSHA: "h", AsOf: "old"}
	answer.ApplyPending(&r, prov())
	if r.ServedFrom != "origin" || r.Stale == nil || !*r.Stale || r.AgeSeconds == nil || *r.AgeSeconds != 400 || len(r.Groups) != 2 || r.AsOf != "2026-10-12T11:53:20Z" {
		t.Errorf("%+v", r)
	}
}

func TestApplyRuns_EveryElementAndEmptyStaysEmpty(t *testing.T) {
	runs := []schema.CIRun{{ID: "1"}, {ID: "2"}}
	answer.ApplyRuns(runs, prov())
	for i, r := range runs {
		if r.ServedFrom != "origin" || !r.Stale || r.AgeSeconds == nil || *r.AgeSeconds != 400 || len(r.Groups) != 2 || r.AsOf != "2026-10-12T11:53:20Z" {
			t.Errorf("element %d: %+v", i, r)
		}
	}
	if runs[0].AgeSeconds == runs[1].AgeSeconds {
		t.Error("elements share one age pointer")
	}
	var none []schema.CIRun
	answer.ApplyRuns(none, prov())
	empty := []schema.CIRun{}
	answer.ApplyRuns(empty, prov())
	if len(empty) != 0 {
		t.Errorf("an empty list must stay empty: %v", empty)
	}
}

func TestApply_DoesNotAliasProvenanceGroups(t *testing.T) {
	p := prov()
	f := schema.PRFilesResult{}
	answer.ApplyFiles(&f, p)
	f.Groups[0].Group = "mutated"
	if p.Groups[0].Group != "summary" {
		t.Error("the result aliases the provenance's groups")
	}
}

func TestRecord_LeavesTheValuesOnTheRouterCarrier(t *testing.T) {
	r := router.New(router.Options{})
	var got router.Annotations
	r.Register("pr", "show", scriptout.OpHandler{SchemaVersion: 1, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
		answer.Record(ctx, prov(), 3*time.Second)
		got = router.AnnotationsFrom(ctx)
		return nil, nil
	}})
	r.Dispatch(context.Background(), router.Call{Instance: "pr", Request: scriptout.Request{Op: "show"}})
	want := router.Annotations{ServedFrom: "origin", Stale: true, AgeSeconds: 400, Wait: 3 * time.Second}
	if got != want {
		t.Errorf("annotations = %+v, want %+v", got, want)
	}
}

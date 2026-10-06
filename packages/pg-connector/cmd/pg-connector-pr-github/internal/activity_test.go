package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/activity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/conformance"
)

var (
	actSince  = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	actBefore = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
)

// activityFixture is a query-keyed fake of the three activity searches plus
// the GetPR read that supplies merge timestamps.
type activityFixture struct {
	mu      sync.Mutex
	opened  []api.PR
	merged  []api.PR
	closed  []api.PR
	mergeAt map[string]string // "repo#n" -> merged_at ("" or absent: unavailable)
	queries []string
	limits  []int
}

func (a *activityFixture) gh() *fakeGH {
	return &fakeGH{
		viewerLogin: "me",
		searchActivityFn: func(_ context.Context, query string, limit int) ([]api.PR, error) {
			// The review/comment candidate searches (activity_review_test.go
			// covers them) find nothing here and are not recorded.
			if strings.Contains(query, "reviewed-by:") || strings.Contains(query, "commenter:") {
				return nil, nil
			}
			a.mu.Lock()
			defer a.mu.Unlock()
			a.queries = append(a.queries, query)
			a.limits = append(a.limits, limit)
			switch {
			case strings.Contains(query, "created:"):
				return a.opened, nil
			case strings.Contains(query, "merged:"):
				return a.merged, nil
			case strings.Contains(query, "closed:"):
				return a.closed, nil
			}
			return nil, fmt.Errorf("unexpected query %q", query)
		},
		getPRFn: func(_ context.Context, repo string, number int) (*api.PR, error) {
			return &api.PR{Repo: repo, Number: number, Base: "main", Merged: true, MergedAt: a.mergeAt[formatPRID(repo, number)]}, nil
		},
	}
}

func actPR(repo string, n int, author, created, closed string) api.PR {
	return api.PR{
		Repo: repo, Number: n, Title: fmt.Sprintf("title %d", n), Author: author,
		URL: fmt.Sprintf("https://example.invalid/%s/pull/%d", repo, n), CreatedAt: created, ClosedAt: closed,
	}
}

func byID(t *testing.T, items []schema.ActivityItem) map[string]schema.ActivityItem {
	t.Helper()
	m := map[string]schema.ActivityItem{}
	for _, it := range items {
		if _, dup := m[it.ID]; dup {
			t.Fatalf("duplicate id %q", it.ID)
		}
		m[it.ID] = it
	}
	return m
}

func TestListActivity_OnlyViewerItemsWithExactTimestampsAndIDs(t *testing.T) {
	fx := &activityFixture{
		opened: []api.PR{
			actPR("o/r", 12, "me", "2026-09-05T10:00:00Z", ""),
			actPR("o/r", 13, "someone-else", "2026-09-06T10:00:00Z", ""),
		},
		merged: []api.PR{
			actPR("o/r", 12, "ME", "2026-09-05T10:00:00Z", "2026-09-07T12:00:00Z"), // case-insensitive author match
			actPR("o/r", 13, "someone-else", "2026-09-06T10:00:00Z", "2026-09-08T00:00:00Z"),
		},
		closed: []api.PR{
			actPR("o/r", 14, "me", "2026-09-01T00:00:00Z", "2026-09-09T08:30:00Z"),
			actPR("o/r", 15, "someone-else", "2026-09-01T00:00:00Z", "2026-09-09T09:30:00Z"),
		},
		mergeAt: map[string]string{"o/r#12": "2026-09-07T12:00:00Z", "o/r#13": "2026-09-08T00:00:00Z"},
	}
	b := New(fx.gh())
	res, err := b.ListActivity(context.Background(), actSince, actBefore)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	if res.Truncated {
		t.Fatalf("truncated = true, want false")
	}
	got := byID(t, res.Items)
	if len(got) != 3 {
		t.Fatalf("got %d items, want exactly the viewer's 3: %+v", len(got), res.Items)
	}
	want := map[string]struct{ kind, at string }{
		"o/r#12#pr.opened": {"pr.opened", "2026-09-05T10:00:00Z"},
		"o/r#12#pr.merged": {"pr.merged", "2026-09-07T12:00:00Z"},
		"o/r#14#pr.closed": {"pr.closed", "2026-09-09T08:30:00Z"},
	}
	for id, w := range want {
		it, ok := got[id]
		if !ok {
			t.Fatalf("missing item %q in %v", id, got)
		}
		if it.Kind != w.kind || it.OccurredAt != w.at || it.EntityType != "pr" || it.Approximate || it.Stale {
			t.Errorf("%s: %+v, want kind %s occurred_at %s entity_type pr, not approximate/stale", id, it, w.kind, w.at)
		}
		if it.EntityID+"#"+it.Kind != id {
			t.Errorf("%s: entity_id %q kind %q do not compose the id", id, it.EntityID, it.Kind)
		}
		if len(it.Labels) != 1 || it.Labels[0] != "repo:o/r" {
			t.Errorf("%s: labels = %v, want [repo:o/r]", id, it.Labels)
		}
		if !strings.HasPrefix(it.URL, "https://example.invalid/o/r/pull/") || it.Summary == "" {
			t.Errorf("%s: url %q summary %q", id, it.URL, it.Summary)
		}
		if _, err := time.Parse(time.RFC3339, it.AsOf); err != nil {
			t.Errorf("%s: as_of %q: %v", id, it.AsOf, err)
		}
		var obj map[string]any
		if err := json.Unmarshal(it.Fields, &obj); err != nil || obj["title"] == nil || obj["author"] == nil {
			t.Errorf("%s: fields %s must be an object with title and author (err %v)", id, it.Fields, err)
		}
	}
	var merged map[string]any
	_ = json.Unmarshal(got["o/r#12#pr.merged"].Fields, &merged)
	if merged["base"] != "main" {
		t.Errorf("pr.merged fields = %v, want base main", merged)
	}

	// Second call over the same range: identical ids.
	res2, err := b.ListActivity(context.Background(), actSince, actBefore)
	if err != nil {
		t.Fatalf("second ListActivity: %v", err)
	}
	got2 := byID(t, res2.Items)
	for id := range got {
		if _, ok := got2[id]; !ok {
			t.Errorf("id %q not stable across calls", id)
		}
	}
}

func TestListActivity_QueriesUseExactBoundsViewerAndCap(t *testing.T) {
	fx := &activityFixture{}
	b := New(fx.gh())
	if _, err := b.ListActivity(context.Background(), actSince, actBefore); err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	if len(fx.queries) != 3 {
		t.Fatalf("queries = %v, want 3 (opened, merged, closed)", fx.queries)
	}
	rng := "2026-09-01T00:00:00Z..2026-10-01T00:00:00Z"
	wants := []string{"author:me created:" + rng, "author:me merged:" + rng, "author:me is:unmerged closed:" + rng}
	for _, w := range wants {
		found := false
		for _, q := range fx.queries {
			if q == w {
				found = true
			}
		}
		if !found {
			t.Errorf("missing query %q in %v", w, fx.queries)
		}
	}
	for _, l := range fx.limits {
		if l != 1000 {
			t.Errorf("limit = %d, want 1000", l)
		}
	}
}

func TestListActivity_SinceOmittedUsesUpperBoundOnly(t *testing.T) {
	fx := &activityFixture{
		opened: []api.PR{actPR("o/r", 1, "me", "2020-01-01T00:00:00Z", "")},
	}
	b := New(fx.gh())
	res, err := b.ListActivity(context.Background(), time.Time{}, actBefore)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	for _, q := range fx.queries {
		if !strings.Contains(q, "<=2026-10-01T00:00:00Z") || strings.Contains(q, "..") {
			t.Errorf("open-ended query %q must be upper-bound only", q)
		}
	}
	if len(res.Items) != 1 || res.Items[0].ID != "o/r#1#pr.opened" {
		t.Fatalf("items = %+v, want the full record up to before", res.Items)
	}
	if res.Truncated {
		t.Fatal("truncated must be false below the cap")
	}
}

func TestListActivity_RangeIsInclusiveSinceExclusiveBefore(t *testing.T) {
	fx := &activityFixture{
		opened: []api.PR{
			actPR("o/r", 1, "me", "2026-09-01T00:00:00Z", ""), // == since: kept
			actPR("o/r", 2, "me", "2026-08-31T23:59:59Z", ""), // before since: dropped
			actPR("o/r", 3, "me", "2026-10-01T00:00:00Z", ""), // == before: dropped
			actPR("o/r", 4, "me", "2026-09-30T23:59:59Z", ""), // kept
			actPR("o/r", 5, "me", "", ""),                     // undatable: dropped
		},
	}
	res, err := New(fx.gh()).ListActivity(context.Background(), actSince, actBefore)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	got := byID(t, res.Items)
	if len(got) != 2 || got["o/r#1#pr.opened"].ID == "" || got["o/r#4#pr.opened"].ID == "" {
		t.Fatalf("items = %v, want only #1 and #4", got)
	}
}

func TestListActivity_MergedWithoutMergeTimestampIsNotEmitted(t *testing.T) {
	fx := &activityFixture{
		merged: []api.PR{
			actPR("o/r", 1, "me", "2026-09-01T00:00:00Z", "2026-09-02T00:00:00Z"),
			actPR("o/r", 2, "me", "2026-09-01T00:00:00Z", "2026-09-02T00:00:00Z"),
			actPR("o/r", 3, "me", "2026-09-01T00:00:00Z", "2026-09-02T00:00:00Z"),
		},
		// #1 has none, #2 has one outside the range, #3 is fine.
		mergeAt: map[string]string{"o/r#2": "2026-10-02T00:00:00Z", "o/r#3": "2026-09-03T00:00:00Z"},
	}
	res, err := New(fx.gh()).ListActivity(context.Background(), actSince, actBefore)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].ID != "o/r#3#pr.merged" || res.Items[0].OccurredAt != "2026-09-03T00:00:00Z" {
		t.Fatalf("items = %+v, want only #3 merged at its own merge timestamp", res.Items)
	}
	for _, it := range res.Items {
		if it.Approximate {
			t.Errorf("%s: approximate set", it.ID)
		}
	}
}

func TestListActivity_MergedPRReadFailures(t *testing.T) {
	merged := []api.PR{actPR("o/r", 1, "me", "2026-09-01T00:00:00Z", "2026-09-02T00:00:00Z")}

	t.Run("not found is skipped", func(t *testing.T) {
		fx := &activityFixture{merged: merged}
		gh := fx.gh()
		gh.getPRFn = func(context.Context, string, int) (*api.PR, error) {
			return nil, errors.New("GraphQL: Could not resolve to a PullRequest")
		}
		res, err := New(gh).ListActivity(context.Background(), actSince, actBefore)
		if err != nil || len(res.Items) != 0 {
			t.Fatalf("res=%+v err=%v, want empty and no error", res, err)
		}
	})
	t.Run("other failure is surfaced", func(t *testing.T) {
		fx := &activityFixture{merged: merged}
		gh := fx.gh()
		gh.getPRFn = func(context.Context, string, int) (*api.PR, error) { return nil, errors.New("boom") }
		if _, err := New(gh).ListActivity(context.Background(), actSince, actBefore); err == nil {
			t.Fatal("want an error, not a silently partial result")
		}
	})
}

func TestListActivity_OpenedAndMergedSamePRHaveDistinctIDs(t *testing.T) {
	fx := &activityFixture{
		opened:  []api.PR{actPR("o/r", 7, "me", "2026-09-02T00:00:00Z", "")},
		merged:  []api.PR{actPR("o/r", 7, "me", "2026-09-02T00:00:00Z", "2026-09-03T00:00:00Z")},
		mergeAt: map[string]string{"o/r#7": "2026-09-03T00:00:00Z"},
	}
	res, err := New(fx.gh()).ListActivity(context.Background(), actSince, actBefore)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	got := byID(t, res.Items)
	if len(got) != 2 || got["o/r#7#pr.opened"].ID == "" || got["o/r#7#pr.merged"].ID == "" {
		t.Fatalf("items = %v, want pr.opened and pr.merged with distinct ids", got)
	}
}

func TestListActivity_TruncatedAtSearchCap(t *testing.T) {
	mk := func(n int) []api.PR {
		out := make([]api.PR, n)
		for i := range out {
			out[i] = actPR("o/r", i+1, "me", "2026-09-05T00:00:00Z", "")
		}
		return out
	}
	for _, tc := range []struct {
		n    int
		want bool
	}{{999, false}, {1000, true}} {
		fx := &activityFixture{opened: mk(tc.n)}
		res, err := New(fx.gh()).ListActivity(context.Background(), actSince, actBefore)
		if err != nil {
			t.Fatalf("n=%d: %v", tc.n, err)
		}
		if res.Truncated != tc.want {
			t.Errorf("n=%d: truncated = %v, want %v", tc.n, res.Truncated, tc.want)
		}
		if len(res.Items) != tc.n {
			t.Errorf("n=%d: items = %d", tc.n, len(res.Items))
		}
	}
}

func TestListActivity_UnavailableWhenViewerCannotBeResolved(t *testing.T) {
	fx := &activityFixture{opened: []api.PR{actPR("o/r", 1, "me", "2026-09-05T00:00:00Z", "")}}
	gh := fx.gh()
	gh.viewerLoginErr = errors.New("gh: not logged in")
	res, err := New(gh).ListActivity(context.Background(), actSince, actBefore)
	if res != nil {
		t.Fatalf("result = %+v, want none", res)
	}
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want unavailable", err)
	}
	if !strings.Contains(err.Error(), "viewer login") || !strings.Contains(err.Error(), "authentication") {
		t.Errorf("reason %q must name the viewer login / authentication", err)
	}
	if len(fx.queries) != 0 {
		t.Errorf("searches ran without an identity: %v", fx.queries)
	}
}

func TestListActivity_RateReserveCheckedBeforeSearch(t *testing.T) {
	fx := &activityFixture{}
	gh := fx.gh()
	gh.rateLimit = 5
	_, err := New(gh).ListActivity(context.Background(), actSince, actBefore)
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want unavailable", err)
	}
	if len(fx.queries) != 0 {
		t.Errorf("searched despite low rate limit: %v", fx.queries)
	}
}

func TestListActivity_SearchFailureIsClassified(t *testing.T) {
	gh := &fakeGH{searchActivityFn: func(context.Context, string, int) ([]api.PR, error) {
		return nil, errors.New("search blew up")
	}}
	if _, err := New(gh).ListActivity(context.Background(), actSince, actBefore); err == nil {
		t.Fatal("want an error")
	}
}

func TestListActivity_ConformsToListActivityCase(t *testing.T) {
	fx := &activityFixture{
		opened:  []api.PR{actPR("o/r", 7, "me", "2026-09-02T00:00:00Z", ""), actPR("o/r", 8, "other", "2026-09-02T00:00:00Z", "")},
		merged:  []api.PR{actPR("o/r", 7, "me", "2026-09-02T00:00:00Z", "2026-09-03T00:00:00Z")},
		closed:  []api.PR{actPR("o/r", 9, "me", "2026-09-01T00:00:00Z", "2026-09-04T00:00:00Z")},
		mergeAt: map[string]string{"o/r#7": "2026-09-03T00:00:00Z"},
	}
	backend := conformance.TableBackend{Table: activity.NewDispatchTable(New(fx.gh()))}
	for _, since := range []time.Time{actSince, {}} {
		for _, r := range conformance.RunListActivityCase(context.Background(), backend, since, actBefore) {
			if r.Err != nil {
				t.Errorf("%s (since=%v): %v", r.Name, since, r.Err)
			}
		}
	}
}

func TestNewActivityItem_FieldsAlwaysAnObject(t *testing.T) {
	for _, f := range []any{nil, map[string]any{"a": 1}, []string{"not", "an", "object"}} {
		it := newActivityItem("o/r#1", "pr.opened", "2026-09-05T00:00:00Z", "s", "", nil, f)
		if len(it.Fields) == 0 || it.Fields[0] != '{' {
			t.Errorf("fields %v -> %s, want an object", f, it.Fields)
		}
		if it.EntityType != "pr" || it.Stale || it.ID != "" {
			t.Errorf("item = %+v: entity_type pr, stale false, id left to the caller", it)
		}
		if _, err := time.Parse(time.RFC3339, it.AsOf); err != nil {
			t.Errorf("as_of %q: %v", it.AsOf, err)
		}
	}
	if got := string(newActivityItem("e", "k", "t", "s", "", nil, nil).Fields); got != "{}" {
		t.Errorf("nil fields = %s, want {}", got)
	}
}

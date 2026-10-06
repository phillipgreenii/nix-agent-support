package internal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/activity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/conformance"
)

// reviewCommentFixture is a query-keyed fake of the reviewed-by and commenter
// searches plus the per-PR reviews and comments reads.
type reviewCommentFixture struct {
	mu        sync.Mutex
	reviewed  []api.PR
	commented []api.PR
	reviews   map[string][]api.Review  // "repo#n" -> reviews
	comments  map[string][]api.Comment // "repo#n" -> comments
	queries   []string
}

func (f *reviewCommentFixture) gh() *fakeGH {
	return &fakeGH{
		viewerLogin: "me",
		searchActivityFn: func(_ context.Context, query string, _ int) ([]api.PR, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.queries = append(f.queries, query)
			switch {
			case strings.Contains(query, "reviewed-by:"):
				return f.reviewed, nil
			case strings.Contains(query, "commenter:"):
				return f.commented, nil
			}
			return nil, nil
		},
		reviewsSubmittedFn: func(_ context.Context, repo string, n int) ([]api.Review, error) {
			return f.reviews[formatPRID(repo, n)], nil
		},
		commentsFn: func(_ context.Context, repo string, n int) ([]api.Comment, error) {
			return f.comments[formatPRID(repo, n)], nil
		},
	}
}

func rev(id, author, state, submitted string) api.Review {
	return api.Review{ID: id, Author: author, State: state, SubmittedAt: submitted}
}

func cmt(id, author, created string) api.Comment {
	return api.Comment{ID: id, Author: author, CreatedAt: created}
}

func fieldsOf(t *testing.T, it schema.ActivityItem) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(it.Fields, &m); err != nil {
		t.Fatalf("%s: fields %s not an object: %v", it.ID, it.Fields, err)
	}
	return m
}

func TestListActivity_ReviewedAndCommentedOnlyViewersOwnWithOwnTimestamps(t *testing.T) {
	pr12 := actPR("o/r", 12, "someone-else", "2026-08-01T00:00:00Z", "")
	fx := &reviewCommentFixture{
		reviewed:  []api.PR{pr12},
		commented: []api.PR{pr12},
		reviews: map[string][]api.Review{"o/r#12": {
			rev("PRR_1", "me", "CHANGES_REQUESTED", "2026-09-05T10:00:00Z"),
			rev("PRR_2", "ME", "APPROVED", "2026-09-06T11:00:00Z"),
			rev("PRR_3", "someone-else", "APPROVED", "2026-09-06T12:00:00Z"),
			rev("PRR_4", "me", "COMMENTED", "2026-08-31T23:59:59Z"), // before since
			rev("PRR_5", "me", "APPROVED", "2026-10-01T00:00:00Z"),  // == before
			rev("PRR_6", "me", "PENDING", ""),                       // unsubmitted
			rev("PRR_7", "me", "DISMISSED", "2026-09-01T00:00:00Z"), // == since, kept
		}},
		comments: map[string][]api.Comment{"o/r#12": {
			cmt("IC_1", "me", "2026-09-07T10:00:00Z"),
			cmt("IC_2", "me", "2026-09-08T10:00:00Z"),
			cmt("IC_3", "someone-else", "2026-09-08T11:00:00Z"),
			cmt("IC_4", "me", ""),                                                            // no own timestamp
			cmt("IC_5", "me", "2026-08-30T00:00:00Z"),                                        // out of range
			{ID: "RC_1", Author: "me", CreatedAt: "2026-09-09T00:00:00Z", Path: "a.go"},      // inline
			{ID: "RC_2", Author: "me", CreatedAt: "2026-09-09T00:00:00Z", ReviewID: "PRR_1"}, // inline
		}},
	}
	b := New(fx.gh())
	res, err := b.ListActivity(context.Background(), actSince, actBefore)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	if res.Truncated {
		t.Fatal("truncated, want false")
	}
	got := byID(t, res.Items)
	want := map[string]struct{ kind, at, state string }{
		"o/r#12#pr.reviewed#PRR_1": {"pr.reviewed", "2026-09-05T10:00:00Z", "changes-requested"},
		"o/r#12#pr.reviewed#PRR_2": {"pr.reviewed", "2026-09-06T11:00:00Z", "approved"},
		"o/r#12#pr.reviewed#PRR_7": {"pr.reviewed", "2026-09-01T00:00:00Z", "dismissed"},
		"o/r#12#pr.commented#IC_1": {"pr.commented", "2026-09-07T10:00:00Z", ""},
		"o/r#12#pr.commented#IC_2": {"pr.commented", "2026-09-08T10:00:00Z", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d items %v, want %d", len(got), got, len(want))
	}
	for id, w := range want {
		it, ok := got[id]
		if !ok {
			t.Fatalf("missing %q in %v", id, got)
		}
		if it.Kind != w.kind || it.OccurredAt != w.at || it.EntityType != "pr" || it.EntityID != "o/r#12" || it.Approximate || it.Stale {
			t.Errorf("%s: %+v", id, it)
		}
		if len(it.Labels) != 1 || it.Labels[0] != "repo:o/r" {
			t.Errorf("%s: labels = %v", id, it.Labels)
		}
		f := fieldsOf(t, it)
		if f["title"] == nil || f["author"] != "someone-else" {
			t.Errorf("%s: fields = %v, want title and the PR author", id, f)
		}
		if w.kind == "pr.reviewed" {
			if f["state"] != w.state {
				t.Errorf("%s: state = %v, want %s", id, f["state"], w.state)
			}
		} else if _, has := f["state"]; has {
			t.Errorf("%s: pr.commented must not carry state: %v", id, f)
		}
	}

	// A second call over the same range yields the same ids.
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

func TestListActivity_ReviewCommentSearchQualifiersAreWholeDays(t *testing.T) {
	fx := &reviewCommentFixture{}
	b := New(fx.gh())
	since := time.Date(2026, 9, 1, 13, 30, 0, 0, time.UTC)
	before := time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC)
	if _, err := b.ListActivity(context.Background(), since, before); err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	for _, w := range []string{"reviewed-by:me updated:2026-09-01..2026-10-01", "commenter:me updated:2026-09-01..2026-10-01"} {
		if !containsQuery(fx.queries, w) {
			t.Errorf("missing query %q in %v", w, fx.queries)
		}
	}

	fx2 := &reviewCommentFixture{}
	if _, err := New(fx2.gh()).ListActivity(context.Background(), time.Time{}, before); err != nil {
		t.Fatalf("ListActivity since omitted: %v", err)
	}
	for _, w := range []string{"reviewed-by:me updated:<=2026-10-01", "commenter:me updated:<=2026-10-01"} {
		if !containsQuery(fx2.queries, w) {
			t.Errorf("since omitted: missing query %q in %v", w, fx2.queries)
		}
	}
	for _, q := range fx2.queries {
		if strings.Contains(q, "..") {
			t.Errorf("since omitted: %q must be upper-bound only", q)
		}
	}
}

func containsQuery(qs []string, want string) bool {
	for _, q := range qs {
		if q == want {
			return true
		}
	}
	return false
}

func TestListActivity_ReviewCommentTruncatedAtSearchCap(t *testing.T) {
	mk := func(n int) []api.PR {
		out := make([]api.PR, n)
		for i := range out {
			out[i] = actPR("o/r", i+1, "x", "2026-09-05T00:00:00Z", "")
		}
		return out
	}
	for _, tc := range []struct {
		name     string
		fx       *reviewCommentFixture
		wantTrun bool
	}{
		{"reviewed below cap", &reviewCommentFixture{reviewed: mk(999)}, false},
		{"reviewed at cap", &reviewCommentFixture{reviewed: mk(1000)}, true},
		{"commented at cap", &reviewCommentFixture{commented: mk(1000)}, true},
	} {
		res, err := New(tc.fx.gh()).ListActivity(context.Background(), actSince, actBefore)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if res.Truncated != tc.wantTrun {
			t.Errorf("%s: truncated = %v, want %v", tc.name, res.Truncated, tc.wantTrun)
		}
	}
}

func TestListActivity_ReviewCommentReadFailures(t *testing.T) {
	pr := actPR("o/r", 1, "x", "2026-09-05T00:00:00Z", "")
	t.Run("not found is skipped", func(t *testing.T) {
		fx := &reviewCommentFixture{reviewed: []api.PR{pr}, commented: []api.PR{pr}}
		gh := fx.gh()
		nf := errors.New("GraphQL: Could not resolve to a PullRequest")
		gh.reviewsSubmittedFn = func(context.Context, string, int) ([]api.Review, error) { return nil, nf }
		gh.commentsFn = func(context.Context, string, int) ([]api.Comment, error) { return nil, nf }
		res, err := New(gh).ListActivity(context.Background(), actSince, actBefore)
		if err != nil || len(res.Items) != 0 {
			t.Fatalf("res=%+v err=%v, want empty and no error", res, err)
		}
	})
	t.Run("reviews failure is surfaced", func(t *testing.T) {
		fx := &reviewCommentFixture{reviewed: []api.PR{pr}}
		gh := fx.gh()
		gh.reviewsSubmittedFn = func(context.Context, string, int) ([]api.Review, error) { return nil, errors.New("boom") }
		if _, err := New(gh).ListActivity(context.Background(), actSince, actBefore); err == nil {
			t.Fatal("want an error, not a silently partial result")
		}
	})
	t.Run("comments failure is surfaced", func(t *testing.T) {
		fx := &reviewCommentFixture{commented: []api.PR{pr}}
		gh := fx.gh()
		gh.commentsFn = func(context.Context, string, int) ([]api.Comment, error) { return nil, errors.New("boom") }
		if _, err := New(gh).ListActivity(context.Background(), actSince, actBefore); err == nil {
			t.Fatal("want an error, not a silently partial result")
		}
	})
}

func TestListActivity_ReviewCommentConformsToListActivityCase(t *testing.T) {
	pr := actPR("o/r", 5, "other", "2026-08-01T00:00:00Z", "")
	fx := &reviewCommentFixture{
		reviewed:  []api.PR{pr},
		commented: []api.PR{pr},
		reviews:   map[string][]api.Review{"o/r#5": {rev("PRR_1", "me", "APPROVED", "2026-09-05T10:00:00Z")}},
		comments:  map[string][]api.Comment{"o/r#5": {cmt("IC_1", "me", "2026-09-06T10:00:00Z")}},
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

func TestActivityKinds_ListsEveryEmittedKind(t *testing.T) {
	want := []string{"pr.opened", "pr.merged", "pr.closed", "pr.reviewed", "pr.commented"}
	if strings.Join(ActivityKinds, ",") != strings.Join(want, ",") {
		t.Fatalf("ActivityKinds = %v, want %v", ActivityKinds, want)
	}
}

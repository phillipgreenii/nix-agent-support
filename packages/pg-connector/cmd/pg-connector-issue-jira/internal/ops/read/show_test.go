package read

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func (r *rig) show(args string) (*schema.Issue, error) {
	r.t.Helper()
	out, err := r.call(OpShow, args)
	if err != nil {
		return nil, err
	}
	iss, ok := out.(*schema.Issue)
	if !ok {
		r.t.Fatalf("show answered %T, not *schema.Issue", out)
	}
	return iss, nil
}

func (r *rig) mustShow(args string) *schema.Issue {
	r.t.Helper()
	iss, err := r.show(args)
	if err != nil {
		r.t.Fatalf("show %s: %v", args, err)
	}
	return iss
}

func TestHandlersAreExactlyTheSevenOpsAtTheirSchemaVersions(t *testing.T) {
	r := newRig(t)
	want := map[string]int{
		"show": schema.IssueSchemaVersion, "list": schema.IssueSchemaVersion,
		"children": schema.IssueSchemaVersion, "deps": schema.IssueSchemaVersion,
		"auth_status": schema.IssueSchemaVersion,
		"search":      schema.SearchSchemaVersion, "list_activity": schema.ActivitySchemaVersion,
	}
	if len(r.ops) != len(want) {
		t.Errorf("handlers = %d, want %d: %v", len(r.ops), len(want), r.ops)
	}
	for op, v := range want {
		h, ok := r.ops[op]
		if !ok || h.Handle == nil || h.SchemaVersion != v {
			t.Errorf("op %s: %+v (present %v), want schema version %d", op, h, ok, v)
		}
	}
	got := append([]string(nil), Ops...)
	wantOps := []string{"show", "list", "children", "search", "list_activity", "deps", "auth_status"}
	if !reflect.DeepEqual(got, wantOps) {
		t.Errorf("Ops = %v, want %v", got, wantOps)
	}
	for _, op := range Ops {
		if _, ok := r.ops[op]; !ok {
			t.Errorf("Ops lists %s but Handlers has none", op)
		}
	}
}

func TestShowOfAFreshIssueIsALocalHitWithZeroOriginCalls(t *testing.T) {
	r := newRig(t)
	r.seed(issueKey, "the title", "the description")
	r.clk.Advance(10 * time.Second)

	iss := r.mustShow(`{"id":"abc-1"}`)
	if s, d := r.origin.counts(); s != 0 || d != 0 {
		t.Fatalf("origin calls = %d summary, %d detail, want none", s, d)
	}
	if iss.ID != issueKey || iss.Title != "the title" || iss.Description != "the description" {
		t.Errorf("issue = %+v", iss)
	}
	if iss.ServedFrom != "cache" || iss.Stale {
		t.Errorf("served_from %q stale %v, want cache and not stale", iss.ServedFrom, iss.Stale)
	}
	if iss.AgeSeconds == nil || *iss.AgeSeconds != 10 {
		t.Errorf("age_seconds = %v, want 10", iss.AgeSeconds)
	}
	if want := t0.Format(time.RFC3339); iss.AsOf != want {
		t.Errorf("as_of = %q, want %q", iss.AsOf, want)
	}
	if iss.Version != 1 {
		t.Errorf("version = %d, want the stored entity version 1", iss.Version)
	}
	if len(iss.Groups) != 2 || iss.Groups[0].Group != "summary" || iss.Groups[1].Group != "detail" {
		t.Fatalf("groups = %+v, want summary then detail", iss.Groups)
	}
	if g := iss.Groups[0]; g.FetchedAt != t0.Format(time.RFC3339) || g.FreshUntil != t0.Add(summaryTTL).Format(time.RFC3339) {
		t.Errorf("summary group = %+v", g)
	}
	if g := iss.Groups[1]; g.FreshUntil != t0.Add(detailTTL).Format(time.RFC3339) {
		t.Errorf("detail group = %+v", g)
	}
}

func TestShowAddsTheDetailToTheStoredSummary(t *testing.T) {
	r := newRig(t)
	r.seed(issueKey, "the title", "the description")

	iss := r.mustShow(`{"id":"ABC-1"}`)
	if !reflect.DeepEqual(iss.Deps, []schema.IssueDependency{{ID: "ABC-9", Type: "blocks"}}) {
		t.Errorf("deps = %+v", iss.Deps)
	}
	if iss.StatusChangedAt != "2026-10-01T00:00:00Z" || iss.OperatorUpdatedAt != "2026-10-02T00:00:00Z" {
		t.Errorf("operator facts = %q, %q", iss.StatusChangedAt, iss.OperatorUpdatedAt)
	}
	// The stored summary's own content is kept; its as_of and stale are replaced.
	if iss.StatusCategory != "new" || iss.CommentCount != 2 || iss.LatestCommentAt != "2026-10-04T10:00:00.000+0000" || iss.State != "To Do" {
		t.Errorf("summary content lost: %+v", iss)
	}
	if iss.AsOf == "ignored" || iss.Stale {
		t.Errorf("as_of %q stale %v were not replaced", iss.AsOf, iss.Stale)
	}
}

func TestShowWithFreshCallsTheOriginOncePerGroup(t *testing.T) {
	r := newRig(t)
	r.seed(issueKey, "the title", "the description")
	r.clk.Advance(5 * time.Second)

	iss := r.mustShow(`{"id":"ABC-1","fresh":true}`)
	if s, d := r.origin.counts(); s != 1 || d != 1 {
		t.Fatalf("origin calls = %d summary, %d detail, want one each", s, d)
	}
	if iss.ServedFrom != "origin" || iss.Stale {
		t.Errorf("served_from %q stale %v, want origin and not stale", iss.ServedFrom, iss.Stale)
	}
	if iss.AgeSeconds == nil || *iss.AgeSeconds != 0 {
		t.Errorf("age_seconds = %v, want 0 for groups fetched now", iss.AgeSeconds)
	}
	if want := t0.Add(5 * time.Second).Format(time.RFC3339); iss.AsOf != want {
		t.Errorf("as_of = %q, want %q", iss.AsOf, want)
	}
	// A second read inside the windows is a local hit again.
	r.mustShow(`{"id":"ABC-1"}`)
	if s, d := r.origin.counts(); s != 1 || d != 1 {
		t.Errorf("origin calls after a plain read = %d, %d, want still one each", s, d)
	}
}

func TestShowOfAStaleIssueFetchesBothGroups(t *testing.T) {
	r := newRig(t)
	r.seed(issueKey, "the title", "the description")
	r.clk.Advance(summaryTTL + detailTTL)

	iss := r.mustShow(`{"id":"ABC-1"}`)
	if s, d := r.origin.counts(); s != 1 || d != 1 {
		t.Fatalf("origin calls = %d, %d, want one each", s, d)
	}
	if iss.ServedFrom != "origin" {
		t.Errorf("served_from = %q, want origin", iss.ServedFrom)
	}
}

func TestShowOfAnUnknownIssueReadsItThroughTheOrigin(t *testing.T) {
	r := newRig(t)
	r.origin.set(issueOf("XYZ-7", "new one"), "its description")

	iss := r.mustShow(`{"id":"xyz-7"}`)
	if iss.ID != "XYZ-7" || iss.Description != "its description" || iss.ServedFrom != "origin" {
		t.Errorf("issue = %+v", iss)
	}
	if s, d := r.origin.counts(); s != 1 || d != 1 {
		t.Errorf("origin calls = %d, %d, want one each", s, d)
	}
}

func TestShowOfAnIdThatIsNotJiraShapedIsNotFoundWithZeroOriginCalls(t *testing.T) {
	r := newRig(t)
	for _, id := range []string{"pg2-abc", "ABC", "owner/repo#1", "ABC-", `A-1") OR ("B-2`, "123-4"} {
		_, err := r.show(`{"id":` + quote(id) + `}`)
		if !errors.Is(err, scriptout.ErrNotFound) {
			t.Errorf("show %q = %v, want not_found", id, err)
		}
	}
	if s, d := r.origin.counts(); s != 0 || d != 0 {
		t.Errorf("origin calls = %d, %d, want none", s, d)
	}
	if n, m := len(r.summary.calls), len(r.detail.calls); n != 0 || m != 0 {
		t.Errorf("the group services were asked %d and %d times, want none", n, m)
	}
}

func TestShowWithABlankOrMalformedArgumentIsInvalid(t *testing.T) {
	r := newRig(t)
	for _, args := range []string{``, `{}`, `{"id":" "}`, `[1]`, `{"id":"ABC-1","watch":"yes"}`, `{"id":"ABC-1","fresh":"x"}`} {
		if _, err := r.show(args); !errors.Is(err, scriptout.ErrInvalidArgument) {
			t.Errorf("show %q = %v, want invalid_argument", args, err)
		}
	}
}

func TestShowOfAnIssueTheOriginDoesNotKnowIsNotFound(t *testing.T) {
	r := newRig(t)
	if _, err := r.show(`{"id":"ZZZ-9"}`); !errors.Is(err, scriptout.ErrNotFound) {
		t.Errorf("show of an unknown issue = %v, want not_found", err)
	}
}

func TestShowOfAGoneSummaryIsNotFound(t *testing.T) {
	r := newRig(t)
	r.seed(issueKey, "t", "d")
	content, _ := json.Marshal(groups.SummaryContent{Issue: issueOf(issueKey, "t"), Gone: true})
	row, _, _ := r.st.GetGroup(context.Background(), groups.EntityKind, issueKey, groups.Summary)
	row.Content = content
	if err := r.st.PutGroup(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	if _, err := r.show(`{"id":"ABC-1"}`); !errors.Is(err, scriptout.ErrNotFound) {
		t.Errorf("show of a gone issue = %v, want not_found", err)
	}
}

func TestShowDuringAPauseServesAStoredIssueStaleAndAnUnknownOneIsUnavailable(t *testing.T) {
	r := newRig(t)
	r.seed(issueKey, "the title", "the description")
	until := t0.Add(time.Hour)
	r.origin.setErr(&origin.PausedError{Until: until, Reason: "rate limited"})
	r.clk.Advance(summaryTTL + detailTTL + 30*time.Second)

	iss := r.mustShow(`{"id":"ABC-1"}`)
	if !iss.Stale || iss.ServedFrom != "cache" {
		t.Errorf("stale %v served_from %q, want stale cache", iss.Stale, iss.ServedFrom)
	}
	if want := int64((summaryTTL + detailTTL + 30*time.Second) / time.Second); iss.AgeSeconds == nil || *iss.AgeSeconds != want {
		t.Errorf("age_seconds = %v, want %d", iss.AgeSeconds, want)
	}
	if iss.Title != "the title" || iss.Description != "the description" {
		t.Errorf("stale content = %+v", iss)
	}

	_, err := r.show(`{"id":"NEW-5"}`)
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("show of an unknown issue during a pause = %v, want unavailable", err)
	}
	if !strings.Contains(err.Error(), "paused until "+until.Format(time.RFC3339)) {
		t.Errorf("error %q does not name the end of the pause", err)
	}
}

func TestShowNamesThePauseInForceWhenTheFailureDoesNot(t *testing.T) {
	r := newRig(t)
	until := t0.Add(time.Hour)
	r.ops = Handlers(Deps{
		Summary: &fakeSummary{r: r}, Detail: &fakeDetail{r: r}, Store: r.st, Clock: r.clk,
		Pause: fixedPause{until: until, reason: "429"},
	})
	r.origin.setErr(errors.New("connection refused"))
	_, err := r.show(`{"id":"NEW-5"}`)
	if !errors.Is(err, scriptout.ErrUnavailable) || !strings.Contains(err.Error(), "connection refused") ||
		!strings.Contains(err.Error(), "the origin is paused until "+until.Format(time.RFC3339)) {
		t.Errorf("error = %v, want unavailable naming the failure and the pause", err)
	}
}

type fixedPause struct {
	until  time.Time
	reason string
}

func (p fixedPause) Paused() (time.Time, string, bool) { return p.until, p.reason, true }

func TestShowAnAuthenticationFailureIsUnauthenticatedWhenNothingIsStoredAndServedStaleWhenSomethingIs(t *testing.T) {
	r := newRig(t)
	r.seed(issueKey, "the title", "the description")
	cause := errors.New("pjira: status 401 Unauthorized")
	r.auth.MarkUnauthenticated(cause)
	r.origin.setErr(cause)

	_, err := r.show(`{"id":"NEW-5"}`)
	if !errors.Is(err, scriptout.ErrUnauthenticated) || !strings.Contains(err.Error(), "401") {
		t.Errorf("show of an unknown issue while unauthenticated = %v, want unauthenticated naming the failure", err)
	}

	r.clk.Advance(2 * time.Hour)
	iss := r.mustShow(`{"id":"ABC-1"}`)
	if !iss.Stale || iss.Title != "the title" {
		t.Errorf("stored issue while unauthenticated = %+v, want served stale", iss)
	}
	if iss.AgeSeconds == nil || *iss.AgeSeconds != 7200 {
		t.Errorf("age_seconds = %v, want 7200", iss.AgeSeconds)
	}
}

func TestShowRefusesAnAnswerWhoseDetailIsUnavailable(t *testing.T) {
	r := newRig(t)
	r.putSummary(issueOf(issueKey, "t"), t0) // a summary but no detail stored
	r.origin.set(issueOf(issueKey, "t"), "d")
	r.clk.Advance(2 * summaryTTL)
	r.origin.setErr(errors.New("origin down"))
	// The summary is served stale from the store; the detail has nothing stored.
	_, err := r.show(`{"id":"ABC-1"}`)
	if !errors.Is(err, scriptout.ErrUnavailable) || !strings.Contains(err.Error(), "origin down") {
		t.Errorf("show with no detail = %v, want unavailable naming the failure", err)
	}
}

func TestShowRecordsItsAnnotationsOnTheRouterCarrier(t *testing.T) {
	// A context that did not come from the router makes Annotate a no-op, so this
	// only proves the call is safe; the carrier values are pinned by the
	// provenance tests below.
	r := newRig(t)
	r.seed(issueKey, "t", "d")
	if _, err := r.ops[OpShow].Handle(context.Background(), json.RawMessage(`{"id":"ABC-1"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestProvenanceOldestGroupSetsAgeAndAsOf(t *testing.T) {
	now := t0.Add(90 * time.Second)
	p := provenanceOf([]servedGroup{
		{group: "summary", row: rowAt(t0.Add(30 * time.Second))},
		{group: "detail", row: rowAt(t0), stale: true},
		{group: "ignored"}, // never fetched: counts for nothing
	}, now)
	if p.servedFrom != "cache" || !p.stale || p.ageSeconds != 90 || p.asOf != t0.Format(time.RFC3339) || len(p.groups) != 2 {
		t.Errorf("provenance = %+v", p)
	}
	p = provenanceOf([]servedGroup{{group: "summary", row: rowAt(t0), fromOrigin: true}}, t0.Add(-time.Hour))
	if p.servedFrom != "origin" || p.ageSeconds != 0 {
		t.Errorf("provenance = %+v, want origin and a never negative age", p)
	}
	if p = provenanceOf(nil, now); p.asOf != "" || p.ageSeconds != 0 || p.servedFrom != "cache" || p.stale {
		t.Errorf("empty provenance = %+v", p)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestForcedReadsFetchEachGroupOnceWhateverTheOrder(t *testing.T) {
	// A known issue: the forced detail Ensure goes first (it also forces the
	// summary, the primary group) and the summary is then read without the option.
	r := newRig(t)
	r.seed(issueKey, "t", "d")
	r.mustShow(`{"id":"ABC-1","fresh":true}`)
	if len(r.detail.calls) != 1 || !r.detail.calls[0].Fresh || len(r.summary.calls) != 1 || r.summary.calls[0].Fresh {
		t.Errorf("known issue: detail calls %+v, summary calls %+v", r.detail.calls, r.summary.calls)
	}
	if s, d := r.origin.counts(); s != 1 || d != 1 {
		t.Errorf("origin calls = %d, %d, want one each", s, d)
	}

	// An unknown issue: the summary read comes first and is the forced one; the
	// detail it then needs is fetched once, and not forced a second time.
	r = newRig(t)
	r.origin.set(issueOf("XYZ-7", "new"), "d")
	r.mustShow(`{"id":"XYZ-7","fresh":true}`)
	if len(r.summary.calls) != 1 || !r.summary.calls[0].Fresh || len(r.detail.calls) != 1 || r.detail.calls[0].Fresh {
		t.Errorf("unknown issue: summary calls %+v, detail calls %+v", r.summary.calls, r.detail.calls)
	}
	if s, d := r.origin.counts(); s != 1 || d != 1 {
		t.Errorf("origin calls = %d, %d, want one each", s, d)
	}
}

func TestShowWithFreshDuringAPauseServesTheStoredIssueAsStale(t *testing.T) {
	r := newRig(t)
	r.seed(issueKey, "the title", "the description")
	r.origin.setErr(&origin.PausedError{Until: t0.Add(time.Hour), Reason: "rate limited"})
	r.clk.Advance(10 * time.Second) // every group is still inside its window

	iss := r.mustShow(`{"id":"ABC-1","fresh":true}`)
	if !iss.Stale || iss.ServedFrom != "cache" || iss.Title != "the title" {
		t.Errorf("stale %v served_from %q title %q: a forced read that could not reach the origin must say it is stale", iss.Stale, iss.ServedFrom, iss.Title)
	}
	if iss.AgeSeconds == nil || *iss.AgeSeconds != 10 {
		t.Errorf("age_seconds = %v, want 10", iss.AgeSeconds)
	}
}

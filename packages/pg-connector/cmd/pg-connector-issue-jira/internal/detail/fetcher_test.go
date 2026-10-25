package detail

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
)

func decodeItems(t *testing.T, items ...fakeIssue) *internal.PjiraSearchResult {
	t.Helper()
	var js []string
	for _, i := range items {
		js = append(js, i.json())
	}
	res, err := internal.DecodeSearchResult(`{"items":[` + strings.Join(js, ",") + `],"truncated":false}`)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func newFetcher(t *testing.T, h *harness, o Origin, facts Facts) *Fetcher {
	t.Helper()
	return &Fetcher{Origin: o, Store: h.st, Rules: groups.Rules(settings), Facts: facts, Clock: h.clk, BodyCap: 8}
}

func TestFetcherDeclaresOneIssuePerCallAndOneRequestOfTheBucket(t *testing.T) {
	f := &Fetcher{}
	if f.Group() != groups.Detail || f.MaxBatch() != 1 {
		t.Errorf("Group %q MaxBatch %d, want detail and 1", f.Group(), f.MaxBatch())
	}
	for _, n := range []int{1, 5} {
		req := f.Spend(governor.ClassQuery, n)
		if req.Bucket != origin.Bucket || req.Cost != 1 || req.Class != governor.ClassQuery {
			t.Errorf("Spend(3, %d) = %+v, want one request of %s", n, req, origin.Bucket)
		}
	}
	if req := (&Fetcher{Bucket: "other"}).Spend(governor.ClassInteractive, 1); req.Bucket != "other" {
		t.Errorf("Spend with a bucket = %+v", req)
	}
}

func TestFetchRunsOneKeyScopedSearchExpandingChangelogAndCommentsAndStoresTheDetail(t *testing.T) {
	h := newHarness(t)
	long := strings.Repeat("é", 10) // 20 bytes: cut to 8 on a rune boundary
	fo := &fakeOrigin{res: decodeItems(t, fakeIssue{
		Key: issueKey, Category: openCat, Updated: updated1,
		Comments: []fakeComment{
			{ID: "7", Author: "Pat", Body: long, Created: "2026-10-04T10:00:00.000+0000"},
			{ID: "8", Author: "Sam", Body: "short", Created: "2026-10-04T11:00:00.000+0000"},
		},
	})}
	var seen *internal.PjiraIssue
	facts := func(_ context.Context, iss *internal.PjiraIssue) (string, string) {
		seen = iss
		return "2026-10-01T00:00:00Z", "2026-10-02T00:00:00Z"
	}
	f := newFetcher(t, h, fo, facts)

	out := f.Fetch(context.Background(), groupsvc.CallMeta{Class: governor.ClassQuery, Purpose: governor.PurposeRefresh, Group: groups.Detail, BatchSize: 1},
		[]groupsvc.Target{{ID: issueKey, Ref: issueKey}})
	if len(out) != 1 || out[0].Err != nil {
		t.Fatalf("outcomes = %+v", out)
	}
	if fo.calls != 1 || fo.jqls[0] != `key = "A-1"` || strings.Join(fo.exps[0], ",") != "changelog,comments" {
		t.Errorf("search = %d calls, jql %q, expand %v", fo.calls, fo.jqls, fo.exps)
	}
	if seen == nil || seen.Key != issueKey {
		t.Errorf("the facts were computed from %+v, not from the issue as fetched", seen)
	}

	var d groups.DetailContent
	if err := json.Unmarshal(out[0].Content, &d); err != nil {
		t.Fatal(err)
	}
	if d.Description != "" || len(d.Deps) != 0 {
		t.Errorf("description %q deps %v, want both empty", d.Description, d.Deps)
	}
	if d.StatusChangedAt != "2026-10-01T00:00:00Z" || d.OperatorUpdatedAt != "2026-10-02T00:00:00Z" {
		t.Errorf("operator facts = %q, %q", d.StatusChangedAt, d.OperatorUpdatedAt)
	}
	if len(d.Comments) != 2 {
		t.Fatalf("comments = %+v", d.Comments)
	}
	c := d.Comments[0]
	if c.ID != "7" || c.Author != "Pat" || c.Created != "2026-10-04T10:00:00.000+0000" || len(c.Body) != 8 || !strings.HasPrefix(long, c.Body) {
		t.Errorf("comment 0 = %+v, want id 7, author Pat, body cut to 8 bytes", c)
	}
	if d.Comments[1].Body != "short" {
		t.Errorf("comment 1 body = %q", d.Comments[1].Body)
	}
	if want := groups.FingerprintDetail(d, 8); out[0].Fingerprint != want {
		t.Errorf("fingerprint = %q, want the normalised detail's %q", out[0].Fingerprint, want)
	}
	if want := t0.Add(settings.DetailTTL); !out[0].FreshUntil.Equal(want) {
		t.Errorf("FreshUntil = %v, want detail_ttl past now: %v", out[0].FreshUntil, want)
	}
}

func TestFetchFingerprintIgnoresTheOperatorFactsButNotAComment(t *testing.T) {
	h := newHarness(t)
	is := fakeIssue{Key: issueKey, Category: openCat, Updated: updated1, Comments: []fakeComment{{ID: "1", Author: "Pat", Body: "a", Created: "x"}}}
	fp := func(is fakeIssue, changed string) string {
		f := newFetcher(t, h, &fakeOrigin{res: decodeItems(t, is)}, func(context.Context, *internal.PjiraIssue) (string, string) { return changed, "" })
		out := f.Fetch(context.Background(), groupsvc.CallMeta{}, []groupsvc.Target{{ID: issueKey, Ref: issueKey}})
		return out[0].Fingerprint
	}
	base := fp(is, "2026-10-01T00:00:00Z")
	if got := fp(is, "2026-10-09T00:00:00Z"); got != base {
		t.Error("the fingerprint moved with a derived operator fact")
	}
	is.Comments = append(is.Comments, fakeComment{ID: "2", Author: "Pat", Body: "b", Created: "y"})
	if got := fp(is, "2026-10-01T00:00:00Z"); got == base {
		t.Error("the fingerprint did not move with a new comment")
	}
}

func TestFetchWithoutFactsOrCommentsStoresAnEmptyDetail(t *testing.T) {
	h := newHarness(t)
	f := newFetcher(t, h, &fakeOrigin{res: decodeItems(t, fakeIssue{Key: issueKey, Category: openCat, Updated: updated1})}, nil)
	out := f.Fetch(context.Background(), groupsvc.CallMeta{}, []groupsvc.Target{{ID: issueKey, Ref: issueKey}})
	if out[0].Err != nil || string(out[0].Content) != "{}" {
		t.Errorf("outcome = %+v content %s, want an empty detail", out[0], out[0].Content)
	}
}

func TestFetchAnEmptyResultOrAnotherKeyIsGone(t *testing.T) {
	h := newHarness(t)
	for name, res := range map[string]*internal.PjiraSearchResult{
		"empty":       decodeItems(t),
		"another key": decodeItems(t, fakeIssue{Key: otherKey, Category: openCat}),
	} {
		f := newFetcher(t, h, &fakeOrigin{res: res}, nil)
		out := f.Fetch(context.Background(), groupsvc.CallMeta{}, []groupsvc.Target{{ID: issueKey, Ref: issueKey}})
		if !errors.Is(out[0].Err, groupsvc.ErrGone) {
			t.Errorf("%s: err = %v, want ErrGone", name, out[0].Err)
		}
	}
}

func TestFetchMatchesTheKeyInAnyCase(t *testing.T) {
	h := newHarness(t)
	f := newFetcher(t, h, &fakeOrigin{res: decodeItems(t, fakeIssue{Key: "a-1", Category: openCat})}, nil)
	out := f.Fetch(context.Background(), groupsvc.CallMeta{}, []groupsvc.Target{{ID: issueKey, Ref: issueKey}})
	if out[0].Err != nil {
		t.Errorf("err = %v", out[0].Err)
	}
}

func TestFetchRefusesANonKeyWithoutACall(t *testing.T) {
	h := newHarness(t)
	fo := &fakeOrigin{}
	f := newFetcher(t, h, fo, nil)
	for _, ref := range []string{`A-1" OR 1=1`, "nonsense"} {
		out := f.Fetch(context.Background(), groupsvc.CallMeta{}, []groupsvc.Target{{ID: "X", Ref: ref}})
		if out[0].Err == nil {
			t.Errorf("Ref %q was fetched", ref)
		}
	}
	if fo.calls != 0 {
		t.Errorf("origin calls = %d, want 0", fo.calls)
	}
	// An empty reference falls back to the store id.
	fo.res = decodeItems(t, fakeIssue{Key: issueKey, Category: openCat})
	if out := f.Fetch(context.Background(), groupsvc.CallMeta{}, []groupsvc.Target{{ID: issueKey}}); out[0].Err != nil || fo.calls != 1 {
		t.Errorf("a target with only an id: %+v, %d calls", out[0], fo.calls)
	}
}

func TestFetchPassesAnOriginFailureThroughUnchanged(t *testing.T) {
	h := newHarness(t)
	boom := errors.New("pjira: search: boom")
	f := newFetcher(t, h, &fakeOrigin{err: boom}, nil)
	out := f.Fetch(context.Background(), groupsvc.CallMeta{}, []groupsvc.Target{{ID: issueKey, Ref: issueKey}})
	if !errors.Is(out[0].Err, boom) || len(out[0].Content) != 0 {
		t.Errorf("outcome = %+v", out[0])
	}
}

func TestFetchStopsAfterARateLimitAnswer(t *testing.T) {
	h := newHarness(t)
	limited := &origin.CallError{Kind: origin.KindRateLimited, Err: errors.New(text429)}
	fo := &fakeOrigin{err: limited}
	f := newFetcher(t, h, fo, nil)
	out := f.Fetch(context.Background(), groupsvc.CallMeta{}, []groupsvc.Target{{ID: issueKey, Ref: issueKey}, {ID: otherKey, Ref: otherKey}})
	if fo.calls != 1 {
		t.Errorf("calls = %d, want 1: the second target must not be sent into a rate limit", fo.calls)
	}
	for i, o := range out {
		if !origin.IsRateLimited(o.Err) {
			t.Errorf("outcome %d err = %v, want the rate-limit answer", i, o.Err)
		}
	}
}

func TestFetchScheduleOfATerminalIssueIsTheTerminalTTL(t *testing.T) {
	h := newHarness(t)
	h.persist(summaryOf(issueKey, closedCat, "closed", updated1)) // closed_at = t0
	fo := &fakeOrigin{res: decodeItems(t, fakeIssue{Key: issueKey, Category: closedCat, Updated: updated1})}
	f := newFetcher(t, h, fo, nil)
	fetch := func() groupsvc.Outcome {
		return f.Fetch(context.Background(), groupsvc.CallMeta{}, []groupsvc.Target{{ID: issueKey, Ref: issueKey}})[0]
	}
	if got := fetch(); !got.FreshUntil.Equal(t0.Add(settings.DetailTTL)) {
		t.Errorf("closed for less than terminal_ttl: FreshUntil = %v, want the normal detail_ttl", got.FreshUntil)
	}
	h.clk.Advance(25 * time.Hour)
	if got := fetch(); !got.FreshUntil.Equal(h.clk.Now().Add(settings.TerminalTTL)) {
		t.Errorf("closed for 25 hours: FreshUntil = %v, want now + terminal_ttl", got.FreshUntil)
	}
}

func TestFetchThroughTheServiceSpendsOneInteractiveRequestOfGroupDetailAndStoresIt(t *testing.T) {
	h := newHarness(t)
	h.fake.set(fakeIssue{
		Key: issueKey, Category: openCat, Updated: updated1, Assignee: operator,
		Comments: []fakeComment{{ID: "1", Author: "Pat", Body: "x", Created: "2026-10-04T10:00:00.000+0000"}},
	})
	h.persist(summaryOf(issueKey, openCat, "t", updated1))
	h.drain() // the first-sighting baseline fetch

	sp := h.spendList()
	if len(sp) != 1 || sp[0].Group != groups.Detail || sp[0].BatchSize != 1 || sp[0].Bucket != origin.Bucket || sp[0].Cost != 1 {
		t.Fatalf("spend = %+v, want one request of group detail, batch size 1", sp)
	}
	if h.fake.count() != 1 {
		t.Fatalf("pjira calls = %d, want 1", h.fake.count())
	}
	args := h.fake.call(0)
	if strings.Join(args, " ") != `search --jql key = "A-1" --all --expand changelog,comments` {
		t.Errorf("pjira args = %v", args)
	}
	d := h.detailContent(issueKey)
	if len(d.Comments) != 1 || d.Comments[0].Author != "Pat" {
		t.Errorf("stored detail = %+v", d)
	}
	// The issue is assigned to the operator, so both facts are computed from the
	// fetched issue (it has no changelog: the entry time is its creation).
	if d.StatusChangedAt != "2026-09-01T00:00:00Z" {
		t.Errorf("status_changed_at = %q, want the creation time", d.StatusChangedAt)
	}
	if row, _ := h.detailRow(issueKey); row.Fingerprint == "" || row.FetchedAt != t0 {
		t.Errorf("row = %+v", row)
	}
}

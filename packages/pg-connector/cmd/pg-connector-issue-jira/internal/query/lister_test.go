package query

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	lq "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var background = lq.CallMeta{Class: governor.ClassQuery, Purpose: governor.PurposeQuery}

// listerOf is a lister over the harness's real origin runner, persister and
// store. A Sleep that advances the manual clock stands in for the wait.
func (h *harness) listerOf(slept *[]time.Duration) *Lister {
	return NewLister(ListerConfig{
		Fetcher: h.origin, Persister: h.pers, Store: h.st, Clock: h.clk,
		Sleep: func(_ context.Context, d time.Duration) error {
			*slept = append(*slept, d)
			h.clk.Advance(d)
			return nil
		},
	})
}

func TestListerSearchesEachExpressionOnceUnionsAndPersistsBeforeReturning(t *testing.T) {
	h := newHarness(t, 100)
	h.fake.setIssue("A-1", "indeterminate")
	h.fake.setIssue("A-2", "new")
	h.fake.setIssue("B-3", "done")
	h.fake.list("jql one", "A-1", "A-2")
	h.fake.list("jql two", "A-2", "B-3")
	var slept []time.Duration

	got, err := h.listerOf(&slept).List(context.Background(), lq.Key{Name: "x"}, []string{"jql one", " jql two "}, background)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Complete {
		t.Error("Complete = false for two whole searches")
	}
	var ids []string
	for _, m := range got.Members {
		if m.ID != m.Ref {
			t.Errorf("member %+v: the ref is the issue key and the id its fold", m)
		}
		ids = append(ids, m.ID)
	}
	if !eq(ids, []string{"A-1", "A-2", "B-3"}) {
		t.Errorf("members = %v, want the union deduplicated by key, in order", ids)
	}
	calls := h.fake.callList()
	if len(calls) != 2 {
		t.Fatalf("origin calls = %v, want one search per expression", calls)
	}
	for i, want := range []string{"jql one", "jql two"} {
		c := calls[i]
		if c[2] != want || strings.Join(c[3:], " ") != "--all --expand changelog,comments" {
			t.Errorf("call %d = %v", i, c)
		}
	}
	for _, id := range ids {
		g, ok, err := h.st.GetGroup(context.Background(), groups.EntityKind, id, groups.Summary)
		if err != nil || !ok || len(g.Content) == 0 {
			t.Errorf("%s was not persisted before List returned (ok=%v err=%v)", id, ok, err)
		}
	}
	if len(slept) != 0 {
		t.Errorf("waited %v with the budget free", slept)
	}
}

func TestListerNeverReportsATruncatedListingComplete(t *testing.T) {
	h := newHarness(t, 100)
	h.fake.setIssue("A-1", "indeterminate")
	h.fake.setIssue("A-2", "indeterminate")
	h.fake.list("jql one", "A-1")
	h.fake.list("jql two", "A-2")
	h.fake.setTruncated("jql two", true)
	var slept []time.Duration

	got, err := h.listerOf(&slept).List(context.Background(), lq.Key{Name: "x"}, []string{"jql one", "jql two"}, background)
	if err != nil {
		t.Fatal(err)
	}
	if got.Complete {
		t.Error("a listing with a truncated search was reported complete")
	}
	if len(got.Members) != 2 {
		t.Errorf("members = %v, want what was returned", got.Members)
	}
}

func TestListerWaitsOutAGovernorRefusalAndRetriesThatExpression(t *testing.T) {
	// Rate 2 per second leaves the background classes one request per second,
	// so the second expression is refused until the first spend ages out.
	h := newHarness(t, 2)
	h.fake.setIssue("A-1", "indeterminate")
	h.fake.setIssue("A-2", "indeterminate")
	h.fake.list("jql one", "A-1")
	h.fake.list("jql two", "A-2")
	var slept []time.Duration

	got, err := h.listerOf(&slept).List(context.Background(), lq.Key{Name: "x"}, []string{"jql one", "jql two"}, background)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Complete || len(got.Members) != 2 {
		t.Errorf("listing = %+v, want both expressions listed", got)
	}
	if len(slept) != 1 || slept[0] <= 0 || slept[0] > time.Second {
		t.Errorf("waited %v, want one wait of at most the one-second window", slept)
	}
	calls := h.fake.callList()
	if len(calls) != 2 || calls[0][2] != "jql one" || calls[1][2] != "jql two" {
		t.Errorf("origin calls = %v, want each expression searched once (the refused attempt never reaches pjira)", calls)
	}
}

type refusingSearcher struct {
	retryAt time.Time
	calls   int
}

func (r *refusingSearcher) Search(context.Context, string, []string) (*internal.PjiraSearchResult, error) {
	r.calls++
	return nil, &governor.Refusal{Kind: governor.RefusedCap, Reason: "cap spent", RetryAt: r.retryAt}
}

func TestListerWaitIsBoundedByTheContext(t *testing.T) {
	h := newHarness(t, 100)
	s := &refusingSearcher{retryAt: h.clk.Now().Add(time.Hour)}
	l := NewLister(ListerConfig{Fetcher: s, Persister: h.pers, Clock: h.clk})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := l.List(ctx, lq.Key{Name: "x"}, []string{"jql"}, background)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("List = %v, want the context's error", err)
	}
	if s.calls != 1 {
		t.Errorf("searches = %d, want 1 (then the wait)", s.calls)
	}
}

func TestListerReturnsARefusalThatStatesNoRetryTime(t *testing.T) {
	h := newHarness(t, 100)
	s := &refusingSearcher{}
	l := NewLister(ListerConfig{Fetcher: s, Persister: h.pers, Clock: h.clk})
	_, err := l.List(context.Background(), lq.Key{Name: "x"}, []string{"jql"}, background)
	var ref *governor.Refusal
	if !errors.As(err, &ref) || s.calls != 1 {
		t.Errorf("List = %v after %d searches, want the refusal at once", err, s.calls)
	}
}

func TestListerDoesNotSpinOnARefusalNamingThePast(t *testing.T) {
	h := newHarness(t, 100)
	s := &refusingSearcher{retryAt: h.clk.Now().Add(-time.Second)}
	l := NewLister(ListerConfig{Fetcher: s, Persister: h.pers, Clock: h.clk})
	_, err := l.List(context.Background(), lq.Key{Name: "x"}, []string{"jql"}, background)
	if err == nil || s.calls != 2 {
		t.Errorf("List = %v after %d searches, want one immediate retry and then the refusal", err, s.calls)
	}
}

func TestListerPropagatesAnOriginFailure(t *testing.T) {
	h := newHarness(t, 100)
	h.fake.setErr(errors.New("boom"))
	var slept []time.Duration
	_, err := h.listerOf(&slept).List(context.Background(), lq.Key{Name: "x"}, []string{"jql"}, background)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("List = %v, want the origin failure", err)
	}
}

func TestListerRefusesAnAdHocKey(t *testing.T) {
	h := newHarness(t, 100)
	var slept []time.Duration
	_, err := h.listerOf(&slept).List(context.Background(), lq.Key{Name: "x", Args: map[string]string{"a": "b"}}, []string{"jql"}, background)
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Errorf("List(ad-hoc) = %v, want invalid_argument", err)
	}
	if n := len(h.fake.callList()); n != 0 {
		t.Errorf("origin calls = %d, want none", n)
	}
}

func TestListerCallIsAdmittedAsTheRunnerAsksAndGroupedAsSummary(t *testing.T) {
	var got []governor.SpendEvent
	h := newHarness(t, 100)
	gov, err := governor.New(governorConfigOf(h), governor.Options{Clock: h.clk, OnSpend: func(e governor.SpendEvent) { got = append(got, e) }})
	if err != nil {
		t.Fatal(err)
	}
	o := newOriginOver(t, h, gov)
	h.fake.setIssue("A-1", "indeterminate")
	h.fake.list("jql", "A-1")
	l := NewLister(ListerConfig{Fetcher: o, Persister: h.pers, Clock: h.clk})
	if _, err := l.List(context.Background(), lq.Key{Name: "x"}, []string{"jql"}, background); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Class != governor.ClassQuery || got[0].Purpose != governor.PurposeQuery || got[0].Group != groups.Summary {
		t.Errorf("spend = %+v, want one class 3 query spend of group summary", got)
	}
}

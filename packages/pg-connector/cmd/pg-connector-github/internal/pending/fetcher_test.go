package pending

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/metered"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

var t0 = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

// fakeGH is the gh the metered runner runs: it records every call and answers
// the pending-review lookup from reply.
type fakeGH struct {
	mu    sync.Mutex
	calls [][]string
	reply func(args []string) ([]byte, error)
}

func (f *fakeGH) Run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), args...))
	reply := f.reply
	f.mu.Unlock()
	return reply(args)
}

func (f *fakeGH) RunStdin(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	return f.Run(ctx, args...)
}

func (f *fakeGH) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// answer is a main lookup response for a PR with no pending review and a head.
func answer(head string, pending ...map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"data": map[string]any{
		"viewer":    map[string]any{"login": "octo-one"},
		"rateLimit": map[string]any{"cost": 1, "remaining": 4990, "resetAt": "2026-10-12T13:00:00Z"},
		"repository": map[string]any{"pullRequest": map[string]any{
			"headRefOid":    head,
			"pending":       page(pending),
			"submitted":     page(nil),
			"reviewThreads": page(nil),
		}},
	}})
	return b
}

func page(nodes []map[string]any) map[string]any {
	if nodes == nil {
		nodes = []map[string]any{}
	}
	return map[string]any{"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""}, "nodes": nodes}
}

func reviewNode(dbID int64, commit string, comments ...map[string]any) map[string]any {
	if comments == nil {
		comments = []map[string]any{}
	}
	return map[string]any{
		"id": fmt.Sprintf("PRR_%d", dbID), "databaseId": dbID, "url": "https://example.invalid/r", "state": "PENDING",
		"author": map[string]any{"login": "octo-one"}, "commit": map[string]any{"oid": commit}, "body": "b",
		"comments": page(comments),
	}
}

func commentNode(id, at string) map[string]any {
	return map[string]any{"id": id, "databaseId": 1, "path": "a.go", "line": 3, "originalLine": 3, "body": "x", "originalCommit": map[string]any{"oid": at}}
}

type harness struct {
	t       *testing.T
	gov     *governor.Governor
	gh      *fakeGH
	fetcher *Fetcher
	events  []governor.SpendEvent
}

func newHarness(t *testing.T, reply func(args []string) ([]byte, error)) *harness {
	t.Helper()
	clock := governor.NewManualClock(t0)
	h := &harness{t: t, gh: &fakeGH{reply: reply}}
	g, err := governor.New(ratefeed.GovernorConfig(config.Defaults().Settings), governor.Options{
		Clock:   clock,
		OnSpend: func(e governor.SpendEvent) { h.events = append(h.events, e) },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.gov = g
	runner := &metered.Runner{Inner: h.gh, Governor: g, Feed: ratefeed.New(g, h.gh, clock)}
	h.fetcher = &Fetcher{Reader: metered.NewProvider(runner)}
	return h
}

func target(id string, n int) groups.Target {
	return groups.Target{
		NodeID: id, Ref: entity.Ref{Owner: "foo", Repo: "bar", Number: n},
		Stamps: groups.Stamps{Head: "headA", Base: "base1"},
	}
}

var refresh = ratefeed.Meta{Class: governor.ClassInvalidated, Purpose: governor.PurposeRefresh}

func TestFetcherIsPerPRAndSpendsOneGraphQLPointPerTarget(t *testing.T) {
	f := &Fetcher{}
	if f.Group() != groups.Pending || f.MaxBatch() != 1 {
		t.Errorf("Group = %q MaxBatch = %d", f.Group(), f.MaxBatch())
	}
	for _, n := range []int{1, 3} {
		got := f.Spend(governor.ClassInteractive, n)
		if got.Bucket != governor.BucketGraphQL || got.Class != governor.ClassInteractive || got.Cost != int64(n) {
			t.Errorf("Spend(%d) = %+v", n, got)
		}
	}
}

func TestFetchReadsOnceAndTheStoredContentDecodesBack(t *testing.T) {
	h := newHarness(t, func([]string) ([]byte, error) {
		return answer("headA", reviewNode(7, "headA", commentNode("c1", "headA"), commentNode("c2", "headOld"))), nil
	})
	outs := h.fetcher.Fetch(context.Background(), refresh, []groups.Target{target("PR_1", 42)})
	if len(outs) != 1 || outs[0].Err != nil {
		t.Fatalf("outs = %+v", outs)
	}
	if h.gh.count() != 1 {
		t.Fatalf("gh ran %d times, want one call for the one target", h.gh.count())
	}
	// The one call was admitted and booked under the call the engine stated.
	if len(h.events) != 1 || h.events[0].Bucket != governor.BucketGraphQL || h.events[0].Class != governor.ClassInvalidated || h.events[0].Group != groups.Pending {
		t.Fatalf("events = %+v, want one GraphQL spend of the pending group at class 2", h.events)
	}
	if bs, _ := h.gov.State().Bucket(governor.BucketGraphQL); bs.Spent != 1 || bs.InFlight != 0 {
		t.Errorf("graphql = %+v, want 1 point spent", bs)
	}
	o := outs[0]
	if o.Stamps != (groups.Stamps{Head: "headA", Base: "base1"}) {
		t.Errorf("stamps = %+v, want the target's from before the call", o.Stamps)
	}
	if o.Fingerprint != groups.Fingerprint(o.Content) || o.Fingerprint == "" {
		t.Errorf("fingerprint = %q", o.Fingerprint)
	}
	data, err := Decode(o.Content)
	if err != nil {
		t.Fatal(err)
	}
	if data.HeadSHA != "headA" || len(data.Reviews) != 1 || data.Reviews[0].DatabaseID != 7 || len(data.Reviews[0].Comments) != 2 ||
		data.Reviews[0].Comments[1].OriginalCommitOID != "headOld" {
		t.Fatalf("decoded = %+v", data)
	}
	// And it computes, end to end.
	res, err := Compute(data, "", "foo/bar", 42, t0, nil)
	if err != nil || !res.Pending || res.Review.CommentsAtHead != 1 || res.Review.Stale {
		t.Errorf("Compute = %+v, %v", res, err)
	}
	args := strings.Join(h.gh.calls[0], " ")
	for _, want := range []string{"api graphql", "owner=foo", "name=bar", "number=42"} {
		if !strings.Contains(args, want) {
			t.Errorf("call args missing %q: %s", want, args)
		}
	}
}

func TestFetchMakesOneCallPerTargetInOrder(t *testing.T) {
	h := newHarness(t, func(args []string) ([]byte, error) {
		head := "headOne"
		if strings.Contains(strings.Join(args, " "), "number=2") {
			head = "headTwo"
		}
		return answer(head), nil
	})
	outs := h.fetcher.Fetch(context.Background(), refresh, []groups.Target{target("PR_1", 1), target("PR_2", 2)})
	if h.gh.count() != 2 || len(h.events) != 2 {
		t.Fatalf("%d gh calls and %d booked spends, want 2 and 2", h.gh.count(), len(h.events))
	}
	for i, want := range []string{"headOne", "headTwo"} {
		d, err := Decode(outs[i].Content)
		if outs[i].Err != nil || err != nil || d.HeadSHA != want {
			t.Errorf("outs[%d] = %+v (%v), want head %s", i, outs[i], err, want)
		}
	}
}

func TestFetchTurnsANotFoundAnswerIntoErrGone(t *testing.T) {
	for _, text := range []string{
		"gh api graphql: exit status 1: GraphQL: Could not resolve to a PullRequest with the number of 42. (repository.pullRequest)",
		"gh api graphql: exit status 1: gh: Not Found (HTTP 404)",
	} {
		h := newHarness(t, func([]string) ([]byte, error) { return nil, errors.New(text) })
		outs := h.fetcher.Fetch(context.Background(), refresh, []groups.Target{target("PR_1", 42)})
		if !errors.Is(outs[0].Err, groups.ErrGone) || outs[0].Content != nil {
			t.Errorf("%q: outcome = %+v, want ErrGone and no content", text, outs[0])
		}
	}
}

func TestFetchReturnsAFailureAndAGovernorRefusalUntouched(t *testing.T) {
	boom := errors.New("gh api graphql: exit status 1: error connecting to api.github.com")
	h := newHarness(t, func([]string) ([]byte, error) { return nil, boom })
	h.fetcher.Reader = metered.NewProvider(&metered.Runner{Inner: h.gh, Governor: h.gov, Feed: ratefeed.New(h.gov, h.gh, governor.NewManualClock(t0))}).
		WithRetryPolicy(github.RetryPolicy{MaxAttempts: 1})
	outs := h.fetcher.Fetch(context.Background(), refresh, []groups.Target{target("PR_1", 42)})
	if outs[0].Err == nil || errors.Is(outs[0].Err, groups.ErrGone) || !strings.Contains(outs[0].Err.Error(), "error connecting") {
		t.Errorf("a transport failure = %v, want it as it came", outs[0].Err)
	}

	// A paused governor refuses the keep-alive class: nothing runs and the
	// refusal is the outcome's error, the rest of the batch carrying it too.
	h = newHarness(t, func([]string) ([]byte, error) { return answer("headA"), nil })
	h.gov.PauseAll(time.Minute, "secondary rate limit (HTTP 403, retry after 1m0s)")
	keepAlive := ratefeed.Meta{Class: governor.ClassKeepAlive, Purpose: governor.PurposeRefresh}
	outs = h.fetcher.Fetch(context.Background(), keepAlive, []groups.Target{target("PR_1", 1), target("PR_2", 2)})
	for i, o := range outs {
		var refusal *governor.Refusal
		if !errors.As(o.Err, &refusal) || refusal.Kind != governor.RefusedPaused || errors.Is(o.Err, groups.ErrGone) {
			t.Errorf("outs[%d].Err = %v, want the paused refusal untouched", i, o.Err)
		}
	}
	if h.gh.count() != 0 {
		t.Errorf("gh ran %d times under a refusal", h.gh.count())
	}
}

// A cancelled context stops the batch without a call.
func TestFetchStopsOnACancelledContext(t *testing.T) {
	h := newHarness(t, func([]string) ([]byte, error) { return answer("headA"), nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outs := h.fetcher.Fetch(ctx, refresh, []groups.Target{target("PR_1", 1)})
	if !errors.Is(outs[0].Err, context.Canceled) || h.gh.count() != 0 {
		t.Errorf("outcome = %+v, %d calls", outs[0], h.gh.count())
	}
}

// The group's rules: never refreshed in the background, fresh until pending_ttl
// (2 minutes by default) and stale after it or after an invalidation.
func TestRulesKeepPendingOutOfTheBackgroundAndExpireItAfterPendingTTL(t *testing.T) {
	rules := groups.NewRules(config.Defaults().Settings)
	if rules.Background(groups.Pending) {
		t.Fatal("the pending group is refreshed in the background")
	}
	if got := rules.TTL(groups.Pending); got != 2*time.Minute {
		t.Fatalf("pending TTL = %v, want 2m", got)
	}
	freshUntil, maxAt := rules.Schedule(groups.Pending, false, t0)
	row := store.GroupRow{
		Kind: entity.KindPR, ID: "PR_1", Group: groups.Pending, Content: []byte("{}"),
		FetchedAt: t0, FreshUntil: freshUntil, MaxAgeAt: maxAt, FetchedForHead: "headA", FetchedForBase: "base1",
	}
	cur := groups.Stamps{Head: "headA", Base: "base1"}
	if got := rules.Fresh(row, cur, t0.Add(119*time.Second)); !got.IsFresh() {
		t.Errorf("at 119s: %v, want fresh", got.Level)
	}
	if got := rules.Fresh(row, cur, t0.Add(121*time.Second)); got.IsFresh() {
		t.Errorf("at 121s: %v, want stale after pending_ttl", got.Level)
	}
}

// An invalidation (what a review_submit does) makes a fresh stored pending row
// stale at once, keeping its content.
func TestAnInvalidationMakesAFreshPendingRowStale(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	rules := groups.NewRules(config.Defaults().Settings)
	freshUntil, maxAt := rules.Schedule(groups.Pending, false, t0)
	tok, err := groups.Begin(ctx, st, entity.KindPR, "PR_1", groups.Pending)
	if err != nil {
		t.Fatal(err)
	}
	row := store.GroupRow{Content: []byte(`{"HeadSHA":"headA"}`), FetchedAt: t0, FreshUntil: freshUntil, MaxAgeAt: maxAt, FetchedForHead: "headA", FetchedForBase: "base1"}
	if ok, err := groups.Commit(ctx, st, tok, row); err != nil || !ok {
		t.Fatalf("Commit = %v, %v", ok, err)
	}
	cur := groups.Stamps{Head: "headA", Base: "base1"}
	read := func(now time.Time) groups.Freshness {
		g, ok, err := st.GetGroup(ctx, entity.KindPR, "PR_1", groups.Pending)
		if err != nil || !ok {
			t.Fatalf("GetGroup = %v, %v", ok, err)
		}
		return rules.Fresh(g, cur, now)
	}
	if !read(t0.Add(30 * time.Second)).IsFresh() {
		t.Fatal("the row is not fresh before pending_ttl")
	}
	if err := groups.Invalidate(ctx, st, entity.KindPR, "PR_1", groups.Pending, t0.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := read(t0.Add(31 * time.Second)); got.IsFresh() {
		t.Errorf("after Invalidate: %v, want stale", got.Level)
	}
}

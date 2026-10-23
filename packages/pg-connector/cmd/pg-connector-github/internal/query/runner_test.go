package query

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/vcs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// call is one gh call the fake saw.
type call struct {
	q, after string
}

// fakeGH answers the membership document from canned fixtures and records every
// call. answer maps (q, after) to a body and error. answer is read by the
// scheduler goroutine inside Run, so a test that swaps it while the scheduler
// loop is running MUST go through setAnswer, which takes mu.
type fakeGH struct {
	mu     sync.Mutex
	calls  []call
	args   [][]string
	answer func(c call) ([]byte, error)
}

func (g *fakeGH) Run(_ context.Context, args ...string) ([]byte, error) {
	var c call
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-f" && strings.HasPrefix(args[i+1], "q=") {
			c.q = strings.TrimPrefix(args[i+1], "q=")
		}
		if args[i] == "-f" && strings.HasPrefix(args[i+1], "after=") {
			c.after = strings.TrimPrefix(args[i+1], "after=")
		}
	}
	g.mu.Lock()
	g.calls = append(g.calls, c)
	g.args = append(g.args, slices.Clone(args))
	answer := g.answer
	g.mu.Unlock()
	return answer(c)
}

// setAnswer replaces the canned answer under mu. The lock publishes everything
// the new closure captures to the goroutine that next calls Run, so the closure
// and its captured data need no further synchronization.
func (g *fakeGH) setAnswer(answer func(c call) ([]byte, error)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.answer = answer
}

func (g *fakeGH) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

func (g *fakeGH) callList() []call {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.calls)
}

// members of the canned fixtures: node id to the PR it names.
var canned = map[string]struct {
	repo   string
	number int
}{
	"PR_a": {"Acme-Org/Widget", 3},
	"PR_b": {"acme-org/gadget", 7},
	"PR_c": {"Acme-Org/Widget", 1},
}

// countingFetcher is the summary fetcher fake: it records every batch and
// answers each node id with its canned pull request.
type countingFetcher struct {
	mu    sync.Mutex
	calls [][]string
	// states overrides the canned State of a node id (a closed or merged PR).
	states map[string]string
}

func (f *countingFetcher) Fetch(_ context.Context, _ ratefeed.Meta, ids []string) ([]summary.Summary, int64, error) {
	f.mu.Lock()
	f.calls = append(f.calls, slices.Clone(ids))
	f.mu.Unlock()
	out := make([]summary.Summary, len(ids))
	for i, id := range ids {
		c, ok := canned[id]
		if !ok {
			out[i] = summary.Summary{NodeID: id, Gone: true}
			continue
		}
		state := "OPEN"
		if st, ok := f.states[id]; ok {
			state = st
		}
		out[i] = summary.Summary{NodeID: id, Repo: c.repo, Number: c.number, State: state, Mergeable: "MERGEABLE", UpdatedAt: "2026-10-12T08:00:00Z"}
	}
	return out, 1, nil
}

func (f *countingFetcher) fetched() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var all []string
	for _, c := range f.calls {
		all = append(all, c...)
	}
	slices.Sort(all)
	return all
}

// fakeAuth is the typed unauthenticated state.
type fakeAuth struct {
	mu  sync.Mutex
	bad error
}

func (a *fakeAuth) Observe(err error) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case err == nil:
		a.bad = nil
	case errors.Is(err, vcs.ErrAuthInvalid):
		a.bad = err
		return true
	}
	return false
}

func (a *fakeAuth) Unauthenticated() (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.bad != nil, a.bad
}

type rig struct {
	st     *store.Store
	clk    *governor.ManualClock
	gov    *governor.Governor
	sched  *scheduler.Scheduler
	svc    *summary.Service
	gh     *fakeGH
	fetch  *countingFetcher
	runner *Runner
	auth   *fakeAuth
	cfg    *config.Config
	id     *entity.Identity
}

func newRig(t *testing.T) *rig { return newRigWith(t) }

// newRigWith is newRig with each opt applied to the runner's Config before the
// runner is built.
func newRigWith(t *testing.T, opts ...func(*Config)) *rig {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := governor.NewManualClock(t0)
	cfg := config.Defaults()
	cfg.PR.Queries = map[string]schema.QueryExpr{
		"mine": {"author:phillipgreenii is:open"},
		"team": {"org:acme-org is:open", "org:acme-org is:draft"},
	}
	gov, err := governor.New(ratefeed.GovernorConfig(cfg.Settings), governor.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	gh := &fakeGH{answer: func(call) ([]byte, error) { return fixture(t, "search_three.json"), nil }}
	sched := scheduler.New(scheduler.Options{Clock: clk, Gate: gov, Workers: 2, PollInterval: 5 * time.Millisecond, RetryDelay: time.Minute})
	id := entity.NewIdentity(st, clk)
	pers := &summary.Persister{Store: st, Identity: id, TTL: time.Minute, TerminalTTL: time.Hour, Clock: clk}
	fetch := &countingFetcher{}
	svc, err := summary.NewService(summary.ServiceConfig{
		Store: st, Persister: pers, Fetcher: fetch, Identity: id, Scheduler: sched, Clock: clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	auth := &fakeAuth{}
	rc := Config{
		Settings: cfg, Store: st, Identity: id, Tracker: svc, GH: gh, Governor: gov,
		Feed: ratefeed.New(gov, gh, clk), Scheduler: sched, Auth: auth, Clock: clk,
	}
	for _, o := range opts {
		o(&rc)
	}
	runner, err := New(rc)
	if err != nil {
		t.Fatal(err)
	}
	return &rig{st: st, clk: clk, gov: gov, sched: sched, svc: svc, gh: gh, fetch: fetch, runner: runner, auth: auth, cfg: cfg, id: id}
}

// run starts the scheduler loop for the test.
func (r *rig) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.sched.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func (r *rig) row(t *testing.T, key string) store.QueryRow {
	t.Helper()
	row, ok, err := r.st.GetQuery(context.Background(), key)
	if err != nil || !ok {
		t.Fatalf("query row %q: ok=%v err=%v", key, ok, err)
	}
	return row
}

func TestMembershipDocument_Pinned(t *testing.T) {
	// The cost of this document was measured at 1 point per page of 100
	// (pg2-cw6b3.1, "Measured cost per call shape"); any change to it needs a
	// new measurement first.
	const want = `
query($q: String!, $after: String) {
  rateLimit { cost remaining resetAt }
  search(query: $q, type: ISSUE, first: 100, after: $after) {
    issueCount
    pageInfo { hasNextPage endCursor }
    nodes {
      ... on PullRequest { id number repository { nameWithOwner } }
    }
  }
}
`
	if membershipDocument != want {
		t.Errorf("membership document changed; re-measure its cost before updating this test:\n%s", membershipDocument)
	}
	if PageSize != 100 || !strings.Contains(membershipDocument, fmt.Sprintf("first: %d", PageSize)) {
		t.Errorf("PageSize %d does not match the document", PageSize)
	}
}

func TestKey_QualifiersAreStableAndQuoted(t *testing.T) {
	a := Key{Name: "mine", Args: map[string]string{"label": "bug", "base": "main"}}
	b := Key{Name: "mine", Args: map[string]string{"base": "main", "label": "bug"}}
	if qualifiers(a) != qualifiers(b) || qualifiers(a) != " base:main label:bug" {
		t.Errorf("qualifiers %q and %q differ or are not canonical", qualifiers(a), qualifiers(b))
	}
	if got := qualifiers(Key{Name: "x", Args: map[string]string{"label": "needs work"}}); got != ` label:"needs work"` {
		t.Errorf("qualifiers = %q", got)
	}
	if got := qualifiers(Key{Name: "x"}); got != "" {
		t.Errorf("qualifiers of a configured key = %q, want none", got)
	}
}

func TestFetch_RunsTheMembershipFormAndStoresTheOrderedIDs(t *testing.T) {
	r := newRig(t)
	r.run(t)
	row, err := r.runner.Fetch(context.Background(), Key{Name: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Acme-Org/Widget#3", "acme-org/gadget#7", "Acme-Org/Widget#1"}
	if !slices.Equal(row.IDs, want) || !row.Complete || !row.Configured || row.Key != "mine" {
		t.Errorf("row = %+v", row)
	}
	if !row.FetchedAt.Equal(t0) || !row.LastSuccessAt.Equal(t0) || !row.FreshUntil.Equal(t0.Add(60*time.Second)) || row.LastError != "" {
		t.Errorf("times = fetched %v success %v fresh %v err %q", row.FetchedAt, row.LastSuccessAt, row.FreshUntil, row.LastError)
	}
	if stored := r.row(t, "mine"); !slices.Equal(stored.IDs, want) {
		t.Errorf("stored ids = %v", stored.IDs)
	}
	calls := r.gh.callList()
	if len(calls) != 1 || calls[0].q != "is:pr author:phillipgreenii is:open" || calls[0].after != "" {
		t.Errorf("gh calls = %+v", calls)
	}
	joined := strings.Join(r.gh.args[0], " ")
	if !strings.Contains(joined, "api graphql") || !strings.Contains(joined, "first: 100") {
		t.Errorf("args = %v", r.gh.args[0])
	}
	if bs, _ := r.gov.State().Bucket(governor.BucketGraphQL); bs.Spent != 1 {
		t.Errorf("one membership page must spend 1 point, spent %d", bs.Spent)
	}
	// The aliases make the members addressable by node id.
	ref, ok, err := entity.NewIdentity(r.st, r.clk).Ref(context.Background(), "PR_b")
	if err != nil || !ok || ref.String() != "acme-org/gadget#7" {
		t.Errorf("alias of PR_b = %v %v %v", ref, ok, err)
	}
}

func TestRun_PagesAndUnionsExpressionsInOrder(t *testing.T) {
	r := newRig(t)
	r.gh.answer = func(c call) ([]byte, error) {
		switch {
		case strings.Contains(c.q, "is:open") && c.after == "":
			return fixture(t, "search_page1.json"), nil
		case strings.Contains(c.q, "is:open"):
			return fixture(t, "search_page2.json"), nil
		default: // the second expression lists PR_c first, then PR_a
			return []byte(`{"data":{"rateLimit":{"cost":1,"remaining":4800,"resetAt":"2026-10-12T10:00:00Z"},"search":{"issueCount":2,"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"id":"PR_c","number":1,"repository":{"nameWithOwner":"Acme-Org/Widget"}},{"id":"PR_a","number":3,"repository":{"nameWithOwner":"Acme-Org/Widget"}}]}}}`), nil
		}
	}
	r.run(t)
	row, err := r.runner.Fetch(context.Background(), Key{Name: "team"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Acme-Org/Widget#3", "acme-org/gadget#7", "Acme-Org/Widget#1"}
	if !slices.Equal(row.IDs, want) || !row.Complete {
		t.Errorf("ids = %v complete = %v, want %v true", row.IDs, row.Complete, want)
	}
	calls := r.gh.callList()
	if len(calls) != 3 || calls[1].after != "cursor-1" {
		t.Errorf("calls = %+v, want page 1, page 2 (after cursor-1) and the second expression", calls)
	}
	if bs, _ := r.gov.State().Bucket(governor.BucketGraphQL); bs.Spent != 3 {
		t.Errorf("3 pages must spend 3 points, spent %d", bs.Spent)
	}
}

func TestRun_AListingPastTheSearchLimitIsNotComplete(t *testing.T) {
	r := newRig(t)
	r.gh.answer = func(call) ([]byte, error) { return fixture(t, "search_over_limit.json"), nil }
	r.run(t)
	row, err := r.runner.Fetch(context.Background(), Key{Name: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	if row.Complete || len(row.IDs) != 1 {
		t.Errorf("row = %+v, want the ids read with complete false", row)
	}
	if !r.row(t, "mine").LastSuccessAt.Equal(t0) || r.row(t, "mine").Complete {
		t.Error("the stored row must carry complete false too")
	}
}

func TestRun_MorePagesThanTheLimitAllowsIsNotComplete(t *testing.T) {
	r := newRig(t)
	r.gh.answer = func(c call) ([]byte, error) {
		return []byte(`{"data":{"rateLimit":{"cost":1,"remaining":4000,"resetAt":"2026-10-12T10:00:00Z"},"search":{"issueCount":900,"pageInfo":{"hasNextPage":true,"endCursor":"more"},"nodes":[{"id":"PR_a","number":3,"repository":{"nameWithOwner":"Acme-Org/Widget"}}]}}}`), nil
	}
	r.run(t)
	row, err := r.runner.Fetch(context.Background(), Key{Name: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	if row.Complete || r.gh.count() != maxPages {
		t.Errorf("complete = %v after %d calls, want false after %d", row.Complete, r.gh.count(), maxPages)
	}
}

func TestSchedule_RerunsAConfiguredQueryAtItsOwnCadenceAsClass3(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.runner.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	r.sched.Drain(ctx)
	// mine (60 s) and team (120 s) each ran once, as class 3 tasks.
	if r.gh.count() != 3 { // mine: 1 expression; team: 2 expressions
		t.Fatalf("first drain made %d gh calls, want 3", r.gh.count())
	}
	for _, b := range r.sched.State().Batches {
		if b.Kind != Kind {
			continue
		}
		for _, it := range b.Items {
			if it.Class != governor.ClassQuery {
				t.Errorf("query task %s ran as class %d, want 3", it.Entity, it.Class)
			}
		}
	}
	if bs, _ := r.gov.State().Bucket(governor.BucketGraphQL); bs.SpentByPurpose[governor.PurposeQuery] != 3 {
		t.Errorf("spend by purpose = %v, want 3 points for query", bs.SpentByPurpose)
	}

	r.clk.Advance(30 * time.Second)
	r.sched.Drain(ctx)
	if r.gh.count() != 3 {
		t.Errorf("inside query_ttl nothing re-runs, but gh was called %d times", r.gh.count())
	}

	r.clk.Advance(31 * time.Second) // 61 s: mine is due, team (120 s) is not
	r.sched.Drain(ctx)
	if r.gh.count() != 4 {
		t.Errorf("after mine's ttl gh calls = %d, want 4 (mine only)", r.gh.count())
	}
	if got := r.row(t, "mine").FetchedAt; !got.Equal(t0.Add(61 * time.Second)) {
		t.Errorf("mine fetched_at = %v", got)
	}
	if got := r.row(t, "team").FetchedAt; !got.Equal(t0) {
		t.Errorf("team fetched_at = %v, want unchanged", got)
	}
}

func TestSchedule_DueTimeSurvivesARestart(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.st.PutQuery(ctx, store.QueryRow{
		Key: "mine", Configured: true, IDs: []string{"Acme-Org/Widget#3"}, Complete: true,
		FetchedAt: t0, FreshUntil: t0.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.runner.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	r.sched.Drain(ctx)
	// mine is stored fresh, so only team ran.
	if got := r.gh.callList(); len(got) != 2 || !strings.Contains(got[0].q, "org:acme-org") {
		t.Errorf("calls = %+v, want only team's two expressions", got)
	}
}

func TestExecutor_TracksTheMembersSoTheyAreRefreshedWithoutAnyListCall(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.runner.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	r.sched.Drain(ctx)

	// Every member has an entity row and a stored summary although no list was
	// ever read: the query run tracked them and the scheduler refreshed them.
	if got := r.fetch.fetched(); !slices.Equal(got, []string{"PR_a", "PR_b", "PR_c"}) {
		t.Errorf("summaries fetched = %v, want the three members once", got)
	}
	for _, node := range []string{"PR_a", "PR_b", "PR_c"} {
		if _, ok, err := r.st.GetGroup(ctx, entity.KindPR, node, summary.GroupName); err != nil || !ok {
			t.Errorf("member %s has no stored summary (ok=%v err=%v)", node, ok, err)
		}
		// Members of a configured query are keep-alive.
		if in, err := r.svc.InKeepAlive(ctx, node); err != nil || !in {
			t.Errorf("member %s keep-alive = %v err %v, want true", node, in, err)
		}
	}

	// A new member appears on the next run and is refreshed on its own.
	r.gh.setAnswer(func(call) ([]byte, error) {
		return []byte(`{"data":{"rateLimit":{"cost":1,"remaining":4800,"resetAt":"2026-10-12T10:00:00Z"},"search":{"issueCount":4,"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"id":"PR_d","number":9,"repository":{"nameWithOwner":"Acme-Org/Widget"}}]}}}`), nil
	})
	canned["PR_d"] = struct {
		repo   string
		number int
	}{"Acme-Org/Widget", 9}
	t.Cleanup(func() { delete(canned, "PR_d") })
	r.clk.Advance(2 * time.Minute)
	r.sched.Drain(ctx)
	if got := r.fetch.fetched(); !slices.Contains(got, "PR_d") {
		t.Errorf("the new member was not refreshed: fetched = %v", got)
	}
}

func TestFetch_AnAdhocKeyIsCachedUnderAdhocTTLAndNeverRerun(t *testing.T) {
	r := newRig(t)
	r.run(t)
	ctx := context.Background()
	key := Key{Name: "mine", Args: map[string]string{"label": "bug"}}
	row, err := r.runner.Fetch(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if row.Key != "mine?label=bug" || row.Configured || !row.FreshUntil.Equal(t0.Add(120*time.Second)) {
		t.Errorf("row = %+v, want an unconfigured row fresh for adhoc_query_ttl (120 s)", row)
	}
	if got := r.gh.callList(); len(got) != 1 || got[0].q != "is:pr author:phillipgreenii is:open label:bug" {
		t.Errorf("calls = %+v, want the argument appended as a qualifier", got)
	}
	// It is not a keep-alive member...
	if rows, _ := r.st.ListQueries(ctx, true); len(rows) != 0 {
		t.Errorf("configured rows = %+v, want none (ad-hoc ids join keep-alive through access only)", rows)
	}
	// ...and the scheduler never re-runs it, however long it waits.
	r.clk.Advance(time.Hour)
	time.Sleep(100 * time.Millisecond) // let the (real-time) loop poll
	if r.gh.count() != 1 {
		t.Errorf("an ad-hoc key was re-run: %d gh calls", r.gh.count())
	}
}

func TestRun_ABudgetRefusalIsReportedAndNotRecordedAsAnOriginFailure(t *testing.T) {
	r := newRig(t)
	r.run(t)
	ctx := context.Background()
	if _, err := r.runner.Fetch(ctx, Key{Name: "mine"}); err != nil {
		t.Fatal(err)
	}
	r.clk.Advance(2 * time.Minute)
	// Spend the whole cap so every class is refused.
	if err := r.gov.Record(governor.Spend{
		Bucket: governor.BucketGraphQL, Class: governor.ClassInteractive,
		Cost: int64(r.cfg.GraphQLPointsPerHour), Outcome: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	before := r.gh.count()
	_, err := r.runner.Fetch(ctx, Key{Name: "mine"})
	var refusal *governor.Refusal
	if !errors.As(err, &refusal) || refusal.Kind != governor.RefusedCap {
		t.Fatalf("err = %v, want a cap refusal", err)
	}
	if r.gh.count() != before {
		t.Error("a refused run must not call gh")
	}
	if got := r.row(t, "mine"); got.LastError != "" || len(got.IDs) != 3 {
		t.Errorf("row = %+v: a refusal is the budget speaking, not an origin failure", got)
	}
}

func TestRun_WhileUnauthenticatedNoFetchIsMade(t *testing.T) {
	r := newRig(t)
	r.run(t)
	r.auth.Observe(fmt.Errorf("%w: bad credentials", vcs.ErrAuthInvalid))
	_, err := r.runner.Fetch(context.Background(), Key{Name: "mine"})
	if !errors.Is(err, vcs.ErrAuthInvalid) || r.gh.count() != 0 {
		t.Errorf("err = %v after %d gh calls, want an auth error and no call", err, r.gh.count())
	}
}

func TestRun_AuthFailureFromTheOriginMarksUnauthenticatedAndSuccessClearsIt(t *testing.T) {
	r := newRig(t)
	r.run(t)
	ctx := context.Background()
	r.gh.setAnswer(func(call) ([]byte, error) { return nil, fmt.Errorf("gh api: %w", vcs.ErrAuthInvalid) })
	if _, err := r.runner.Fetch(ctx, Key{Name: "mine"}); err == nil {
		t.Fatal("want an error")
	}
	if bad, _ := r.auth.Unauthenticated(); !bad {
		t.Fatal("an origin 401 must mark the daemon unauthenticated")
	}
	r.auth.Observe(nil) // the viewer refresh recovers it
	r.gh.setAnswer(func(call) ([]byte, error) { return fixture(t, "search_three.json"), nil })
	r.clk.Advance(2 * time.Minute)
	if _, err := r.runner.Fetch(ctx, Key{Name: "mine"}); err != nil {
		t.Fatal(err)
	}
	if bad, _ := r.auth.Unauthenticated(); bad {
		t.Error("a successful call must clear the state")
	}
}

func TestRun_AnEmptyAnswerIsAnError(t *testing.T) {
	r := newRig(t)
	r.run(t)
	r.gh.setAnswer(func(call) ([]byte, error) { return fixture(t, "search_null.json"), nil })
	_, err := r.runner.Fetch(context.Background(), Key{Name: "mine"})
	if err == nil || !strings.Contains(err.Error(), "search is unavailable") {
		t.Errorf("err = %v", err)
	}
}

func TestNew_RequiresItsParts(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Error("New with nothing wired must fail")
	}
}

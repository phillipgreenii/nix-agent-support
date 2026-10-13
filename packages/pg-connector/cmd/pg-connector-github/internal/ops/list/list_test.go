package list

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/vcs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
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

// fakeGH plays gh: a membership search answers search(), a nodes(ids:) call
// answers the canned pull requests asked for. Every call is counted.
type fakeGH struct {
	t *testing.T

	mu       sync.Mutex
	searches []string   // q of every search call
	nodes    [][]string // ids of every nodes call
	// search, when set, answers a search call; nil means the golden fixture.
	search func(q string) ([]byte, error)
	// nodesErr, when set, fails every nodes call.
	nodesErr error
	// mergeable overrides what a node reports as mergeable.
	mergeable map[string]string
	// gate, when set, is waited on inside a search call; nodesGate likewise
	// inside a nodes call.
	gate      chan struct{}
	nodesGate chan struct{}
}

func (g *fakeGH) Run(_ context.Context, args ...string) ([]byte, error) {
	var q string
	var ids []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-f" {
			continue
		}
		switch {
		case strings.HasPrefix(args[i+1], "q="):
			q = strings.TrimPrefix(args[i+1], "q=")
		case strings.HasPrefix(args[i+1], "ids[]="):
			ids = append(ids, strings.TrimPrefix(args[i+1], "ids[]="))
		}
	}
	g.mu.Lock()
	if ids == nil {
		g.searches = append(g.searches, q)
		search, gate := g.search, g.gate
		g.mu.Unlock()
		if gate != nil {
			<-gate
		}
		if search != nil {
			return search(q)
		}
		return fixture(g.t, "search_golden.json"), nil
	}
	g.nodes = append(g.nodes, slices.Clone(ids))
	nodesErr, merge, nodesGate := g.nodesErr, g.mergeable, g.nodesGate
	g.mu.Unlock()
	if nodesGate != nil {
		<-nodesGate
	}
	if nodesErr != nil {
		return nil, nodesErr
	}
	var doc struct {
		Data struct {
			RateLimit json.RawMessage  `json:"rateLimit"`
			Nodes     []map[string]any `json:"nodes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(fixture(g.t, "nodes_golden.json"), &doc); err != nil {
		g.t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, n := range doc.Data.Nodes {
		byID[n["id"].(string)] = n
	}
	var out []map[string]any
	for _, id := range ids {
		n := byID[id]
		if n != nil {
			if m, ok := merge[id]; ok {
				n["mergeable"] = m
			}
		}
		out = append(out, n)
	}
	body, _ := json.Marshal(map[string]any{"data": map[string]any{"rateLimit": doc.Data.RateLimit, "nodes": out}})
	return body, nil
}

// fetched returns how many times each node id was asked for in nodes calls. The
// scheduler may split one request's ids over two calls (a task it was handed
// first can go out before the next one is submitted), so a test asserts what
// was fetched, not how the ids were packed.
func (g *fakeGH) fetched() map[string]int {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[string]int{}
	for _, c := range g.nodes {
		for _, id := range c {
			out[id]++
		}
	}
	return out
}

// since returns how many more times each node id was asked for than in before.
func since(now, before map[string]int) map[string]int {
	out := map[string]int{}
	for id, n := range now {
		out[id] = n - before[id]
	}
	return out
}

func (g *fakeGH) counts() (searches, nodeCalls int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.searches), len(g.nodes)
}

// countingSummaries counts the calls the handler makes into the summary
// service: an interactive fetch is a caller waiting on the origin.
type countingSummaries struct {
	Summaries
	mu          sync.Mutex
	interactive int
}

func (c *countingSummaries) Interactive(ctx context.Context, ids []string) []summary.InteractiveResult {
	c.mu.Lock()
	c.interactive++
	c.mu.Unlock()
	return c.Summaries.Interactive(ctx, ids)
}

func (c *countingSummaries) InteractiveFresh(ctx context.Context, ids []string) []summary.InteractiveResult {
	c.mu.Lock()
	c.interactive++
	c.mu.Unlock()
	return c.Summaries.InteractiveFresh(ctx, ids)
}

func (c *countingSummaries) waits() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.interactive
}

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
	t      *testing.T
	st     *store.Store
	clk    *governor.ManualClock
	gov    *governor.Governor
	sched  *scheduler.Scheduler
	svc    *summary.Service
	gh     *fakeGH
	runner *query.Runner
	waits  *countingSummaries
	auth   *fakeAuth
	router *router.Router
	cfg    *config.Config

	mu   sync.Mutex
	last router.Annotations
}

// newRig is a rig whose scheduler runs no background refresh (see quiet): a
// request is the only thing that fetches, so what it fetches can be counted.
func newRig(t *testing.T) *rig {
	t.Helper()
	r := newRigWith(t)
	r.quiet()
	return r
}

// newRigWith is a rig with the scheduler's background classes running.
func newRigWith(t *testing.T) *rig {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := governor.NewManualClock(t0)
	cfg := config.Defaults()
	cfg.PR.Queries = map[string]schema.QueryExpr{"mine": {"author:phillipgreenii is:open"}, "team": {"org:acme-org is:open"}}
	gov, err := governor.New(ratefeed.GovernorConfig(cfg.Settings), governor.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	gh := &fakeGH{t: t}
	feed := ratefeed.New(gov, gh, clk)
	sched := scheduler.New(scheduler.Options{Clock: clk, Gate: gov, Workers: 2, PollInterval: 5 * time.Millisecond, RetryDelay: time.Minute})
	id := entity.NewIdentity(st, clk)
	pers := &summary.Persister{Store: st, Identity: id, TTL: time.Minute, TerminalTTL: time.Hour, Clock: clk}
	svc, err := summary.NewService(summary.ServiceConfig{
		Store: st, Persister: pers, Fetcher: &summary.Fetcher{GH: gh, Governor: gov, Feed: feed}, Identity: id,
		Scheduler: sched, Clock: clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	auth := &fakeAuth{}
	runner, err := query.New(query.Config{
		Settings: cfg, Store: st, Identity: id, Tracker: svc, GH: gh, Governor: gov, Feed: feed,
		Scheduler: sched, Auth: auth, Clock: clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, st: st, clk: clk, gov: gov, sched: sched, svc: svc, gh: gh, runner: runner, auth: auth, cfg: cfg}
	r.router = router.New(router.Options{Instances: []string{"pr", "ci"}, Now: clk.Now})
	r.router.Use(func(next router.Func) router.Func {
		return func(ctx context.Context, c router.Call) scriptout.Response {
			resp := next(ctx, c)
			r.mu.Lock()
			r.last = router.AnnotationsFrom(ctx)
			r.mu.Unlock()
			return resp
		}
	})
	r.waits = &countingSummaries{Summaries: svc}
	err = Register(r.router, Config{
		Queries: runner, Summaries: r.waits, Store: st, Identity: id, Auth: auth,
		Expiry: cfg.Expiry.Std(), Clock: clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sched.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return r
}

// quiet holds the scheduler's background classes back for good, so a test of
// what a request does on its own (stale content, refusals, failures) is not raced
// by the scheduler refreshing the same summaries and queries behind its back.
// Interactive requests are not affected by a pause.
func (r *rig) quiet() {
	r.t.Helper()
	r.gov.PauseAll(1000*24*time.Hour, "test: no background refresh")
}

// do dispatches one list call and returns the response and its annotations.
func (r *rig) do(args string, config string, deadline time.Duration) (scriptout.Response, router.Annotations) {
	r.t.Helper()
	req := scriptout.Request{Op: "list", Args: json.RawMessage(args)}
	if config != "" {
		req.Config = json.RawMessage(config)
	}
	resp := r.router.Dispatch(context.Background(), router.Call{Instance: "pr", Request: req, Deadline: deadline})
	r.mu.Lock()
	defer r.mu.Unlock()
	return resp, r.last
}

func (r *rig) list(args string, config string) (schema.PRListResult, router.Annotations) {
	r.t.Helper()
	resp, ann := r.do(args, config, 0)
	if resp.Error != nil {
		r.t.Fatalf("list %s: error %+v", args, resp.Error)
	}
	var res schema.PRListResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		r.t.Fatal(err)
	}
	return res, ann
}

func (r *rig) errCode(args string, config string) (code, msg string) {
	r.t.Helper()
	resp, _ := r.do(args, config, 0)
	if resp.Error == nil {
		r.t.Fatalf("list %s: want an error, got %s", args, resp.Result)
	}
	return resp.Error.Code, resp.Error.Message
}

func idsOf(res schema.PRListResult) []string {
	var out []string
	for _, e := range res.Entities {
		out = append(out, e.ID)
	}
	return out
}

func TestRegister_InstallsListUnderPrOnly(t *testing.T) {
	r := newRig(t)
	if got := r.router.Ops("pr"); !slices.Equal(got, []string{"list"}) {
		t.Errorf("pr ops = %v", got)
	}
	if got := r.router.Ops("ci"); len(got) != 0 {
		t.Errorf("ci ops = %v, want none", got)
	}
}

func TestList_InsideQueryTTLMakesNoOriginCallAndSaysCacheWithItsAge(t *testing.T) {
	r := newRig(t)
	first, ann := r.list(`{"query":"mine"}`, "")
	if len(first.Entities) != 2 || first.Entities[0].ID != "Acme-Org/Widget#3" {
		t.Fatalf("first answer = %+v", first)
	}
	if ann.ServedFrom != "origin" || ann.Stale {
		t.Errorf("first call annotations = %+v, want origin and not stale", ann)
	}
	searches, _ := r.gh.counts()
	// Each summary is fetched (once; a scheduler refresh racing the caller's own
	// request can duplicate one fetch, so "at least once" is what is asserted).
	if f := r.gh.fetched(); searches != 1 || f["PR_a"] < 1 || f["PR_b"] < 1 {
		t.Fatalf("first call: %d searches, summaries fetched %v, want 1 search and each summary fetched", searches, f)
	}

	r.clk.Advance(30 * time.Second)
	s0, n0 := r.gh.counts()
	second, ann := r.list(`{"query":"mine"}`, "")
	if s, n := r.gh.counts(); s != s0 || n != n0 {
		t.Errorf("a list inside query_ttl made origin calls: %d searches, %d nodes calls", s-s0, n-n0)
	}
	if ann.ServedFrom != "cache" || ann.Stale || ann.AgeSeconds != 30 {
		t.Errorf("annotations = %+v, want cache, not stale, age 30", ann)
	}
	if !reflect.DeepEqual(idsOf(first), idsOf(second)) {
		t.Errorf("answers differ: %v vs %v", idsOf(first), idsOf(second))
	}
}

func TestList_ReadsTheDaemonsOwnConfigNotTheRequests(t *testing.T) {
	r := newRig(t)
	cfg := `{"queries":{"mine":"author:someone-else","sneaky":"is:open"}}`
	if code, msg := r.errCode(`{"query":"sneaky"}`, cfg); code != "query_not_recognized" || !strings.Contains(msg, "sneaky") {
		t.Errorf("unknown name = %s %q, want query_not_recognized naming it", code, msg)
	}
	if code, _ := r.errCode(`{"query":""}`, ""); code != "query_not_recognized" {
		t.Errorf("empty name = %s", code)
	}
	if s, n := r.gh.counts(); s != 0 || n != 0 {
		t.Errorf("an unknown name made origin calls: %d %d", s, n)
	}
	// A configured name works whatever the request carries, and its expression
	// is the daemon's.
	r.list(`{"query":"mine"}`, cfg)
	if got := r.gh.searches[0]; got != "is:pr author:phillipgreenii is:open" {
		t.Errorf("searched %q, want the daemon's own expression", got)
	}
}

func TestList_AStaleQueryReRunsOnceAndStoresIdsAndTheCompleteFlag(t *testing.T) {
	r := newRig(t)
	r.list(`{"query":"mine"}`, "")
	before := r.gh.fetched()
	r.clk.Advance(61 * time.Second) // past query_ttl 60 s and summary_ttl 60 s
	res, ann := r.list(`{"query":"mine"}`, "")
	searches, _ := r.gh.counts()
	if f := since(r.gh.fetched(), before); searches != 2 || f["PR_a"] < 1 || f["PR_b"] < 1 {
		t.Errorf("%d searches, summaries fetched again %v, want one re-run and each stale summary fetched", searches, f)
	}
	for _, c := range r.gh.nodes {
		if len(c) > 74 {
			t.Errorf("a nodes call carried %d ids", len(c))
		}
	}
	if ann.ServedFrom != "origin" || ann.Stale || len(res.Entities) != 2 {
		t.Errorf("annotations %+v, entities %d", ann, len(res.Entities))
	}
	row, ok, _ := r.st.GetQuery(context.Background(), "mine")
	if !ok || !row.Configured || !row.Complete || !slices.Equal(row.IDs, []string{"Acme-Org/Widget#3", "acme-org/gadget#7"}) ||
		!row.FetchedAt.Equal(r.clk.Now()) {
		t.Errorf("stored row = %+v", row)
	}
}

func TestList_AListingPastTheSearchLimitIsTruncated(t *testing.T) {
	r := newRig(t)
	r.gh.search = func(string) ([]byte, error) {
		return []byte(`{"data":{"rateLimit":{"cost":1,"remaining":4900,"resetAt":"2026-10-12T10:00:00Z"},"search":{"issueCount":1500,"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"id":"PR_a","number":3,"repository":{"nameWithOwner":"Acme-Org/Widget"}}]}}}`), nil
	}
	res, _ := r.list(`{"query":"mine"}`, "")
	if !res.Truncated || len(res.PresentIDs) != 1 {
		t.Errorf("answer = %+v, want truncated with the one id read", res)
	}
	if row, _, _ := r.st.GetQuery(context.Background(), "mine"); row.Complete {
		t.Error("the stored row must have complete false")
	}
}

func TestList_SinceAndBeforeFilterTheCachedIdsByUpdatedAtWithNoOriginCall(t *testing.T) {
	r := newRig(t)
	r.list(`{"query":"mine"}`, "")
	s0, n0 := r.gh.counts()

	since, _ := r.list(`{"query":"mine"}`, `{"list_since":"2026-10-12T00:00:00Z"}`)
	if !slices.Equal(idsOf(since), []string{"Acme-Org/Widget#3"}) || !slices.Equal(since.PresentIDs, []string{"Acme-Org/Widget#3"}) {
		t.Errorf("since = %v / %v, want only the PR updated on the 12th", idsOf(since), since.PresentIDs)
	}
	before, ann := r.list(`{"query":"mine"}`, `{"list_before":"2026-10-12T00:00:00Z"}`)
	if !slices.Equal(idsOf(before), []string{"acme-org/gadget#7"}) {
		t.Errorf("before = %v, want only the PR updated on the 11th", idsOf(before))
	}
	both, _ := r.list(`{"query":"mine"}`, `{"list_since":"2026-10-11T00:00:00Z","list_before":"2026-10-12T00:00:00Z"}`)
	if !slices.Equal(idsOf(both), []string{"acme-org/gadget#7"}) {
		t.Errorf("window = %v", idsOf(both))
	}
	idsOnly, _ := r.list(`{"query":"mine","ids_only":true}`, `{"list_since":"2026-10-12T00:00:00Z"}`)
	if !slices.Equal(idsOnly.PresentIDs, []string{"Acme-Org/Widget#3"}) || idsOnly.Entities != nil {
		t.Errorf("ids_only with since = %+v", idsOnly)
	}
	if s, n := r.gh.counts(); s != s0 || n != n0 {
		t.Errorf("a time range made origin calls: searches %d->%d nodes %d->%d", s0, s, n0, n)
	}
	if ann.ServedFrom != "cache" {
		t.Errorf("annotations = %+v, want cache", ann)
	}
	if row, _, _ := r.st.GetQuery(context.Background(), "mine?list_since=x"); row.Key != "" {
		t.Error("a range must not make a key of its own")
	}
	if rows, _ := r.st.ListQueries(context.Background(), false); len(rows) != 1 {
		t.Errorf("rows = %d, want only the configured query's", len(rows))
	}
}

func TestList_AMalformedBoundIsInvalidArgumentBeforeAnyOriginCall(t *testing.T) {
	r := newRig(t)
	if code, _ := r.errCode(`{"query":"mine"}`, `{"list_since":"yesterday"}`); code != "invalid_argument" {
		t.Errorf("code = %s", code)
	}
	if s, _ := r.gh.counts(); s != 0 {
		t.Errorf("%d searches", s)
	}
	if code, _ := r.errCode(`{"query":`, ""); code != "invalid_argument" {
		t.Errorf("malformed args code = %s", code)
	}
}

func TestList_ADifferingArgumentIsAnAdhocKeyCachedUnderAdhocTTLNeverReRunAndItsIdsJoinKeepAlive(t *testing.T) {
	r := newRigWith(t) // the scheduler runs, so "never re-run" is a real claim
	ctx := context.Background()
	args := `{"query":"mine","args":{"label":"bug"}}`
	r.list(args, "")
	if got := r.gh.searches[0]; got != "is:pr author:phillipgreenii is:open label:bug" {
		t.Errorf("searched %q", got)
	}
	row, ok, _ := r.st.GetQuery(ctx, "mine?label=bug")
	if !ok || row.Configured || !row.FreshUntil.Equal(t0.Add(120*time.Second)) {
		t.Fatalf("row = %+v ok=%v, want an unconfigured row fresh for adhoc_query_ttl", row, ok)
	}
	// The same arguments again inside adhoc_query_ttl (and past summary_ttl).
	r.clk.Advance(100 * time.Second)
	r.list(args, "")
	if s, _ := r.gh.counts(); s != 1 {
		t.Errorf("a repeat inside adhoc_query_ttl re-ran the query (%d searches)", s)
	}
	// The un-argumented query is a different key.
	r.list(`{"query":"mine"}`, "")
	if s, _ := r.gh.counts(); s != 2 {
		t.Errorf("the configured key shared the ad-hoc row (%d searches)", s)
	}
	adhocSearches := func() int {
		r.gh.mu.Lock()
		defer r.gh.mu.Unlock()
		n := 0
		for _, q := range r.gh.searches {
			if strings.Contains(q, "label:bug") {
				n++
			}
		}
		return n
	}
	// Its ids joined the keep-alive set through the access.
	for _, node := range []string{"PR_a", "PR_b"} {
		e, ok, err := r.st.GetEntity(ctx, entity.KindPR, node)
		if err != nil || !ok || e.LastAccess.IsZero() {
			t.Errorf("%s entity = %+v ok=%v: want an access recorded", node, e, ok)
		}
	}
	// And the scheduler never re-runs it (the configured query it sits next to
	// does re-run, which is why only the ad-hoc searches are counted).
	r.clk.Advance(time.Hour)
	time.Sleep(100 * time.Millisecond)
	if n := adhocSearches(); n != 1 {
		t.Errorf("the scheduler re-ran an ad-hoc key (%d searches carried its argument)", n)
	}
}

func TestList_AnAdhocIdsOnlyAnswerStillRecordsTheAccess(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.list(`{"query":"mine","ids_only":true,"args":{"label":"bug"}}`, "")
	for _, node := range []string{"PR_a", "PR_b"} {
		e, ok, err := r.st.GetEntity(ctx, entity.KindPR, node)
		if err != nil || !ok || e.LastAccess.IsZero() || !e.KeepAlive {
			t.Errorf("%s = %+v ok=%v, want an access and keep-alive", node, e, ok)
		}
		if in, err := r.svc.InKeepAlive(ctx, node); err != nil || !in {
			t.Errorf("%s keep-alive = %v %v", node, in, err)
		}
	}
}

func TestList_IDsOnlyReturnsIdsOnlyAndWaitsOnNoSummary(t *testing.T) {
	r := newRig(t)
	// A configured query's members are refreshed in the background (the run
	// tracked them), but the caller never waits on a summary for ids only.
	r.gh.nodesGate = make(chan struct{})
	t.Cleanup(func() { close(r.gh.nodesGate) })
	res, ann := r.list(`{"query":"mine","ids_only":true}`, "")
	if res.Entities != nil || !slices.Equal(res.PresentIDs, []string{"Acme-Org/Widget#3", "acme-org/gadget#7"}) {
		t.Errorf("answer = %+v", res)
	}
	if s, _ := r.gh.counts(); s != 1 {
		t.Errorf("%d searches, want 1", s)
	}
	if r.waits.waits() != 0 {
		t.Error("ids_only waited on the summary service")
	}
	if ann.ServedFrom != "origin" {
		t.Errorf("annotations = %+v", ann)
	}
}

func TestList_FreshTrueGoesToTheOriginForTheQueryAndEverySummary(t *testing.T) {
	r := newRig(t)
	r.list(`{"query":"mine"}`, "")
	before := r.gh.fetched()
	r.clk.Advance(5 * time.Second)
	_, ann := r.list(`{"query":"mine","fresh":true}`, "")
	s1, n1 := r.gh.counts()
	if f := since(r.gh.fetched(), before); s1 != 2 || f["PR_a"] < 1 || f["PR_b"] < 1 {
		t.Errorf("fresh made %d searches and fetched %v again, want the query and every summary again", s1, f)
	}
	if ann.ServedFrom != "origin" || ann.AgeSeconds != 0 {
		t.Errorf("annotations = %+v", ann)
	}
	r.list(`{"query":"mine"}`, "") // and the plain call still reads the cache
	if s, n := r.gh.counts(); s != s1 || n != n1 {
		t.Errorf("after fresh: %d %d, want no more calls", s, n)
	}
}

func TestList_WhenTheGovernorRefusesItServesStaleInsideExpiryAndNamesTheCapOutsideIt(t *testing.T) {
	r := newRig(t)
	first, _ := r.list(`{"query":"mine"}`, "")
	r.clk.Advance(2 * time.Hour) // stale, but inside expiry (7 d)
	if err := r.gov.Record(governor.Spend{
		Bucket: governor.BucketGraphQL, Class: governor.ClassInteractive,
		Cost: int64(r.cfg.GraphQLPointsPerHour), Outcome: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	s0, n0 := r.gh.counts()
	got, ann := r.list(`{"query":"mine"}`, "")
	if !ann.Stale || ann.ServedFrom != "cache" || ann.AgeSeconds != 7200 {
		t.Errorf("annotations = %+v, want stale cache aged 2 h", ann)
	}
	if !reflect.DeepEqual(idsOf(got), idsOf(first)) {
		t.Errorf("the stale answer differs: %v", idsOf(got))
	}
	for _, e := range got.Entities {
		if !e.Stale {
			t.Errorf("entity %s served past its freshness must say stale", e.ID)
		}
	}
	if s, n := r.gh.counts(); s != s0 || n != n0 {
		t.Error("a refused run must not call gh")
	}

	// Outside expiry there is nothing to serve: unavailable, naming the cap.
	r.clk.Advance(8 * 24 * time.Hour)
	if err := r.gov.Record(governor.Spend{
		Bucket: governor.BucketGraphQL, Class: governor.ClassInteractive,
		Cost: int64(r.cfg.GraphQLPointsPerHour), Outcome: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	code, msg := r.errCode(`{"query":"mine"}`, "")
	if code != "unavailable" || !strings.Contains(msg, "cap reached") {
		t.Errorf("answer = %s %q, want unavailable naming the cap", code, msg)
	}
}

func TestList_WhenTheReserveIsBelowItNamesTheReserve(t *testing.T) {
	r := newRig(t)
	if err := r.gov.Observe(governor.Reading{Bucket: governor.BucketGraphQL, Remaining: 10, ResetAt: t0.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	code, msg := r.errCode(`{"query":"mine"}`, "")
	if code != "unavailable" || !strings.Contains(msg, "reserve") {
		t.Errorf("answer = %s %q, want unavailable naming the reserve", code, msg)
	}
}

func TestList_AnOriginFailureServesStaleInsideExpiryAndRecordsLastError(t *testing.T) {
	r := newRig(t)
	r.list(`{"query":"mine"}`, "")
	r.clk.Advance(2 * time.Minute)
	r.gh.search = func(string) ([]byte, error) { return nil, errors.New("gh: HTTP 502 Bad Gateway") }
	// The id list is stale and the origin is down for it; the summaries (also
	// stale) are fetched fine, so the answer is part origin, part stale.
	got, ann := r.list(`{"query":"mine"}`, "")
	if !ann.Stale || ann.ServedFrom != "origin" || len(got.Entities) != 2 {
		t.Errorf("annotations %+v, entities %d, want stale ids with refreshed summaries", ann, len(got.Entities))
	}
	// With the whole origin down nothing is refreshed: pure stale cache.
	r.gh.nodesErr = errors.New("gh: HTTP 502 Bad Gateway")
	r.clk.Advance(2 * time.Minute)
	got, ann = r.list(`{"query":"mine"}`, "")
	if !ann.Stale || ann.ServedFrom != "cache" || len(got.Entities) != 2 {
		t.Errorf("annotations %+v, entities %d, want the stale answer from cache", ann, len(got.Entities))
	}
	row, _, _ := r.st.GetQuery(context.Background(), "mine")
	if !strings.Contains(row.LastError, "502") || len(row.IDs) != 2 {
		t.Errorf("row = %+v, want last_error recorded and the ids kept", row)
	}

	r.clk.Advance(8 * 24 * time.Hour)
	if code, msg := r.errCode(`{"query":"mine"}`, ""); code != "unavailable" || !strings.Contains(msg, "502") {
		t.Errorf("outside expiry = %s %q", code, msg)
	}
}

func TestList_ASummaryFetchFailureServesTheStoredSummaryInsideExpiry(t *testing.T) {
	r := newRig(t)
	r.list(`{"query":"mine"}`, "")
	r.clk.Advance(61 * time.Second)
	r.gh.nodesErr = errors.New("gh: HTTP 504 Gateway Timeout")
	got, ann := r.list(`{"query":"mine"}`, "")
	if !ann.Stale || len(got.Entities) != 2 || !got.Entities[0].Stale {
		t.Errorf("annotations %+v answer %+v", ann, got)
	}
	r.clk.Advance(8 * 24 * time.Hour)
	if code, _ := r.errCode(`{"query":"mine"}`, ""); code != "unavailable" {
		t.Errorf("code = %s", code)
	}
}

func TestList_Unauthenticated(t *testing.T) {
	r := newRig(t)
	// Nothing cached: origin would be needed, so unauthenticated, no call.
	r.auth.Observe(fmt.Errorf("%w: bad credentials", vcs.ErrAuthInvalid))
	if code, _ := r.errCode(`{"query":"mine"}`, ""); code != "unauthenticated" {
		t.Errorf("code = %s, want unauthenticated", code)
	}
	if s, _ := r.gh.counts(); s != 0 || r.waits.waits() != 0 {
		t.Errorf("an unauthenticated daemon fetched for a request: %d searches, %d summary waits", s, r.waits.waits())
	}

	// With a cached list: served inside expiry, with its age.
	r.auth.Observe(nil)
	r.list(`{"query":"mine"}`, "")
	r.clk.Advance(2 * time.Hour)
	r.auth.Observe(fmt.Errorf("%w: bad credentials", vcs.ErrAuthInvalid))
	s0, _ := r.gh.counts()
	w0 := r.waits.waits()
	got, ann := r.list(`{"query":"mine"}`, "")
	if len(got.Entities) != 2 || !ann.Stale || ann.ServedFrom != "cache" || ann.AgeSeconds != 7200 {
		t.Errorf("cached read while unauthenticated: %+v %+v", got, ann)
	}
	// Even a fresh: true read is served from cache while unauthenticated, and
	// the request itself makes no origin call (the summary service's own
	// background refresh is its own business, so only the request's fetch paths
	// are counted).
	r.list(`{"query":"mine","fresh":true}`, "")
	if s, _ := r.gh.counts(); s != s0 || r.waits.waits() != w0 {
		t.Errorf("a request fetched from the origin while unauthenticated: searches %d->%d, summary waits %d->%d",
			s0, s, w0, r.waits.waits())
	}
	// Past expiry it is unauthenticated again.
	r.clk.Advance(8 * 24 * time.Hour)
	if code, _ := r.errCode(`{"query":"mine"}`, ""); code != "unauthenticated" {
		t.Errorf("code = %s", code)
	}
}

func TestList_AnOriginAuthFailureMarksTheDaemonUnauthenticated(t *testing.T) {
	r := newRig(t)
	r.gh.search = func(string) ([]byte, error) { return nil, fmt.Errorf("gh api: %w", vcs.ErrAuthInvalid) }
	if code, _ := r.errCode(`{"query":"mine"}`, ""); code != "unauthenticated" {
		t.Errorf("code = %s", code)
	}
	if bad, _ := r.auth.Unauthenticated(); !bad {
		t.Error("the 401 must mark the daemon unauthenticated")
	}
}

func TestList_TwoAnswersAroundAnUnknownMergeabilityHopAreByteIdentical(t *testing.T) {
	r := newRig(t)
	first, _ := r.list(`{"query":"mine"}`, "")
	if first.Entities[0].Mergeable != "MERGEABLE" {
		t.Fatalf("first answer mergeable = %q", first.Entities[0].Mergeable)
	}
	// GitHub recomputes lazily: the next fetch of PR_a says UNKNOWN.
	before := r.gh.fetched()
	r.gh.mergeable = map[string]string{"PR_a": "UNKNOWN"}
	r.clk.Advance(61 * time.Second)
	second, _ := r.list(`{"query":"mine"}`, "")
	if f := since(r.gh.fetched(), before); f["PR_a"] < 1 {
		t.Fatalf("PR_a was not re-fetched (fetched again %v)", f)
	}
	if got := second.Entities[0].Mergeable; got != "MERGEABLE" {
		t.Errorf("mergeable after the UNKNOWN hop = %q, want the carried-forward MERGEABLE", got)
	}
	// as_of and stale are what the umbrella's fingerprint drops from every
	// hash; everything else must be byte-identical.
	norm := func(res schema.PRListResult) string {
		for i := range res.Entities {
			res.Entities[i].AsOf, res.Entities[i].Stale = "", false
		}
		b, _ := json.Marshal(res)
		return string(b)
	}
	if a, b := norm(first), norm(second); a != b {
		t.Errorf("answers differ around an UNKNOWN hop:\n%s\n%s", a, b)
	}
}

// TestList_HasTheOldBackendsShape compares the answer for the same canned
// pull requests with what the old pg-connector-pr-github list answered. The
// golden files are that backend's own output for old_search_page.json, which
// holds the same two PRs as nodes_golden.json (generated by running its
// Backend.List over that page, then `jq -S`). Apart from as_of and stale (this
// read's own time and staleness, which the umbrella drops from every hash) the
// answers are identical, mergeable included for this input.
func TestList_HasTheOldBackendsShape(t *testing.T) {
	r := newRig(t)
	for _, tc := range []struct{ name, args, golden string }{
		{"list", `{"query":"mine"}`, "golden_old_list.json"},
		{"ids_only", `{"query":"mine","ids_only":true}`, "golden_old_list_ids_only.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := r.do(tc.args, "", 0)
			if resp.Error != nil {
				t.Fatal(resp.Error)
			}
			if resp.SchemaVersion != schema.PRSchemaVersion {
				t.Errorf("schemaVersion = %d, want %d", resp.SchemaVersion, schema.PRSchemaVersion)
			}
			strip := func(raw []byte) any {
				var v map[string]any
				if err := json.Unmarshal(raw, &v); err != nil {
					t.Fatal(err)
				}
				if es, _ := v["entities"].([]any); es != nil {
					for _, e := range es {
						delete(e.(map[string]any), "as_of")
						delete(e.(map[string]any), "stale")
					}
				}
				return v
			}
			got, want := strip(resp.Result), strip(fixture(t, tc.golden))
			if !reflect.DeepEqual(got, want) {
				g, _ := json.MarshalIndent(got, "", "  ")
				w, _ := json.MarshalIndent(want, "", "  ")
				t.Errorf("answer differs from the old backend's:\ngot  %s\nwant %s", g, w)
			}
		})
	}
}

func TestList_EveryFieldIsAListFieldAndTheEntityIsSummaryLevel(t *testing.T) {
	r := newRig(t)
	res, _ := r.list(`{"query":"mine"}`, "")
	raw, _ := json.Marshal(res.Entities[0])
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	allowed := map[string]bool{"branch": true, "base": true, "merged": true} // never set on the list path
	for _, f := range schema.PRListFields {
		allowed[f] = true
	}
	for k := range m {
		if !allowed[k] {
			t.Errorf("entity carries %q, which is not a list field", k)
		}
	}
	for _, f := range []string{"comments", "reviews", "connections", "base_sha", "additions", "review_requests"} {
		if _, ok := m[f]; ok {
			t.Errorf("entity carries %q, which the list leaves out", f)
		}
	}
}

func TestList_ACursorIsAcceptedAndIgnored(t *testing.T) {
	r := newRig(t)
	res, _ := r.list(`{"query":"mine","cursor":{"anything":1}}`, "")
	if string(res.Cursor) != "null" || len(res.Entities) != 2 {
		t.Errorf("answer = %+v", res)
	}
}

func TestList_AFetchRunningAtTheDeadlineIsNotCancelledAndIsStored(t *testing.T) {
	r := newRig(t)
	r.gh.gate = make(chan struct{})
	// A tight deadline would also expire inside the first store read on a loaded
	// host; the search is blocked, so any deadline elapses.
	resp, _ := r.do(`{"query":"mine"}`, "", 400*time.Millisecond)
	if resp.Error == nil || resp.Error.Code != "unavailable" {
		t.Fatalf("response = %+v, want unavailable at the deadline with nothing cached", resp)
	}
	firstErr := resp.Error.Message
	close(r.gh.gate)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if row, ok, _ := r.st.GetQuery(context.Background(), "mine"); ok && len(row.IDs) == 2 {
			break
		}
		if time.Now().After(deadline) {
			row, ok, _ := r.st.GetQuery(context.Background(), "mine")
			ns, nn := r.gh.counts()
			t.Fatalf("the run that outlived its caller was never stored: first answer %q, row %+v ok=%v, %d searches %d nodes calls", firstErr, row, ok, ns, nn)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The next call is answered from what the background run stored.
	res, _ := r.list(`{"query":"mine","ids_only":true}`, "")
	if len(res.PresentIDs) != 2 {
		t.Errorf("answer = %+v", res)
	}
	if s, _ := r.gh.counts(); s != 1 {
		t.Errorf("%d searches, want the one background run", s)
	}
}

func TestList_AnEmptyResultIsAnEmptyListNotNull(t *testing.T) {
	r := newRig(t)
	r.gh.search = func(string) ([]byte, error) {
		return []byte(`{"data":{"rateLimit":{"cost":1,"remaining":4900,"resetAt":"2026-10-12T10:00:00Z"},"search":{"issueCount":0,"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}`), nil
	}
	resp, _ := r.do(`{"query":"mine"}`, "", 0)
	if resp.Error != nil || !strings.Contains(string(resp.Result), `"entities":[]`) || !strings.Contains(string(resp.Result), `"present_ids":[]`) {
		t.Errorf("response = %s %+v", resp.Result, resp.Error)
	}
	resp, _ = r.do(`{"query":"mine","ids_only":true}`, "", 0)
	if !strings.Contains(string(resp.Result), `"entities":null`) || !strings.Contains(string(resp.Result), `"present_ids":[]`) {
		t.Errorf("ids_only response = %s", resp.Result)
	}
}

func TestHandler_RequiresItsParts(t *testing.T) {
	if _, err := Handler(Config{}); err == nil {
		t.Error("Handler with nothing wired must fail")
	}
}

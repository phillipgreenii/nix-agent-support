package loadtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/conversation"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/detail"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc/groupsvctest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/prfacts"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/revalidate"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/runs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// The scale and the clock of the load scenario (see the README comment in
// load_test.go for what each number models).
const (
	// loadSeed seeds every random choice of the scenario (which PRs change
	// their head and when, which PR an interactive read asks for).
	loadSeed = 20261014

	nPRs     = 150 // kept-alive PRs
	nMembers = 120 // PRs listed by a configured query; the rest are kept alive by access alone

	// tick is the fake time one driver step advances. The settle lifecycle is
	// swept every sweepEvery ticks, which is its interval of one second.
	tick       = 250 * time.Millisecond
	sweepEvery = 4
	// originLatency is the fake time one origin call takes. The daemon has four
	// workers in production; the harness steps ONE worker (so the run is
	// deterministic) whose calls take a quarter of a second, which gives the same
	// throughput of four calls per second.
	originLatency = 250 * time.Millisecond

	// reading values of the origin's GraphQL bucket.
	fullRemaining = 5000
)

// scenarioOpts select the scenario: the zero value is the real test, the other
// fields are the deliberate mutations of the harness the mutation test applies.
type scenarioOpts struct {
	seed int64
	// duration is the fake time of the run; zero means defaultDuration.
	duration time.Duration

	// capScale multiplies every cap of the harness's governor (mutation a);
	// zero or one leaves them.
	capScale int64
	// skipSweep never runs the settle lifecycle's sweep (mutation b).
	skipSweep bool
	// ackEarly makes the consumer acknowledge the log head instead of the
	// poll's NextSeq (mutation c).
	ackEarly bool
}

const defaultDuration = 19 * time.Minute

// ---- the latency gate --------------------------------------------------------

// latencyGate models the origin's round trip on the manual clock. A fake origin
// call parks in wait; the driver releases a parked call once the clock has
// reached its due time, one at a time, so what the daemon stores and when is
// the same on every run.
type latencyGate struct {
	clk *governor.ManualClock

	mu      sync.Mutex
	d       time.Duration
	parked  []*parkedCall
	arrived chan struct{}
}

type parkedCall struct {
	key     string
	due     time.Time
	release chan struct{}
}

func newLatencyGate(clk *governor.ManualClock, d time.Duration) *latencyGate {
	return &latencyGate{clk: clk, d: d, arrived: make(chan struct{}, 1)}
}

func (g *latencyGate) wait(key string) {
	g.mu.Lock()
	p := &parkedCall{key: key, due: g.clk.Now().Add(g.d), release: make(chan struct{})}
	g.parked = append(g.parked, p)
	g.mu.Unlock()
	select {
	case g.arrived <- struct{}{}:
	default:
	}
	<-p.release
}

// first is the parked call that comes due first (ties by key), nil for none.
func (g *latencyGate) first() *parkedCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	var best *parkedCall
	for _, p := range g.parked {
		if best == nil || p.due.Before(best.due) || (p.due.Equal(best.due) && p.key < best.key) {
			best = p
		}
	}
	return best
}

func (g *latencyGate) release(p *parkedCall) {
	g.mu.Lock()
	g.parked = slices.DeleteFunc(g.parked, func(o *parkedCall) bool { return o == p })
	g.mu.Unlock()
	close(p.release)
}

func (g *latencyGate) setLatency(d time.Duration) {
	g.mu.Lock()
	g.d = d
	g.mu.Unlock()
}

// cachedAliases is the alias table read through a memo. The daemon reads the
// alias of every query member on every group call (and AliasByRefFold scans the
// table), about two million reads in a scenario, which costs minutes of real
// time and is not what this test measures. The memo is written through, so the
// store stays the truth; the scenario never renames a PR, so a lookup the memo
// holds cannot be stale.
type cachedAliases struct {
	inner *store.Store

	mu     sync.Mutex
	byNode map[string]store.AliasRow
	byRef  map[string]store.AliasRow
}

func newCachedAliases(st *store.Store) *cachedAliases {
	return &cachedAliases{inner: st, byNode: map[string]store.AliasRow{}, byRef: map[string]store.AliasRow{}}
}

func (c *cachedAliases) PutAlias(ctx context.Context, a store.AliasRow) error {
	if err := c.inner.PutAlias(ctx, a); err != nil {
		return err
	}
	c.mu.Lock()
	c.byNode[a.NodeID] = a
	c.byRef[strings.ToLower(a.Ref)] = a
	c.mu.Unlock()
	return nil
}

func (c *cachedAliases) AliasByNodeID(ctx context.Context, nodeID string) (store.AliasRow, bool, error) {
	c.mu.Lock()
	a, ok := c.byNode[nodeID]
	c.mu.Unlock()
	if ok {
		return a, true, nil
	}
	return c.inner.AliasByNodeID(ctx, nodeID)
}

func (c *cachedAliases) AliasByRefFold(ctx context.Context, ref string) (store.AliasRow, bool, error) {
	c.mu.Lock()
	a, ok := c.byRef[strings.ToLower(ref)]
	c.mu.Unlock()
	if ok {
		return a, true, nil
	}
	return c.inner.AliasByRefFold(ctx, ref)
}

// ---- the world ---------------------------------------------------------------

func nodeOf(i int) string { return fmt.Sprintf("PR_%03d", i) }
func refOf(i int) string  { return fmt.Sprintf("acme-org/widget#%d", i+1) }
func headOf(i, n int) string {
	return fmt.Sprintf("head-%03d-%d", i, n)
}

// queryName names the n-th configured query: the two defaults, then nine more.
func queryName(n int) string {
	switch n {
	case 0:
		return config.DefaultQueryNameMine
	case 1:
		return config.DefaultQueryNameTeam
	}
	return fmt.Sprintf("q%02d", n+1)
}

// queryMembers lists the members of query n: the member PRs i with i mod 11 = n
// or (i / 10) mod 11 = n, so every member PR is in one or two queries and each
// query lists a few dozen.
func queryMembers(n int) []int {
	var out []int
	for i := 0; i < nMembers; i++ {
		if i%11 == n || (i/10)%11 == n {
			out = append(out, i)
		}
	}
	return out
}

// world is the origin's rate-limit state: what GitHub says is left of the
// GraphQL bucket. It falls with every GraphQL point the daemon spends and falls
// further when another process on the token drains it.
type world struct {
	mu        sync.Mutex
	remaining int64
	resetAt   time.Time
}

func (w *world) spend(cost int64) {
	w.mu.Lock()
	w.remaining = max(w.remaining-cost, 0)
	w.mu.Unlock()
}

// nextWindow moves the reset an hour on: the origin's window has reset.
func (w *world) nextWindow(d time.Duration) {
	w.mu.Lock()
	w.resetAt = w.resetAt.Add(d)
	w.mu.Unlock()
}

func (w *world) set(r int64) {
	w.mu.Lock()
	w.remaining = r
	w.mu.Unlock()
}

// readingAfter is the reading a response carries when its call costs cost.
func (w *world) readingAfter(cost int64) (remaining int64, resetAt time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return max(w.remaining-cost, 0), w.resetAt
}

// ---- the fake origin's groups ------------------------------------------------

// groupFetcher is a fake groups.Fetcher with the cost shape of the real one: a
// group is either one GraphQL point per call or one REST core request per PR,
// admitted through the governor, booked as the call's class, and answered after
// the origin's latency. Its content is a pure function of the head and base the
// target carries, so an unchanged PR never changes content.
type groupFetcher struct {
	h      *harness
	group  string
	batch  int
	bucket governor.Bucket
	body   func(t groups.Target) any
}

func (f *groupFetcher) Group() string { return f.group }
func (f *groupFetcher) MaxBatch() int { return f.batch }
func (f *groupFetcher) Spend(class governor.Class, _ int) governor.Request {
	return governor.Request{Bucket: f.bucket, Class: class, Cost: 1}
}

func (f *groupFetcher) Fetch(_ context.Context, call ratefeed.Meta, targets []groups.Target) []groups.Outcome {
	out := make([]groups.Outcome, len(targets))
	tk, err := f.h.gov.Admit(governor.Request{Bucket: f.bucket, Class: call.Class, Cost: 1})
	if err != nil {
		for i := range out {
			out[i] = groups.Outcome{Err: err}
		}
		return out
	}
	f.h.gate.wait(f.group + "/" + targets[0].NodeID)
	_ = tk.Settle(governor.Spend{
		Bucket: f.bucket, Class: call.Class, Purpose: call.Purpose, Group: f.group,
		BatchSize: len(targets), Cost: 1, Outcome: "ok",
	})
	for i, t := range targets {
		raw, merr := json.Marshal(f.body(t))
		if merr != nil {
			out[i] = groups.Outcome{Err: merr}
			continue
		}
		out[i] = groups.Outcome{Content: raw, Stamps: t.Stamps, Cost: 1}
	}
	return out
}

func (h *harness) fetchers() []*groupFetcher {
	return []*groupFetcher{
		{h: h, group: groups.Detail, batch: 25, bucket: governor.BucketGraphQL, body: func(groups.Target) any {
			return detail.Detail{MergeStateStatus: "CLEAN", ReviewRequests: []string{}, Additions: 10, Deletions: 2, ChangedFiles: 3}
		}},
		{h: h, group: groups.Conversation, batch: 1, bucket: governor.BucketGraphQL, body: func(groups.Target) any {
			return conversation.Content{Comments: []schema.PRComment{}, Reviews: []schema.PRReview{}}
		}},
		{h: h, group: groups.Files, batch: 1, bucket: governor.BucketRESTCore, body: func(t groups.Target) any {
			return prfacts.Files{Files: []schema.PRFile{{Path: "src/" + t.Stamps.Head + ".go", Additions: 7, Deletions: 1}}}
		}},
		{h: h, group: groups.Commits, batch: 1, bucket: governor.BucketRESTCore, body: func(t groups.Target) any {
			return prfacts.Commits{Commits: []schema.PRCommit{{SHA: t.Stamps.Head, Author: "phillipgreenii", Message: "change"}}}
		}},
		{h: h, group: groups.Runs, batch: 1, bucket: governor.BucketRESTCore, body: func(t groups.Target) any {
			return runs.Content{Runs: []schema.CIRun{{
				ID: "run-" + t.Stamps.Head, Name: "ci", Status: "completed", Conclusion: "success",
				Provider: runs.ProviderName, HeadSHA: t.Stamps.Head,
				Jobs: []schema.CIJob{{Name: "build", Status: "completed", Conclusion: "success"}},
			}}}
		}},
	}
}

// ---- the fake listing --------------------------------------------------------

// fakeGH answers the membership document of the query runner: query "name" is
// run as the search "is:pr loadtest:<name>" and lists queryMembers. Each answer
// folds in the rate-limit selection the real origin folds in, with the world's
// remaining, so a falling reading reaches the governor the way it does in
// production.
type fakeGH struct{ h *harness }

func (g *fakeGH) Run(_ context.Context, args ...string) ([]byte, error) {
	name := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-f" && strings.HasPrefix(args[i+1], "q=") {
			q := strings.TrimPrefix(args[i+1], "q=")
			if _, rest, ok := strings.Cut(q, "loadtest:"); ok {
				name, _, _ = strings.Cut(rest, " ")
			}
		}
	}
	n := slices.IndexFunc(g.h.queryNames, func(s string) bool { return s == name })
	if n < 0 {
		return nil, fmt.Errorf("loadtest: the fake listing got no query name in %v", args)
	}
	g.h.gate.wait("gh/" + name)
	g.h.rec.noteQueryRun(g.h.clk.Now(), name)

	type node struct {
		ID         string `json:"id"`
		Number     int    `json:"number"`
		Repository struct {
			NameWithOwner string `json:"nameWithOwner"`
		} `json:"repository"`
	}
	var nodes []node
	for _, i := range queryMembers(n) {
		x := node{ID: nodeOf(i), Number: i + 1}
		x.Repository.NameWithOwner = "acme-org/widget"
		nodes = append(nodes, x)
	}
	remaining, resetAt := g.h.world.readingAfter(1)
	body := map[string]any{"data": map[string]any{
		"rateLimit": map[string]any{"cost": 1, "remaining": remaining, "resetAt": resetAt.UTC().Format(time.RFC3339)},
		"search": map[string]any{
			"issueCount": len(nodes),
			"pageInfo":   map[string]any{"hasNextPage": false, "endCursor": ""},
			"nodes":      nodes,
		},
	}}
	return json.Marshal(body)
}

// allQueries is the feed's Queries: a query "all" naming every PR (so one
// consumer sees every row of the log). It never re-runs anything.
type allQueries struct{ refs []string }

func (q allQueries) Known(name string) bool { return name == consumerQuery }
func (q allQueries) Members(context.Context, string) (feed.Members, error) {
	return feed.Members{Refs: q.refs, Baseline: true}, nil
}
func (q allQueries) Run(context.Context, string) error { return nil }

const (
	consumerName  = "load-consumer"
	consumerQuery = "all"
)

// ---- the recorder ------------------------------------------------------------

type storeRec struct {
	at    time.Time
	node  int
	group string
}

type waitRec struct {
	at      time.Time
	kind    string
	wait    time.Duration
	err     string
	refused bool
}

type readingStep struct {
	at        time.Time
	remaining int64
}

type pendRec struct {
	id       string
	deadline time.Time
	first    time.Time
	ended    bool
	endedAt  time.Time
}

type govSample struct {
	at    time.Time
	state governor.State
}

// recorder collects what the checks read. Every method is safe for concurrent
// use: the hooks run on the scheduler's worker goroutine.
type recorder struct {
	mu        sync.Mutex
	spends    []governor.SpendEvent
	stores    []storeRec
	queryRuns []time.Time
	queryHits map[string]int
	settled   map[string]int
	errs      []string
}

func (r *recorder) noteSpend(ev governor.SpendEvent) {
	r.mu.Lock()
	r.spends = append(r.spends, ev)
	r.mu.Unlock()
}

func (r *recorder) noteStore(at time.Time, node string, group string) {
	var i int
	if _, err := fmt.Sscanf(node, "PR_%d", &i); err != nil {
		return
	}
	r.mu.Lock()
	r.stores = append(r.stores, storeRec{at: at, node: i, group: group})
	r.mu.Unlock()
}

func (r *recorder) noteQueryRun(at time.Time, name string) {
	r.mu.Lock()
	r.queryRuns = append(r.queryRuns, at)
	r.queryHits[name]++
	r.mu.Unlock()
}

func (r *recorder) noteErr(s string) {
	r.mu.Lock()
	r.errs = append(r.errs, s)
	r.mu.Unlock()
}

// Detected and Settled make the recorder the change engine's Observer.
func (r *recorder) Detected(string, string, time.Duration) {}

func (r *recorder) Settled(result string) {
	r.mu.Lock()
	r.settled[result]++
	r.mu.Unlock()
}

// ---- the interactive calls ---------------------------------------------------

type call struct {
	kind      string // "summary" or the group Ensure was asked for
	node      string
	pairs     [][2]string // the (entity, group) tasks the call can wait on
	submitted time.Time
	done      chan struct{}
	err       error
}

// ---- the harness -------------------------------------------------------------

type harness struct {
	t   *testing.T
	opt scenarioOpts
	ctx context.Context

	clk    *governor.ManualClock
	t0     time.Time
	gate   *latencyGate
	world  *world
	gov    *governor.Governor
	rig    *groupsvctest.Rig
	chg    *changes.Engine
	life   *changes.Lifecycle
	fd     *feed.Feed
	runner *query.Runner
	rec    *recorder
	cfg    *config.Config
	rng    *rand.Rand

	queryNames []string
	reval      *revalidate.Handler
	govCaps    map[governor.Bucket]governor.Limits // the spec's caps, before any mutation

	// driver state
	worker chan struct{}
	// the burst: how many heads changed and when
	burstHeads int
	burstAt    time.Duration

	// stats of the driver, for the summary line
	stepCalls, batches, released int

	// releaseAll lets a parked origin call go before its due time (the end of
	// the run).
	releaseAll bool
	calls      []*call
	waits      []waitRec
	lastHead   map[int]time.Duration // elapsed time of each PR's last head change
	events     []event
	pend       map[string]*pendRec
	maxPend    int
	late       []string // pending rows still present after a sweep past their deadline
	govs       []govSample
	steps      []readingStep

	cons consumer
}

type event struct {
	at   time.Duration
	kind string
	n    int
}

func newHarness(t *testing.T, o scenarioOpts) *harness {
	t.Helper()
	if o.duration == 0 {
		o.duration = defaultDuration
	}
	h := &harness{
		t: t, opt: o, ctx: context.Background(),
		rec:      &recorder{queryHits: map[string]int{}, settled: map[string]int{}},
		rng:      rand.New(rand.NewSource(o.seed)),
		lastHead: map[int]time.Duration{},
		pend:     map[string]*pendRec{},
	}
	h.cfg = config.Defaults()
	// A fifth of the default ages, so that the run covers three maximum ages in
	// nineteen minutes of fake time instead of the two hours it would take at the
	// defaults (a run at the defaults costs minutes of real time under the race
	// detector). The ratios of the defaults hold: a time to live of a third of
	// the maximum age.
	h.cfg.DetailTTL, h.cfg.DetailMaxAge = config.Duration(2*time.Minute), config.Duration(6*time.Minute)
	h.cfg.ConversationTTL, h.cfg.ConversationMaxAge = config.Duration(2*time.Minute), config.Duration(6*time.Minute)
	h.cfg.CIMaxAge = config.Duration(6 * time.Minute)
	// The REST cap is lowered to half its default, so that it binds inside the
	// nineteen minutes (at the default it binds after about twenty-five of the
	// 150 requests a minute the runs groups ask for). Nothing else is lowered:
	// the GraphQL cap, the reserve, the background share and the REST search cap
	// are the defaults.
	h.cfg.RESTRequestsPerHour = 1500
	h.cfg.PR.Queries = map[string]schema.QueryExpr{}
	for n := 0; n < 11; n++ {
		h.queryNames = append(h.queryNames, queryName(n))
		h.cfg.PR.Queries[queryName(n)] = schema.QueryExpr{"loadtest:" + queryName(n)}
		if n >= 2 {
			// mine and team keep their defaults (60 s and 120 s); the other nine
			// are re-run every five minutes, so that the queries are not most of
			// the GraphQL spend.
			h.cfg.QueryTTL[queryName(n)] = config.Duration(5 * time.Minute)
		}
	}
	h.clk = governor.NewManualClock(groupsvctest.T0)
	h.t0 = h.clk.Now()
	h.gate = newLatencyGate(h.clk, originLatency)
	// The origin's window resets when the drain ends, as GitHub's does: a
	// remaining only rises at the reset, so that is when the governor can see it
	// recover (and when its refusals say to retry).
	h.world = &world{remaining: fullRemaining, resetAt: h.t0.Add(drainEnd)}

	gcfg := ratefeed.GovernorConfig(h.cfg.Settings)
	h.govCaps = map[governor.Bucket]governor.Limits{
		governor.BucketGraphQL: gcfg.GraphQL, governor.BucketRESTCore: gcfg.RESTCore, governor.BucketRESTSearch: gcfg.RESTSearch,
	}
	if o.capScale > 1 {
		gcfg.GraphQL.Cap *= o.capScale
		gcfg.RESTCore.Cap *= o.capScale
		gcfg.RESTSearch.Cap *= o.capScale
	}
	var err error
	h.gov, err = governor.New(gcfg, governor.Options{Clock: h.clk, OnSpend: h.onSpend})
	if err != nil {
		t.Fatal(err)
	}
	h.chg, err = changes.New(changes.Config{Clock: h.clk, SettleTimeout: h.cfg.SettleTimeout.Std(), BodyCap: int(h.cfg.ChangeBodyCap)})
	if err != nil {
		t.Fatal(err)
	}
	h.chg.SetObserver(h.rec)

	st, _, err := store.Open(":memory:", store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h.rig = groupsvctest.NewRig(t, groupsvctest.RigOptions{
		Gov: h.gov, Clock: h.clk, Workers: 1, Settings: &h.cfg.Settings,
		Store:              st,
		AliasStore:         newCachedAliases(st),
		OnGroupStoring:     h.onGroupStoring,
		OnGone:             h.chg.DetectGone,
		OnSummaryRefreshed: func(prev *summary.Summary, next summary.Stored) { h.reval.OnSummaryRefreshed(prev, next) },
		SummaryOnGone:      h.chg.DetectGone,
	})
	h.rig.Persist.Hook = h.onSummaryStoring
	h.rig.Svc.SetOriginHints(h.chg)
	h.rig.Origin.Latency = func() { h.gate.wait("origin") }
	for _, f := range h.fetchers() {
		h.rig.Svc.Register(f)
	}
	h.reval = revalidate.New(revalidate.Config{
		Store: h.rig.Store, Engine: h.rig.Svc, KeepAlive: h.rig.Summary.InKeepAlive, Rules: h.rig.Rules, Clock: h.clk,
		OnError: func(err error) { h.rec.noteErr("revalidate: " + err.Error()) },
	})

	gh := &fakeGH{h: h}
	h.runner, err = query.New(query.Config{
		Settings: h.cfg, Store: h.rig.Store, Identity: h.rig.Aliases, Tracker: h.rig.Summary, GH: gh,
		Governor: h.gov, Feed: ratefeed.New(h.gov, gh, h.clk), Scheduler: h.rig.Sched, Clock: h.clk,
		Changes: h.chg, Confirm: query.NewConfirmer(h.rig.Summary, h.rig.Store),
		Log: func(event string, kv ...any) { h.rec.noteErr(fmt.Sprint("query: ", event, kv)) },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.life = &changes.Lifecycle{Engine: h.chg, Store: h.rig.Store, Queue: h.rig.Svc, Clock: h.clk, Interval: time.Second}

	refs := make([]string, nPRs)
	for i := range refs {
		refs[i] = refOf(i)
	}
	h.fd, err = feed.New(feed.Config{
		Store: h.rig.Store, Queries: allQueries{refs: refs}, Clock: h.clk,
		Retention: h.cfg.ChangeLogRetention.Std(), ConsumerStaleAfter: h.cfg.ConsumerStaleAfter.Std(),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.cons = consumer{h: h, seen: map[int64]bool{}}

	for i := 0; i < nPRs; i++ {
		h.rig.Origin.Set(nodeOf(i), groupsvctest.PR{
			Repo: "acme-org/widget", Number: i + 1, State: "OPEN",
			HeadRefOid: headOf(i, 0), BaseRefOid: "base-1", HeadRefName: fmt.Sprintf("feature-%d", i),
		})
	}
	h.plan()
	return h
}

func (h *harness) onSpend(ev governor.SpendEvent) {
	if ev.Bucket == governor.BucketGraphQL {
		h.world.spend(ev.Cost)
	}
	h.rec.noteSpend(ev)
}

// onSummaryStoring is the persister's hook: the progress record, then the change
// engine's seam (both inside the store transaction, using only tx).
func (h *harness) onSummaryStoring(ctx context.Context, tx *store.Tx, prev *summary.Summary, st summary.Stored, info summary.FetchInfo) error {
	h.rec.noteStore(h.clk.Now(), st.Summary.NodeID, groups.Summary)
	return h.chg.DetectSummary(ctx, tx, prev, st, info)
}

func (h *harness) onGroupStoring(ctx context.Context, tx *store.Tx, prev *store.GroupRow, next store.GroupRow, info groupsvc.RefreshInfo) error {
	h.rec.noteStore(h.clk.Now(), next.ID, next.Group)
	return h.chg.DetectGroup(ctx, tx, prev, next, info)
}

// ---- the plan ----------------------------------------------------------------

// plan lays out the scenario's events on the fake clock, from the seed. The
// numbers: a summary cycle is summary_ttl (60 s), so the first cycle is 0 to 60
// s and the burst cycle (the third) is 120 to 180 s.
func (h *harness) plan() {
	add := func(at time.Duration, kind string, n int) { h.events = append(h.events, event{at, kind, n}) }
	summaryTTL := h.cfg.SummaryTTL.Std()
	add(summaryTTL+10*time.Second, "heads", 5)   // cycle 2: a handful
	add(2*summaryTTL+10*time.Second, "burst", 0) // cycle 3: the mass rebase
	add(3*summaryTTL+10*time.Second, "heads", 5) // cycle 4
	add(4*summaryTTL+10*time.Second, "heads", 5) // cycle 5
	injectEnd := h.opt.duration - 90*time.Second // the quiet end lets every pending row end
	for at := 4 * time.Minute; at < injectEnd; at += time.Minute {
		add(at, "heads", 1) // a trickle of single head changes
	}
	for at := 20 * time.Second; at < injectEnd; at += 30 * time.Second {
		add(at, "interactive", 4) // four callers at once, each for another PR
	}
	for at := 50 * time.Second; at < injectEnd; at += 90 * time.Second {
		add(at, "ensure", 2)
	}
	for at := 20 * time.Second; at <= h.opt.duration; at += 10 * time.Second {
		add(at, "poll", 0)
	}
	// Another process drains the token: the reading falls in steps and recovers
	// when the origin's window resets (see load_test.go, drainStart and
	// drainEnd).
	if h.opt.duration >= drainEnd+6*time.Minute {
		add(drainStart, "reading", 1300)
		add(drainMid, "reading", 1005)
		add(drainEnd, "reset", fullRemaining)
	}
	slices.SortStableFunc(h.events, func(a, b event) int { return int(a.at - b.at) })
}

// fire applies the events due at elapsed.
func (h *harness) fire(elapsed time.Duration) {
	for len(h.events) > 0 && h.events[0].at <= elapsed {
		e := h.events[0]
		h.events = h.events[1:]
		switch e.kind {
		case "heads":
			for _, i := range h.pick(e.n, elapsed) {
				h.changeHead(i, elapsed)
			}
		case "burst":
			n := 100 + h.rng.Intn(21)
			picked := h.pick(n, elapsed)
			for _, i := range picked {
				h.changeHead(i, elapsed)
			}
			h.burstHeads, h.burstAt = len(picked), elapsed
		case "interactive":
			for _, i := range h.rng.Perm(nPRs)[:e.n] {
				h.startCall("summary", i)
			}
		case "ensure":
			gs := []string{groups.Detail, groups.Conversation, groups.Files, groups.Commits, groups.Runs}
			for _, i := range h.rng.Perm(nPRs)[:e.n] {
				h.startCall(gs[h.rng.Intn(len(gs))], i)
			}
		case "poll":
			h.cons.poll()
		case "reading", "reset":
			remaining := int64(e.n)
			if e.kind == "reset" {
				h.world.nextWindow(governor.DefaultWindow)
			}
			h.world.set(remaining)
			if err := h.gov.Observe(governor.Reading{
				Bucket: governor.BucketGraphQL, Limit: fullRemaining, Remaining: remaining, ResetAt: h.world.resetAt,
			}); err != nil {
				h.t.Fatal(err)
			}
			h.steps = append(h.steps, readingStep{at: h.clk.Now(), remaining: remaining})
		}
	}
}

// pick returns n PRs, chosen by the seed among those whose head has not changed
// for four minutes, so a PR never changes twice inside a settle window.
func (h *harness) pick(n int, elapsed time.Duration) []int {
	var cand []int
	for i := 0; i < nPRs; i++ {
		if last, ok := h.lastHead[i]; !ok || elapsed-last >= 4*time.Minute {
			cand = append(cand, i)
		}
	}
	perm := h.rng.Perm(len(cand))
	var out []int
	for _, p := range perm[:min(n, len(perm))] {
		out = append(out, cand[p])
	}
	slices.Sort(out)
	return out
}

func (h *harness) changeHead(i int, elapsed time.Duration) {
	h.lastHead[i] = elapsed
	n := h.rig.Origin.Changes(nodeOf(i)) + 1
	h.rig.Origin.Update(nodeOf(i), func(p *groupsvctest.PR) { p.HeadRefOid = headOf(i, n) })
}

// ---- the driver --------------------------------------------------------------

// start queues the first run of every configured query and the access that
// keeps the non-member PRs alive.
func (h *harness) start() {
	if err := h.runner.Schedule(h.ctx); err != nil {
		h.t.Fatal(err)
	}
	for i := nMembers; i < nPRs; i++ {
		if err := h.rig.Summary.Access(h.ctx, nodeOf(i)); err != nil {
			h.t.Fatal(err)
		}
	}
}

// pump dispatches batches and releases parked origin calls until the one worker
// is busy waiting for the clock (or nothing can go).
func (h *harness) pump() {
	for {
		if h.worker == nil {
			h.stepCalls++
			n := h.rig.Sched.Step(h.ctx)
			if n == 0 {
				return
			}
			h.batches += n
			w := make(chan struct{})
			h.worker = w
			go func() {
				h.rig.Sched.Wait()
				close(w)
			}()
		}
		p := h.gate.first()
		if p == nil {
			select {
			case <-h.worker:
				h.worker = nil
				// A call whose flight just finished runs on, locally, until it
				// returns or queues the next one: let it, so it never overlaps the
				// next batch.
				for _, c := range h.calls {
					h.quiesce(c)
				}
			case <-h.gate.arrived:
			case <-time.After(60 * time.Second):
				h.t.Fatal("loadtest: the scheduler's worker neither finished nor reached the origin within 60 s of real time")
			}
			continue
		}
		if p.due.After(h.clk.Now()) && !h.releaseAll {
			return
		}
		h.released++
		h.gate.release(p)
	}
}

func (h *harness) startCall(kind string, i int) {
	node := nodeOf(i)
	for _, c := range h.calls {
		if c.node == node {
			return // one call per PR at a time: the pairs a call waits on stay its own
		}
	}
	c := &call{kind: kind, node: node, submitted: h.clk.Now(), done: make(chan struct{})}
	c.pairs = [][2]string{{node, groups.Summary}}
	if kind != "summary" {
		c.pairs = append(c.pairs, [2]string{node, kind})
	}
	h.calls = append(h.calls, c)
	go func() {
		defer close(c.done)
		if kind == "summary" {
			res := h.rig.Summary.InteractiveFresh(h.ctx, []string{node})
			if len(res) == 1 {
				c.err = res[0].Err
			}
			return
		}
		res, err := h.rig.Svc.Ensure(h.ctx, node, []string{kind}, groupsvc.EnsureOptions{Fresh: true})
		c.err = err
		for _, g := range res.Groups {
			if g.Err != nil && c.err == nil {
				c.err = g.Err
			}
		}
	}()
	h.quiesce(c)
}

// quiet reports whether the call has returned or is waiting for a flight the
// scheduler holds (queued at class 1, or running).
func (h *harness) quiet(c *call) bool {
	select {
	case <-c.done:
		return true
	default:
	}
	for _, p := range c.pairs {
		class, queued, running := h.rig.Sched.Inspect(p[0], p[1])
		if running || (queued && class == governor.ClassInteractive) {
			return true
		}
	}
	return false
}

// quiesce waits, in real time, until the call is quiet: its goroutine does only
// local store work between its flights, so it always gets there.
func (h *harness) quiesce(c *call) {
	deadline := time.Now().Add(30 * time.Second)
	for !h.quiet(c) {
		if time.Now().After(deadline) {
			h.t.Fatalf("loadtest: the %s call for %s neither returned nor queued a flight within 30 s of real time", c.kind, c.node)
		}
		runtime.Gosched()
		time.Sleep(20 * time.Microsecond)
	}
}

// settleCalls records the wait of every call that has returned.
func (h *harness) settleCalls() {
	var keep []*call
	for _, c := range h.calls {
		h.quiesce(c)
		select {
		case <-c.done:
			w := waitRec{at: h.clk.Now(), kind: c.kind, wait: h.clk.Now().Sub(c.submitted)}
			if c.err != nil {
				w.err = c.err.Error()
				w.refused = errors.Is(c.err, governor.ErrRefused)
			}
			h.waits = append(h.waits, w)
		default:
			keep = append(keep, c)
		}
	}
	h.calls = keep
}

// sweep ends the pending rows past their deadline and records what the pending
// table holds.
func (h *harness) sweep(now time.Time) {
	if !h.opt.skipSweep {
		if _, err := h.life.Sweep(h.ctx); err != nil {
			h.t.Fatal(err)
		}
	}
	rows, err := h.rig.Store.ListPending(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	h.maxPend = max(h.maxPend, len(rows))
	seen := map[string]bool{}
	for _, p := range rows {
		k := p.ID + "@" + p.Deadline.Format(time.RFC3339Nano)
		seen[k] = true
		if _, ok := h.pend[k]; !ok {
			h.pend[k] = &pendRec{id: p.ID, deadline: p.Deadline, first: now}
		}
		if !p.Deadline.After(now) {
			h.late = append(h.late, fmt.Sprintf("%s (deadline %s) still pending at %s after a sweep", p.ID, p.Deadline.Format("15:04:05.000"), now.Format("15:04:05.000")))
		}
	}
	for k, r := range h.pend {
		if !r.ended && !seen[k] {
			r.ended, r.endedAt = true, now
		}
	}
	h.govs = append(h.govs, govSample{at: now, state: h.gov.State()})
}

// run drives the scenario to its end and collects the result.
func (h *harness) run() *result {
	h.start()
	ticks := int(h.opt.duration / tick)
	for k := 1; k <= ticks; k++ {
		h.clk.Advance(tick)
		elapsed := time.Duration(k) * tick
		h.fire(elapsed)
		h.pump()
		h.settleCalls()
		if k%sweepEvery == 0 {
			h.sweep(h.clk.Now())
		}
		h.cons.step()
	}
	h.finish()
	return h.collect()
}

// finish lets everything in flight end: the origin answers at once, every call
// returns, a last sweep ends the rows past their deadline, and the consumer
// reads the log to its head.
func (h *harness) finish() {
	h.gate.setLatency(0)
	h.releaseAll = true
	for {
		h.pump()
		h.settleCalls()
		if h.worker == nil && len(h.calls) == 0 {
			break
		}
	}
	h.sweep(h.clk.Now())
	h.cons.drain()
}

// ---- the consumer ------------------------------------------------------------

// consumer polls the feed and acknowledges, one tick apart: it polls at the end
// of one tick and acknowledges the poll's NextSeq at the end of the next, so rows
// appended in between are not covered by the acknowledgement.
type consumer struct {
	h *harness

	seen      map[int64]bool
	delivered []int64
	acked     int64
	twice     []string
	next      int64
	pollNow   bool
	ackDue    bool
}

func (c *consumer) poll() { c.pollNow = true }

func (c *consumer) step() {
	if c.ackDue {
		c.ack()
	}
	if c.pollNow {
		c.pollNow = false
		c.doPoll()
		c.ackDue = true
	}
}

func (c *consumer) doPoll() {
	res, err := c.h.fd.Poll(c.h.ctx, feed.PollRequest{Kind: feed.KindPR, Consumer: consumerName, Query: consumerQuery})
	if err != nil {
		c.h.t.Fatalf("poll: %v", err)
	}
	for _, r := range res.Rows {
		if r.Seq <= c.acked || c.seen[r.Seq] {
			c.twice = append(c.twice, fmt.Sprintf("seq %d was delivered again after it was acknowledged or delivered", r.Seq))
		}
		c.seen[r.Seq] = true
		c.delivered = append(c.delivered, r.Seq)
	}
	c.next = res.NextSeq
}

func (c *consumer) ack() {
	c.ackDue = false
	n := c.next
	if c.h.opt.ackEarly {
		head, err := c.h.rig.Store.ChangeHead(c.h.ctx)
		if err != nil {
			c.h.t.Fatal(err)
		}
		n = head
	}
	if err := c.h.fd.Ack(c.h.ctx, feed.KindPR, consumerName, consumerQuery, n); err != nil {
		c.h.t.Fatalf("ack %d: %v", n, err)
	}
	c.acked = max(c.acked, n)
}

// drain reads the feed until it has nothing more.
func (c *consumer) drain() {
	if c.ackDue {
		c.ack()
	}
	for i := 0; i < 20; i++ {
		before := len(c.delivered)
		c.doPoll()
		c.ack()
		if len(c.delivered) == before {
			return
		}
	}
}

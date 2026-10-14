package ciops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc/groupsvctest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/runs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

const (
	node1 = "PR_1"
	node2 = "PR_2"
	// head1 is the head the fake origin gives PR_1 (groupsvctest.Origin's default).
	head1 = "head-1"
	repo1 = "acme-org/widget"
	// ref1 is PR_1 as a caller writes it.
	ref1 = "acme-org/widget#1"
)

// reply is an `gh api --include` output with the rate headers of the core bucket.
func reply(status int, body string) []byte {
	return []byte(fmt.Sprintf("HTTP/2.0 %d X\r\nX-RateLimit-Limit: 5000\r\nX-RateLimit-Remaining: 4990\r\nX-RateLimit-Resource: core\r\n\r\n%s", status, body))
}

// ghAnswer is one canned answer of the fake gh.
type ghAnswer struct {
	status int
	body   string
	// err is returned beside the output, as gh does for an HTTP error status.
	err error
}

// fakeGH is the `gh` the daemon's Feed runs: CANNED answers only, every call
// recorded. No real request ever leaves the test. A rerun is a `gh api --include
// repos/<repo>/actions/runs/<id>/rerun-failed-jobs --method POST` call.
type fakeGH struct {
	mu sync.Mutex
	// runs answers a runs list by branch and by the 1-based number of the request
	// for that branch; the default is an empty list.
	runs func(branch string, n int) ghAnswer
	// jobs answers a job list; the default is no jobs.
	jobs func(runID string, attempt int) ghAnswer
	// post answers a rerun by the 1-based number of the POST; the default is
	// GitHub's 201 with an empty object.
	post func(endpoint string, n int) ghAnswer
	// onPost runs inside the POST before it answers (to block it).
	onPost func(endpoint string)

	runsCalls []string
	jobCalls  []string
	posts     []string
	argv      [][]string
	byBranch  map[string]int
}

func (g *fakeGH) Run(_ context.Context, args ...string) ([]byte, error) {
	g.mu.Lock()
	g.argv = append(g.argv, append([]string(nil), args...))
	if len(args) < 3 || args[0] != "api" || args[1] != "--include" {
		g.mu.Unlock()
		return nil, fmt.Errorf("fakeGH: unexpected call %v", args)
	}
	endpoint := args[2]
	method := "GET"
	for i, a := range args {
		if a == "--method" && i+1 < len(args) {
			method = args[i+1]
		}
	}
	var (
		ans    ghAnswer
		onPost func(string)
	)
	switch {
	case method == "POST":
		g.posts = append(g.posts, endpoint)
		ans = ghAnswer{status: 201, body: `{}`}
		if g.post != nil {
			ans = g.post(endpoint, len(g.posts))
		}
		onPost = g.onPost
	case strings.Contains(endpoint, "/jobs"):
		g.jobCalls = append(g.jobCalls, endpoint)
		parts := strings.Split(strings.SplitN(endpoint, "?", 2)[0], "/")
		// repos/o/r/actions/runs/<id>/attempts/<n>/jobs
		var attempt int
		fmt.Sscanf(parts[7], "%d", &attempt)
		ans = ghAnswer{status: 200, body: `{"total_count":0,"jobs":[]}`}
		if g.jobs != nil {
			ans = g.jobs(parts[5], attempt)
		}
	default:
		g.runsCalls = append(g.runsCalls, endpoint)
		u, err := url.Parse(endpoint)
		if err != nil {
			g.mu.Unlock()
			return nil, err
		}
		branch := u.Query().Get("branch")
		if g.byBranch == nil {
			g.byBranch = map[string]int{}
		}
		g.byBranch[branch]++
		ans = ghAnswer{status: 200, body: runsBody()}
		if g.runs != nil {
			ans = g.runs(branch, g.byBranch[branch])
		}
	}
	g.mu.Unlock()
	if onPost != nil {
		onPost(endpoint)
	}
	return reply(ans.status, ans.body), ans.err
}

func (g *fakeGH) postCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.posts)
}

func (g *fakeGH) runsCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.runsCalls)
}

func (g *fakeGH) postList() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.posts...)
}

func (g *fakeGH) argvOf(i int) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.argv[i]
}

// runJSON is one REST workflow run.
func runJSON(id int, name, sha, status, conclusion string, attempt int) string {
	c := "null"
	if conclusion != "" {
		c = fmt.Sprintf("%q", conclusion)
	}
	return fmt.Sprintf(`{"id":%d,"name":%q,"head_sha":%q,"status":%q,"conclusion":%s,"run_attempt":%d,"html_url":"https://example.invalid/runs/%d"}`,
		id, name, sha, status, c, attempt, id)
}

func runsBody(rs ...string) string {
	return `{"total_count":` + fmt.Sprint(len(rs)) + `,"workflow_runs":[` + strings.Join(rs, ",") + `]}`
}

func ok(body string) ghAnswer { return ghAnswer{status: 200, body: body} }

// recordingFetcher is the real runs fetcher, recording the call each Fetch is
// made as (class and purpose), so the precondition read (class 2, purpose write)
// and the group refreshes (class 2, purpose refresh) can be told apart.
type recordingFetcher struct {
	groups.Fetcher
	mu    sync.Mutex
	calls []ratefeed.Meta
}

func (r *recordingFetcher) Fetch(ctx context.Context, call ratefeed.Meta, targets []groups.Target) []groups.Outcome {
	r.mu.Lock()
	r.calls = append(r.calls, call)
	r.mu.Unlock()
	return r.Fetcher.Fetch(ctx, call, targets)
}

func (r *recordingFetcher) metas() []ratefeed.Meta {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ratefeed.Meta(nil), r.calls...)
}

// countBy counts the recorded calls with the purpose.
func (r *recordingFetcher) countBy(p governor.Purpose) int {
	n := 0
	for _, m := range r.metas() {
		if m.Purpose == p {
			n++
		}
	}
	return n
}

// env is a real store, scheduler, summary service, group engine and governor on
// a manual clock, with the real runs fetcher over the canned gh.
type env struct {
	t   *testing.T
	rig *groupsvctest.Rig
	gh  *fakeGH
	gov *governor.Governor
	clk *governor.ManualClock
	rec *recordingFetcher
	h   map[string]scriptout.OpHandler

	mu        sync.Mutex
	events    []governor.SpendEvent
	refreshed []store.GroupRow
}

type envOptions struct {
	// govConfig replaces the governor's configuration.
	govConfig *governor.Config
	// engineRuns replaces the runs fetcher the engine refreshes with; the
	// precondition read keeps using the real one.
	engineRuns groups.Fetcher
	// logs is the ops' log reader.
	logs Logs
	// gh is the canned gh; nil means a default one.
	gh *fakeGH
}

func newEnv(t *testing.T, o envOptions) *env {
	t.Helper()
	e := &env{t: t, gh: o.gh, clk: governor.NewManualClock(groupsvctest.T0)}
	if e.gh == nil {
		e.gh = &fakeGH{}
	}
	cfg := governor.Config{
		GraphQL: governor.Limits{Cap: 1000}, RESTCore: governor.Limits{Cap: 1000}, RESTSearch: governor.Limits{Cap: 1000},
		BackgroundShare: 1,
	}
	if o.govConfig != nil {
		cfg = *o.govConfig
	}
	gov, err := governor.New(cfg, governor.Options{
		Clock: e.clk,
		OnSpend: func(ev governor.SpendEvent) {
			e.mu.Lock()
			e.events = append(e.events, ev)
			e.mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.gov = gov
	e.rig = groupsvctest.NewRig(t, groupsvctest.RigOptions{
		Gov: gov, Clock: e.clk,
		OnGroupRefreshed: func(_ *store.GroupRow, next store.GroupRow, _ groupsvc.RefreshInfo) {
			e.mu.Lock()
			e.refreshed = append(e.refreshed, next)
			e.mu.Unlock()
		},
	})
	feed := ratefeed.New(gov, e.gh, e.clk)
	real := &runs.Fetcher{
		Feed: feed, Store: e.rig.Store, Rules: *e.rig.Rules, Settings: e.rig.Settings, Clock: e.clk,
		Retry: &github.RetryPolicy{MaxAttempts: 1},
	}
	e.rec = &recordingFetcher{Fetcher: real}
	if o.engineRuns != nil {
		e.rig.Svc.Register(o.engineRuns)
	} else {
		e.rig.Svc.Register(e.rec)
	}
	e.h = Handlers(Deps{Engine: e.rig.Svc, Feed: feed, Logs: o.logs, Runs: e.rec, Clock: e.clk})
	e.rig.Start()
	return e
}

// seedPR stores the summary of the node id as the fake origin answers it.
func (e *env) seedPR(node string) {
	e.t.Helper()
	e.rig.SeedSummary(node, e.clk.Now())
}

// seedRuns stores a runs group row for the node id; stale makes it past its
// fresh_until (but inside expiry).
func (e *env) seedRuns(node string, stale bool, rs ...schema.CIRun) store.GroupRow {
	e.t.Helper()
	content := runs.Content{Runs: rs}
	raw, err := json.Marshal(content)
	if err != nil {
		e.t.Fatal(err)
	}
	sum := e.rig.Origin.Summary(node)
	now := e.clk.Now()
	row := store.GroupRow{
		ID: node, Group: groups.Runs, Content: raw, Fingerprint: runs.Fingerprint(content),
		FetchedAt: now.Add(-time.Minute), FreshUntil: now.Add(time.Hour), MaxAgeAt: now.Add(2 * time.Hour),
		FetchedForHead: sum.HeadRefOid, FetchedForBase: sum.BaseRefOid, Generation: 1,
	}
	if stale {
		row.FetchedAt = now.Add(-time.Hour)
		row.FreshUntil = now.Add(-time.Minute)
		row.MaxAgeAt = time.Time{}
	}
	e.rig.SeedGroup(row)
	return row
}

// call runs an op handler with a bounded context.
func (e *env) call(op string, args any) (any, error) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return e.callCtx(ctx, op, args)
}

func (e *env) callCtx(ctx context.Context, op string, args any) (any, error) {
	e.t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		e.t.Fatal(err)
	}
	h, ok := e.h[op]
	if !ok {
		e.t.Fatalf("no handler for %s", op)
	}
	return h.Handle(ctx, raw)
}

// spends returns the booked spends of the bucket.
func (e *env) spends(b governor.Bucket) []governor.SpendEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []governor.SpendEvent
	for _, ev := range e.events {
		if ev.Bucket == b {
			out = append(out, ev)
		}
	}
	return out
}

func (e *env) refreshes() []store.GroupRow {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]store.GroupRow(nil), e.refreshed...)
}

// eventually waits for cond, failing the test after a bound.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// code names the wire code of err: one of the seven sentinels, "" for nil.
func code(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, scriptout.ErrInvalidArgument):
		return "invalid_argument"
	case errors.Is(err, scriptout.ErrNotFound):
		return "not_found"
	case errors.Is(err, scriptout.ErrUnauthenticated):
		return "unauthenticated"
	case errors.Is(err, scriptout.ErrUnavailable):
		return "unavailable"
	}
	return "other"
}

// ciRun builds a stored run.
func ciRun(id, name, sha, status, conclusion string) schema.CIRun {
	return schema.CIRun{
		ID: id, Name: name, Status: status, Conclusion: conclusion, URL: "https://example.invalid/runs/" + id,
		Provider: runs.ProviderName, HeadSHA: sha, Repo: repo1, PRID: ref1, Attempt: 1,
	}
}

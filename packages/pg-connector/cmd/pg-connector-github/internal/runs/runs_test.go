package runs

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

const head = "head-new"

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// reply is an `gh api --include` output with the rate headers of the core bucket.
func reply(status int, text, body string) []byte {
	return []byte(fmt.Sprintf("HTTP/2.0 %d %s\r\nX-RateLimit-Limit: 5000\r\nX-RateLimit-Remaining: 4990\r\nX-RateLimit-Resource: core\r\n\r\n%s", status, text, body))
}

func ok(body string) []byte { return reply(200, "OK", body) }

// fakeGH answers by endpoint and records every call's endpoint.
type fakeGH struct {
	runs      func(n int) ([]byte, error) // n is the 1-based number of runs requests
	jobs      func(runID string, attempt, n int) ([]byte, error)
	runsCalls []string
	jobCalls  []string
	argv      [][]string
}

func (g *fakeGH) Run(_ context.Context, args ...string) ([]byte, error) {
	g.argv = append(g.argv, args)
	endpoint := args[len(args)-1]
	if strings.Contains(endpoint, "/jobs") {
		g.jobCalls = append(g.jobCalls, endpoint)
		var id string
		var attempt int
		parts := strings.Split(strings.SplitN(endpoint, "?", 2)[0], "/")
		// repos/o/r/actions/runs/<id>/attempts/<n>/jobs
		id = parts[5]
		_, _ = fmt.Sscanf(parts[7], "%d", &attempt) // malformed attempt segment leaves attempt at 0
		return g.jobs(id, attempt, len(g.jobCalls))
	}
	g.runsCalls = append(g.runsCalls, endpoint)
	return g.runs(len(g.runsCalls))
}

type sleeps struct{ delays []time.Duration }

func (s *sleeps) sleep(_ context.Context, d time.Duration) error {
	s.delays = append(s.delays, d)
	return nil
}

type env struct {
	f   *Fetcher
	gh  *fakeGH
	gov *governor.Governor
	st  *store.Store
	sl  *sleeps
	clk *governor.ManualClock
}

func newEnv(t *testing.T, gh *fakeGH) *env {
	t.Helper()
	clk := governor.NewManualClock(t0)
	cfg := config.Defaults().Settings
	gov, err := governor.New(ratefeed.GovernorConfig(cfg), governor.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sl := &sleeps{}
	policy := github.DefaultRetryPolicy()
	policy.Sleep = sl.sleep
	f := &Fetcher{
		Feed: ratefeed.New(gov, gh, clk), Store: st, Rules: *groups.NewRules(cfg),
		Settings: cfg, Clock: clk, Retry: &policy,
	}
	return &env{f: f, gh: gh, gov: gov, st: st, sl: sl, clk: clk}
}

var background = ratefeed.Meta{Class: governor.ClassKeepAlive, Purpose: governor.PurposeRefresh}

func target(t *testing.T, branch string) groups.Target {
	t.Helper()
	ref, err := entity.ParseRef("phillipgreenii/widgets#42")
	if err != nil {
		t.Fatal(err)
	}
	return groups.Target{NodeID: "PR_42", Ref: ref, Stamps: groups.Stamps{Head: head, Base: "base-1"}, HeadRefName: branch, State: "OPEN"}
}

func (e *env) fetch(t *testing.T, tg groups.Target) groups.Outcome {
	t.Helper()
	got := e.f.Fetch(context.Background(), background, []groups.Target{tg})
	if len(got) != 1 {
		t.Fatalf("Fetch returned %d outcomes", len(got))
	}
	return got[0]
}

func restSpent(t *testing.T, gov *governor.Governor) int64 {
	t.Helper()
	bs, _ := gov.State().Bucket(governor.BucketRESTCore)
	return bs.Spent
}

// run builds one REST workflow run as JSON.
func run(id int, name, sha, status, conclusion string, attempt int) string {
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

func jobsBody(js ...string) string { return `{"total_count":1,"jobs":[` + strings.Join(js, ",") + `]}` }

func job(id int, name, status, conclusion string) string {
	return fmt.Sprintf(`{"id":%d,"name":%q,"status":%q,"conclusion":%q,"html_url":"https://example.invalid/job/%d"}`, id, name, status, conclusion, id)
}

func TestFetcher_Identity(t *testing.T) {
	f := &Fetcher{}
	if f.Group() != groups.Runs || f.Group() != "runs" || f.MaxBatch() != 1 {
		t.Errorf("Group %q MaxBatch %d", f.Group(), f.MaxBatch())
	}
	req := f.Spend(governor.ClassInteractive, 3)
	if req.Bucket != governor.BucketRESTCore || req.Class != governor.ClassInteractive || req.Cost != 1 {
		t.Errorf("Spend = %+v", req)
	}
}

func TestFetch_MapsRunsLikeTheOldListRuns(t *testing.T) {
	gh := &fakeGH{
		runs: func(int) ([]byte, error) { return ok(fixture(t, "runs_branch.json")), nil },
		jobs: func(string, int, int) ([]byte, error) { return ok(fixture(t, "jobs_ci_failed.json")), nil },
	}
	e := newEnv(t, gh)
	o := e.fetch(t, target(t, "feat/widget"))
	if o.Err != nil || o.LastError != "" {
		t.Fatalf("err %v last %q", o.Err, o.LastError)
	}
	if len(gh.runsCalls) != 1 || len(gh.jobCalls) != 1 {
		t.Fatalf("runs requests %d jobs requests %d, want 1 and 1", len(gh.runsCalls), len(gh.jobCalls))
	}
	if want := "repos/phillipgreenii/widgets/actions/runs?branch=feat%2Fwidget&per_page=100"; gh.runsCalls[0] != want {
		t.Errorf("runs endpoint = %q, want %q", gh.runsCalls[0], want)
	}
	if got := gh.argv[0]; got[0] != "api" || got[1] != "--include" {
		t.Errorf("argv = %v, want api --include <endpoint>", got)
	}
	if want := "repos/phillipgreenii/widgets/actions/runs/3001/attempts/2/jobs?per_page=100"; gh.jobCalls[0] != want {
		t.Errorf("jobs endpoint = %q, want %q", gh.jobCalls[0], want)
	}
	c, err := Decode(o.Content)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range c.Runs {
		ids = append(ids, r.ID)
	}
	if strings.Join(ids, ",") != "3001,3002,3003,3004" {
		t.Errorf("run order %v, want GitHub's newest-first", ids)
	}
	first := c.Runs[0]
	want := schema.CIRun{
		ID: "3001", Name: "ci", Status: "completed", Conclusion: "failure",
		URL: "https://github.com/phillipgreenii/widgets/actions/runs/3001", Provider: "github-actions",
		HeadSHA: head, Repo: "phillipgreenii/widgets", PRID: "phillipgreenii/widgets#42", Attempt: 2,
		Jobs: []schema.CIJob{
			{ID: "70010", Name: "changes", Status: "completed", Conclusion: "success", URL: "https://github.com/phillipgreenii/widgets/actions/runs/3001/job/70010"},
			{ID: "70011", Name: "build-test-validate", Status: "completed", Conclusion: "failure", URL: "https://github.com/phillipgreenii/widgets/actions/runs/3001/job/70011"},
		},
	}
	if fmt.Sprintf("%+v", first) != fmt.Sprintf("%+v", want) {
		t.Errorf("run 0 = %+v\nwant   %+v", first, want)
	}
	if c.Runs[1].Status != "completed" || c.Runs[1].Conclusion != "success" || c.Runs[1].Jobs != nil {
		t.Errorf("run 1 = %+v (lower-cased, no jobs)", c.Runs[1])
	}
	if c.Runs[2].Conclusion != "" || c.Runs[3].Jobs != nil {
		t.Errorf("older-head runs: %+v %+v", c.Runs[2], c.Runs[3])
	}
	if c.Runs[0].AsOf != "" || c.Runs[0].Stale {
		t.Errorf("as_of/stale must stay empty, got %q %v", c.Runs[0].AsOf, c.Runs[0].Stale)
	}
	if o.Stamps != (groups.Stamps{Head: head, Base: "base-1"}) {
		t.Errorf("stamps = %+v", o.Stamps)
	}
	// Both requests were admitted to the REST core bucket and booked with a reading.
	if got := restSpent(t, e.gov); got != 2 || o.Cost != 2 {
		t.Errorf("REST core spent %d, outcome cost %d, want 2 and 2", got, o.Cost)
	}
	bs, _ := e.gov.State().Bucket(governor.BucketRESTCore)
	if bs.Remaining != 4990 {
		t.Errorf("REST core remaining = %d, want the header's 4990", bs.Remaining)
	}
	if g, _ := e.gov.State().Bucket(governor.BucketGraphQL); g.Spent != 0 {
		t.Errorf("GraphQL spent %d, want 0", g.Spent)
	}
	// The outcome's fresh_until is the failing case: ci_failed_ttl from the fetch.
	if !o.FreshUntil.Equal(t0.Add(5 * time.Minute)) {
		t.Errorf("FreshUntil = %v, want t0+5m", o.FreshUntil)
	}
}

func TestFetch_TerminalPRSetsNoFreshUntil(t *testing.T) {
	gh := &fakeGH{runs: func(int) ([]byte, error) { return ok(runsBody(run(1, "ci", head, "completed", "success", 1))), nil }}
	e := newEnv(t, gh)
	tg := target(t, "feat/widget")
	tg.Terminal, tg.State = true, "MERGED"
	if o := e.fetch(t, tg); o.Err != nil || !o.FreshUntil.IsZero() {
		t.Errorf("err %v FreshUntil %v, want none", o.Err, o.FreshUntil)
	}
}

func TestFetch_JobResultsAreCachedByRunAndAttempt(t *testing.T) {
	failedAttempt := func(attempt int) string { return runsBody(run(1, "ci", head, "completed", "failure", attempt)) }
	current := failedAttempt(1)
	gh := &fakeGH{
		runs: func(int) ([]byte, error) { return ok(current), nil },
		jobs: func(string, int, int) ([]byte, error) {
			return ok(jobsBody(job(10, "build", "completed", "failure"))), nil
		},
	}
	e := newEnv(t, gh)
	tg := target(t, "main")

	o1 := e.fetch(t, tg)
	if len(gh.jobCalls) != 1 {
		t.Fatalf("first refresh: %d job requests, want 1", len(gh.jobCalls))
	}
	row, found, err := e.st.GetJobs(context.Background(), "1", 1)
	if err != nil || !found || len(row.Jobs) != 1 || row.Jobs[0].Name != "build" || row.Jobs[0].Conclusion != "failure" || !row.FetchedAt.Equal(t0) {
		t.Errorf("job row = %+v found %v err %v", row, found, err)
	}
	o2 := e.fetch(t, tg)
	if len(gh.jobCalls) != 1 || len(gh.runsCalls) != 2 {
		t.Errorf("second refresh: %d job requests (want still 1), %d runs requests (want 2)", len(gh.jobCalls), len(gh.runsCalls))
	}
	if o2.Fingerprint != o1.Fingerprint || string(o2.Content) != string(o1.Content) {
		t.Errorf("the cached refresh changed the content")
	}
	// A newly completed attempt of the same run is a different (run, attempt): one fetch.
	current = failedAttempt(2)
	e.fetch(t, tg)
	if len(gh.jobCalls) != 2 || !strings.Contains(gh.jobCalls[1], "/attempts/2/jobs") {
		t.Errorf("new attempt: job requests %v", gh.jobCalls)
	}
}

func TestFetch_OnlyCurrentHeadNonPassingCompletedRunsCostAJobFetch(t *testing.T) {
	gh := &fakeGH{
		runs: func(int) ([]byte, error) {
			return ok(runsBody(
				run(1, "a", head, "completed", "success", 1),
				run(2, "b", head, "completed", "skipped", 1),
				run(3, "c", head, "in_progress", "", 1),
				run(4, "d", "head-old", "completed", "failure", 1),
				run(5, "e", head, "completed", "neutral", 1),
				run(6, "f", head, "completed", "action_required", 1),
				run(7, "g", head, "completed", "timed_out", 1),
			)), nil
		},
		jobs: func(string, int, int) ([]byte, error) { return ok(jobsBody()), nil },
	}
	e := newEnv(t, gh)
	e.fetch(t, target(t, "main"))
	if len(gh.jobCalls) != 1 || !strings.Contains(gh.jobCalls[0], "/runs/7/") {
		t.Errorf("job requests = %v, want only run 7 (timed_out on the current head)", gh.jobCalls)
	}
}

func TestFetch_AtMostTenJobFetchesPerRefresh(t *testing.T) {
	var rs []string
	for i := 1; i <= 14; i++ {
		rs = append(rs, run(i, "ci", head, "completed", "failure", 1))
	}
	gh := &fakeGH{
		runs: func(int) ([]byte, error) { return ok(runsBody(rs...)), nil },
		jobs: func(string, int, int) ([]byte, error) { return ok(jobsBody(job(1, "x", "completed", "failure"))), nil },
	}
	e := newEnv(t, gh)
	o := e.fetch(t, target(t, "main"))
	if len(gh.jobCalls) != 10 || o.Cost != 11 {
		t.Errorf("job requests %d cost %d, want 10 and 11", len(gh.jobCalls), o.Cost)
	}
	c, _ := Decode(o.Content)
	for i, r := range c.Runs {
		if (i < 10) != (len(r.Jobs) == 1) {
			t.Errorf("run %d (newest first) has %d jobs", i, len(r.Jobs))
		}
	}
	// The next refresh serves the first ten from the cache and fetches the rest.
	e.fetch(t, target(t, "main"))
	if len(gh.jobCalls) != 14 {
		t.Errorf("after the second refresh: %d job requests, want 14", len(gh.jobCalls))
	}
}

func TestFetch_FailedJobFetchKeepsJobsAndIsNotAChange(t *testing.T) {
	fail := true
	gh := &fakeGH{
		runs: func(int) ([]byte, error) {
			return ok(runsBody(run(1, "ci", head, "completed", "failure", 1), run(2, "lint", head, "completed", "cancelled", 1))), nil
		},
		jobs: func(id string, _, _ int) ([]byte, error) {
			if id == "1" {
				return ok(jobsBody(job(10, "build", "completed", "failure"))), nil
			}
			if fail {
				return reply(500, "Internal Server Error", `{"message":"boom"}`), errors.New("gh: HTTP 500")
			}
			return ok(jobsBody(job(20, "lint", "completed", "cancelled"))), nil
		},
	}
	e := newEnv(t, gh)
	tg := target(t, "main")
	o1 := e.fetch(t, tg)
	if o1.Err != nil || !strings.Contains(o1.LastError, "run 2 attempt 1") || strings.Contains(o1.LastError, "run 1 ") {
		t.Fatalf("err %v LastError %q, want a note about run 2 only", o1.Err, o1.LastError)
	}
	c1, _ := Decode(o1.Content)
	if len(c1.Runs[0].Jobs) != 1 || len(c1.Runs[1].Jobs) != 0 {
		t.Errorf("jobs after a failed fetch: %+v / %+v (run 1's kept, run 2's none)", c1.Runs[0].Jobs, c1.Runs[1].Jobs)
	}
	if _, found, _ := e.st.GetJobs(context.Background(), "2", 1); found {
		t.Error("a failed fetch stored a job row")
	}
	// Failing again, content and fingerprint do not move.
	o2 := e.fetch(t, tg)
	if o2.Fingerprint != o1.Fingerprint || o2.LastError == "" {
		t.Errorf("fingerprint moved on a repeated failure (%q vs %q) or LastError empty", o2.Fingerprint, o1.Fingerprint)
	}
	// A later success DOES change the content, and clears LastError.
	fail = false
	o3 := e.fetch(t, tg)
	if o3.Fingerprint == o1.Fingerprint || o3.LastError != "" {
		t.Errorf("recovery: fingerprint unchanged %v LastError %q", o3.Fingerprint == o1.Fingerprint, o3.LastError)
	}
}

func TestFingerprint_Signals(t *testing.T) {
	base := func() Content {
		return Content{Runs: []schema.CIRun{{
			ID: "1", Name: "ci", Status: "completed", Conclusion: "failure", HeadSHA: head, Attempt: 1,
			URL: "u1", Jobs: []schema.CIJob{{ID: "10", Name: "build", Status: "completed", Conclusion: "failure", URL: "j1"}},
		}}}
	}
	fp := Fingerprint(base())

	// Not signals: urls, ids, as_of, stale, provider, repo, pr_id, job order.
	same := base()
	same.Runs[0].URL, same.Runs[0].Jobs[0].URL, same.Runs[0].Jobs[0].ID = "other", "other", "99"
	same.Runs[0].AsOf, same.Runs[0].Stale = "2026-01-01T00:00:00Z", true
	if Fingerprint(same) != fp {
		t.Error("url, job id, as_of or stale moved the fingerprint")
	}
	// A second job failing, or any covered field, does.
	moves := map[string]func(c *Content){
		"second job": func(c *Content) {
			c.Runs[0].Jobs = append(c.Runs[0].Jobs, schema.CIJob{Name: "lint", Status: "completed", Conclusion: "failure"})
		},
		"job conclusion": func(c *Content) { c.Runs[0].Jobs[0].Conclusion = "cancelled" },
		"job status":     func(c *Content) { c.Runs[0].Jobs[0].Status = "in_progress" },
		"job name":       func(c *Content) { c.Runs[0].Jobs[0].Name = "other" },
		"run id":         func(c *Content) { c.Runs[0].ID = "2" },
		"attempt":        func(c *Content) { c.Runs[0].Attempt = 2 },
		"name":           func(c *Content) { c.Runs[0].Name = "other" },
		"head":           func(c *Content) { c.Runs[0].HeadSHA = "other" },
		"status":         func(c *Content) { c.Runs[0].Status = "queued" },
		"conclusion":     func(c *Content) { c.Runs[0].Conclusion = "cancelled" },
		"new run":        func(c *Content) { c.Runs = append(c.Runs, schema.CIRun{ID: "3"}) },
	}
	for name, mutate := range moves {
		c := base()
		mutate(&c)
		if Fingerprint(c) == fp {
			t.Errorf("%s did not move the fingerprint", name)
		}
	}
	// Job order is not a change.
	a := base()
	a.Runs[0].Jobs = append(a.Runs[0].Jobs, schema.CIJob{Name: "lint", Status: "completed", Conclusion: "success"})
	b := base()
	b.Runs[0].Jobs = []schema.CIJob{{Name: "lint", Status: "completed", Conclusion: "success"}, b.Runs[0].Jobs[0]}
	if Fingerprint(a) != Fingerprint(b) {
		t.Error("job order moved the fingerprint")
	}
}

func TestFetch_TimestampOnlyDifferenceIsNoChange(t *testing.T) {
	withTime := func(ts string) string {
		return runsBody(strings.Replace(run(1, "ci", head, "completed", "success", 1), "}", `,"updated_at":"`+ts+`","created_at":"`+ts+`"}`, 1))
	}
	cur := withTime("2026-10-12T10:00:00Z")
	gh := &fakeGH{runs: func(int) ([]byte, error) { return ok(cur), nil }}
	e := newEnv(t, gh)
	o1 := e.fetch(t, target(t, "main"))
	cur = withTime("2026-10-12T11:00:00Z")
	o2 := e.fetch(t, target(t, "main"))
	if o1.Fingerprint != o2.Fingerprint {
		t.Error("a timestamp-only difference moved the fingerprint")
	}
}

func TestFetch_EscapesTheBranchAndRetriesTransientFailures(t *testing.T) {
	const branch = "feat/a&b#1+x%"
	var seen []string
	gh := &fakeGH{runs: func(n int) ([]byte, error) {
		if n == 1 {
			return reply(502, "Bad Gateway", `{"message":"bad gateway"}`), errors.New("gh: HTTP 502")
		}
		return ok(runsBody(run(1, "ci", head, "completed", "success", 1))), nil
	}}
	e := newEnv(t, gh)
	o := e.fetch(t, target(t, branch))
	if o.Err != nil || len(o.Content) == 0 {
		t.Fatalf("err %v content %q", o.Err, o.Content)
	}
	for _, ep := range gh.runsCalls {
		_, q, _ := strings.Cut(ep, "?")
		vals, err := url.ParseQuery(q)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, vals.Get("branch"))
		if vals.Get("per_page") != "100" || len(vals) != 2 {
			t.Errorf("query %q parsed to %v, want exactly branch and per_page", q, vals)
		}
	}
	if len(seen) != 2 || seen[0] != branch || seen[1] != branch {
		t.Errorf("decoded branch values = %q, want %q twice", seen, branch)
	}
	if got := restSpent(t, e.gov); got != 2 || o.Cost != 2 {
		t.Errorf("admissions %d cost %d, want 2 and 2 (each attempt admitted again)", got, o.Cost)
	}
	if len(e.sl.delays) != 1 || e.sl.delays[0] != 500*time.Millisecond {
		t.Errorf("backoff = %v, want [500ms]", e.sl.delays)
	}
}

func TestFetch_ThreeTransientFailuresReturnTheError(t *testing.T) {
	gh := &fakeGH{runs: func(int) ([]byte, error) {
		return reply(503, "Service Unavailable", `{}`), errors.New("gh: HTTP 503")
	}}
	e := newEnv(t, gh)
	o := e.fetch(t, target(t, "main"))
	if o.Err == nil || !strings.Contains(o.Err.Error(), "503") || errors.Is(o.Err, groups.ErrGone) {
		t.Errorf("err = %v", o.Err)
	}
	if len(gh.runsCalls) != 3 || o.Cost != 3 {
		t.Errorf("attempts %d cost %d, want 3 and 3", len(gh.runsCalls), o.Cost)
	}
	if len(e.sl.delays) != 2 || e.sl.delays[0] != 500*time.Millisecond || e.sl.delays[1] != time.Second {
		t.Errorf("backoff = %v, want [500ms 1s]", e.sl.delays)
	}
	if len(o.Content) != 0 {
		t.Error("a failed fetch carried content")
	}
}

func TestFetch_ConnectionErrorsRetryToo(t *testing.T) {
	gh := &fakeGH{runs: func(n int) ([]byte, error) {
		if n < 3 {
			return nil, errors.New("error connecting to api.github.com")
		}
		return ok(runsBody()), nil
	}}
	e := newEnv(t, gh)
	o := e.fetch(t, target(t, "main"))
	if o.Err != nil || len(gh.runsCalls) != 3 {
		t.Errorf("err %v attempts %d, want success on the third", o.Err, len(gh.runsCalls))
	}
	if c, _ := Decode(o.Content); c.Runs == nil || len(c.Runs) != 0 || !strings.Contains(string(o.Content), `"runs":[]`) {
		t.Errorf("empty branch content = %s", o.Content)
	}
}

func TestFetch_Errors(t *testing.T) {
	t.Run("404 is gone", func(t *testing.T) {
		gh := &fakeGH{runs: func(int) ([]byte, error) {
			return reply(404, "Not Found", `{"message":"Not Found"}`), errors.New("gh: HTTP 404")
		}}
		e := newEnv(t, gh)
		if o := e.fetch(t, target(t, "main")); !errors.Is(o.Err, groups.ErrGone) {
			t.Errorf("err = %v, want ErrGone", o.Err)
		}
		if len(gh.runsCalls) != 1 {
			t.Errorf("a 404 was retried: %d requests", len(gh.runsCalls))
		}
	})
	t.Run("rate limit unchanged", func(t *testing.T) {
		gh := &fakeGH{runs: func(int) ([]byte, error) {
			return []byte("HTTP/2.0 403 Forbidden\r\nRetry-After: 60\r\nX-RateLimit-Resource: core\r\n\r\n{}"), errors.New("gh: HTTP 403")
		}}
		e := newEnv(t, gh)
		o := e.fetch(t, target(t, "main"))
		var rl *ratefeed.RateLimitError
		if !errors.As(o.Err, &rl) || !rl.Secondary || rl.RetryAfter != time.Minute {
			t.Errorf("err = %#v, want the secondary *RateLimitError", o.Err)
		}
		if len(gh.runsCalls) != 1 {
			t.Errorf("a rate-limit answer was retried: %d requests", len(gh.runsCalls))
		}
	})
	t.Run("refusal unchanged", func(t *testing.T) {
		gh := &fakeGH{runs: func(int) ([]byte, error) { return ok(runsBody()), nil }}
		e := newEnv(t, gh)
		e.gov.PauseAll(time.Hour, "test")
		o := e.fetch(t, target(t, "main"))
		var ref *governor.Refusal
		if !errors.As(o.Err, &ref) || len(gh.runsCalls) != 0 || o.Cost != 0 {
			t.Errorf("err = %#v calls %d cost %d, want a *Refusal, no call, no cost", o.Err, len(gh.runsCalls), o.Cost)
		}
	})
	t.Run("empty branch", func(t *testing.T) {
		gh := &fakeGH{runs: func(int) ([]byte, error) { return ok(runsBody()), nil }}
		e := newEnv(t, gh)
		if o := e.fetch(t, target(t, "")); o.Err == nil || len(gh.runsCalls) != 0 {
			t.Errorf("err %v calls %d, want an error and no call", o.Err, len(gh.runsCalls))
		}
	})
	t.Run("invalid repository", func(t *testing.T) {
		gh := &fakeGH{runs: func(int) ([]byte, error) { return ok(runsBody()), nil }}
		e := newEnv(t, gh)
		tg := target(t, "main")
		tg.Ref = entity.Ref{Owner: "a/../b", Repo: "r", Number: 1}
		if o := e.fetch(t, tg); o.Err == nil || len(gh.runsCalls) != 0 {
			t.Errorf("err %v calls %d, want an error and no call", o.Err, len(gh.runsCalls))
		}
		tg.Ref = entity.Ref{}
		if o := e.fetch(t, tg); o.Err == nil || len(gh.runsCalls) != 0 {
			t.Errorf("zero ref: err %v calls %d", o.Err, len(gh.runsCalls))
		}
	})
	t.Run("unusable answer", func(t *testing.T) {
		gh := &fakeGH{runs: func(int) ([]byte, error) { return ok("not json"), nil }}
		e := newEnv(t, gh)
		if o := e.fetch(t, target(t, "main")); o.Err == nil || len(gh.runsCalls) != 1 {
			t.Errorf("err %v calls %d", o.Err, len(gh.runsCalls))
		}
	})
}

func TestFetch_RateLimitDuringJobsStopsFurtherJobFetchesButKeepsTheRuns(t *testing.T) {
	gh := &fakeGH{
		runs: func(int) ([]byte, error) {
			return ok(runsBody(run(1, "a", head, "completed", "failure", 1), run(2, "b", head, "completed", "failure", 1))), nil
		},
		jobs: func(string, int, int) ([]byte, error) {
			return []byte("HTTP/2.0 429 Too Many Requests\r\nRetry-After: 30\r\n\r\n{}"), errors.New("gh: HTTP 429")
		},
	}
	e := newEnv(t, gh)
	o := e.fetch(t, target(t, "main"))
	if o.Err != nil || len(o.Content) == 0 || o.LastError == "" || len(gh.jobCalls) != 1 {
		t.Errorf("err %v content %d bytes LastError %q job calls %d, want content kept, error noted, one job call", o.Err, len(o.Content), o.LastError, len(gh.jobCalls))
	}
}

func TestRerunnable(t *testing.T) {
	for c, want := range map[string]bool{
		"failure": true, "timed_out": true, "startup_failure": true, "cancelled": true, "FAILURE": true,
		"success": false, "skipped": false, "neutral": false, "stale": false, "action_required": false, "": false,
	} {
		if Rerunnable(c) != want {
			t.Errorf("Rerunnable(%q) = %v, want %v", c, !want, want)
		}
	}
}

func TestPruneJobs(t *testing.T) {
	e := newEnv(t, &fakeGH{})
	ctx := context.Background()
	now := t0.Add(100 * time.Hour)
	expiry := 72 * time.Hour
	for i, age := range []time.Duration{73 * time.Hour, 72 * time.Hour, time.Hour} {
		err := e.st.PutJobs(ctx, store.JobRow{RunID: fmt.Sprint(i), Attempt: 1, FetchedAt: now.Add(-age)})
		if err != nil {
			t.Fatal(err)
		}
	}
	n, err := PruneJobs(ctx, e.st, now, expiry)
	if err != nil || n != 1 {
		t.Fatalf("PruneJobs = %d, %v, want 1", n, err)
	}
	for id, want := range map[string]bool{"0": false, "1": true, "2": true} {
		if _, found, _ := e.st.GetJobs(ctx, id, 1); found != want {
			t.Errorf("row %s present = %v, want %v", id, found, want)
		}
	}
}

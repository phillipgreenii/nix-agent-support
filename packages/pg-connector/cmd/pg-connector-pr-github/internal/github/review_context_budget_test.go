package github

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// longHistoryFixture is a recording-shaped set of the GraphQL response pages
// of a PR with a long comment history (testdata/long-comment-history.json):
// 100 review threads in 10 pages, 50 issue comments in 5 pages, 1 review. Page
// N of a connection is the response to the request whose `after` cursor is
// "<prefix>N" (the first page has none).
type longHistoryFixture struct {
	Threads []json.RawMessage `json:"threads"`
	Issues  []json.RawMessage `json:"issues"`
	Reviews []json.RawMessage `json:"reviews"`
}

func loadLongHistory(t *testing.T) longHistoryFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/long-comment-history.json")
	if err != nil {
		t.Fatal(err)
	}
	var f longHistoryFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// budgetRunner replays the recorded pages through a fake clock: every request
// costs its connection's latency, and a request issued once the clock has
// passed the context deadline fails the way a gh killed by the call deadline
// does. That is how the real call died: the heavy thread pages used the whole
// deadline and the issue-comment read after them was killed.
type budgetRunner struct {
	fx       longHistoryFixture
	clock    time.Time
	deadline time.Time
	latency  map[string]time.Duration
	calls    map[string]int
}

func (r *budgetRunner) now() time.Time { return r.clock }

func (r *budgetRunner) RunStdin(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	return r.Run(ctx, args...)
}

func (r *budgetRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	query := argValue(args, "query")
	after := argValue(args, "after")
	var kind string
	var pages []json.RawMessage
	var prefix string
	switch {
	case strings.Contains(query, "reviewThreads(first: 100"):
		kind, pages, prefix = "threads", r.fx.Threads, "T"
	case strings.Contains(query, "reviews(first: 100"):
		kind, pages, prefix = "reviews", r.fx.Reviews, "R"
	case strings.Contains(query, "comments(first: 100, after: $after)"):
		kind, pages, prefix = "issues", r.fx.Issues, "I"
	default:
		return nil, errors.New("budgetRunner: unrecognised query")
	}
	r.calls[kind]++
	if !r.clock.Before(r.deadline) {
		return nil, errors.New("gh api graphql: signal: killed (deadline exceeded) in " + kind)
	}
	r.clock = r.clock.Add(r.latency[kind])
	idx := 0
	if after != "" {
		n, ok := strings.CutPrefix(after, prefix)
		if !ok {
			return nil, errors.New("budgetRunner: cursor " + after + " is not a " + kind + " cursor")
		}
		for _, c := range n {
			idx = idx*10 + int(c-'0')
		}
	}
	return pages[idx], nil
}

func newBudgetRunner(t *testing.T) (*budgetRunner, context.Context) {
	t.Helper()
	base := time.Date(2026, 10, 19, 18, 20, 0, 0, time.UTC)
	r := &budgetRunner{
		fx:       loadLongHistory(t),
		clock:    base,
		deadline: base.Add(30 * time.Second),
		// The heavy thread pages (100 threads, each with 100 comments) cost
		// 3s; the issue-comment and review pages are light.
		latency: map[string]time.Duration{"threads": 3 * time.Second, "issues": 500 * time.Millisecond, "reviews": 500 * time.Millisecond},
		calls:   map[string]int{},
	}
	// The context's deadline is the fake clock's: the real clock is never read.
	ctx, cancel := context.WithDeadline(context.Background(), r.deadline)
	t.Cleanup(cancel)
	return r, ctx
}

// With a call deadline that the thread pages alone would exhaust (10 pages x
// 3s = 30s against 30s), ListCommentsReport still reads the issue comments
// COMPLETE and reports the threads as truncated, instead of failing "list
// issue comments" once the deadline is gone (bead pg2-vq8ii).
func TestListCommentsReport_ThreadReadDoesNotStarveIssueComments(t *testing.T) {
	r, ctx := newBudgetRunner(t)
	p := NewWithRunner(r)
	p.now = r.now

	res, err := p.ListCommentsReport(ctx, "foo/bar", 1)
	if err != nil {
		t.Fatalf("ListCommentsReport = %v, want success within the call deadline", err)
	}
	if !res.Threads.Truncated || res.Threads.Total != 100 || res.Threads.Returned >= 100 || res.Threads.Returned == 0 {
		t.Errorf("threads report = %+v, want truncated with some but not all of the 100", res.Threads)
	}
	// The thread share is half of 30s: the 5th page ends at 15s, which is
	// where the share is spent.
	if r.calls["threads"] != 5 || res.Threads.Returned != 50 {
		t.Errorf("thread pages = %d (returned %d), want 5 (50)", r.calls["threads"], res.Threads.Returned)
	}
	if r.calls["issues"] != 5 {
		t.Errorf("issue-comment pages = %d, want all 5", r.calls["issues"])
	}
	issues := 0
	for _, c := range res.Comments {
		if c.ThreadID == "" {
			issues++
		}
	}
	if issues != 50 {
		t.Errorf("issue comments returned = %d, want all 50", issues)
	}
	// CommentsReport counts only the fetched threads (api.CommentsResult), so
	// the partial read is the Threads report above, not this one.
	if res.CommentsReport.Returned != 50+50 {
		t.Errorf("comments returned = %d, want 50 thread comments + 50 issue comments", res.CommentsReport.Returned)
	}

	// The reviews that follow still fit.
	rv, err := p.ListReviewsReport(ctx, "foo/bar", 1)
	if err != nil || len(rv.Reviews) != 1 || rv.Report.Truncated {
		t.Errorf("ListReviewsReport = %+v, %v; want the one review, complete", rv, err)
	}
}

// Control: the same recording with no time bound (no context deadline) reads
// every page, so the bound is what changes the outcome and a PR that fits its
// budget is never cut.
func TestListCommentsReport_NoDeadlineReadsEveryPage(t *testing.T) {
	r, _ := newBudgetRunner(t)
	p := NewWithRunner(r)
	r.deadline = r.clock.Add(24 * time.Hour)

	res, err := p.ListCommentsReport(context.Background(), "foo/bar", 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Threads.Truncated || res.Threads.Returned != 100 || r.calls["threads"] != 10 || r.calls["issues"] != 5 {
		t.Errorf("threads = %+v, pages threads=%d issues=%d; want everything", res.Threads, r.calls["threads"], r.calls["issues"])
	}
	if res.CommentsReport.Truncated || res.CommentsReport.Returned != 150 {
		t.Errorf("comments report = %+v, want 150 complete", res.CommentsReport)
	}
}

// The failure this bead fixes, reproduced: with the thread read unbounded the
// same recording dies at the issue comments with the "list issue comments"
// error of the run record. It pins that the recording really exceeds the call
// budget, so the passing test above is not vacuous.
func TestListCommentsReport_UnboundedThreadReadStarvesIssueComments(t *testing.T) {
	r, _ := newBudgetRunner(t)
	// Replay the pre-fix flow: unbounded threads, then the issue comments.
	p := NewWithRunner(r)
	if _, _, err := p.threadsConnection(context.Background(), "foo/bar", 1, pageBound{}); err != nil {
		t.Fatalf("threads (30s of pages: the clock reaches the deadline only at the end): %v", err)
	}
	if r.clock.Before(r.deadline) {
		t.Fatalf("clock %v is before the deadline %v: the recording does not exhaust the budget", r.clock, r.deadline)
	}
	_, _, err := p.issueCommentsConnection(context.Background(), "foo/bar", 1, maxComments, pageBound{})
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("issue comments after an unbounded thread read: err = %v, want the deadline failure", err)
	}
}

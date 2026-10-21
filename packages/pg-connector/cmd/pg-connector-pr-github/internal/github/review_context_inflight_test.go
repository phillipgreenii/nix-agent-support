package github

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// stallRunner replays the recorded long-comment-history pages
// (testdata/long-comment-history.json) in real time. A page for which stall
// returns true never answers: like a `gh api graphql` on a loaded host it
// blocks until its context is done and then fails, which is how the 12 desk-pr
// gather failures after pg2-vq8ii died ("list issue comments: ... deadline
// exceeded"): the page was IN FLIGHT when the call's deadline arrived, so the
// between-pages share check of pg2-vq8ii never got to run (bead pg2-2hwim).
type stallRunner struct {
	fx    longHistoryFixture
	stall func(kind string, idx int) bool
	calls map[string]int
}

func (r *stallRunner) RunStdin(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	return r.Run(ctx, args...)
}

func (r *stallRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	query := argValue(args, "query")
	after := argValue(args, "after")
	var kind, prefix string
	var pages []json.RawMessage
	switch {
	case strings.Contains(query, "reviewThreads(first: 100"):
		kind, pages, prefix = "threads", r.fx.Threads, "T"
	case strings.Contains(query, "reviews(first: 100"):
		kind, pages, prefix = "reviews", r.fx.Reviews, "R"
	case strings.Contains(query, "comments(first: 100, after: $after)"):
		kind, pages, prefix = "issues", r.fx.Issues, "I"
	default:
		return nil, errors.New("stallRunner: unrecognised query")
	}
	idx := 0
	if after != "" {
		n, ok := strings.CutPrefix(after, prefix)
		if !ok {
			return nil, errors.New("stallRunner: cursor " + after + " is not a " + kind + " cursor")
		}
		for _, c := range n {
			idx = idx*10 + int(c-'0')
		}
	}
	r.calls[kind]++
	if r.stall(kind, idx) {
		<-ctx.Done()
		return nil, errors.New("gh api graphql: signal: killed: " + ctx.Err().Error())
	}
	return pages[idx], nil
}

func newStallRunner(t *testing.T, stall func(kind string, idx int) bool) *stallRunner {
	t.Helper()
	return &stallRunner{fx: loadLongHistory(t), stall: stall, calls: map[string]int{}}
}

// A slow issue-comment page that is in flight when the issue comments' share
// ends is cancelled there and the comments read so far come back as a
// truncated connection: the call succeeds well inside its deadline, with the
// review threads complete and the reviews still read afterwards. The second
// page of the 5 never answers.
func TestListCommentsReport_SlowIssueCommentPageIsCutAtItsShare(t *testing.T) {
	const budget = 2 * time.Second
	r := newStallRunner(t, func(kind string, idx int) bool { return kind == "issues" && idx >= 1 })
	p := NewWithRunner(r)
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	start := time.Now()
	res, err := p.ListCommentsReport(ctx, "foo/bar", 1)
	took := time.Since(start)
	if err != nil {
		t.Fatalf("ListCommentsReport = %v, want a partial result inside the call deadline", err)
	}
	// The issue comments' share is 80% of the budget; the call is done there,
	// leaving the rest for the reviews, not at the call deadline.
	if took >= budget*9/10 {
		t.Errorf("took %v, want the stalled page cut at its share (80%% of %v)", took, budget)
	}
	if res.Threads.Truncated || res.Threads.Returned != 100 {
		t.Errorf("threads report = %+v, want all 100", res.Threads)
	}
	issues := 0
	for _, c := range res.Comments {
		if c.ThreadID == "" {
			issues++
		}
	}
	if issues != 10 {
		t.Errorf("issue comments returned = %d, want the 10 of the page that answered", issues)
	}
	// 100 threads' comments + 10 issue comments, of a connection that held 50.
	if !res.CommentsReport.Truncated || res.CommentsReport.Total <= res.CommentsReport.Returned {
		t.Errorf("comments report = %+v, want truncated with Total > Returned", res.CommentsReport)
	}

	// The time that is left is enough for the reviews.
	rv, err := p.ListReviewsReport(ctx, "foo/bar", 1)
	if err != nil || len(rv.Reviews) != 1 || rv.Report.Truncated {
		t.Errorf("ListReviewsReport = %+v, %v; want the one review, complete", rv, err)
	}
}

// The same cut applies before any page answered: the first issue-comment page
// never arrives, and the call still succeeds with no issue comments, reported
// as truncated (the connection total is unknown then, so Total is the lower
// bound Returned+1).
func TestListCommentsReport_StalledFirstIssueCommentPageIsReportedTruncated(t *testing.T) {
	r := newStallRunner(t, func(kind string, _ int) bool { return kind == "issues" })
	p := NewWithRunner(r)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := p.ListCommentsReport(ctx, "foo/bar", 1)
	if err != nil {
		t.Fatalf("ListCommentsReport = %v, want a result with the threads and no issue comments", err)
	}
	for _, c := range res.Comments {
		if c.ThreadID == "" {
			t.Fatalf("got issue comment %q from a page that never answered", c.ID)
		}
	}
	if !res.CommentsReport.Truncated || res.CommentsReport.Total <= res.CommentsReport.Returned {
		t.Errorf("comments report = %+v, want truncated with Total > Returned", res.CommentsReport)
	}
}

// A thread page in flight when the threads' share ends is cut the same way:
// the threads read so far are returned as a truncated connection and the issue
// comments, whose time was not eaten, are read complete.
func TestListCommentsReport_SlowThreadPageIsCutAtItsShare(t *testing.T) {
	r := newStallRunner(t, func(kind string, idx int) bool { return kind == "threads" && idx >= 2 })
	p := NewWithRunner(r)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := p.ListCommentsReport(ctx, "foo/bar", 1)
	if err != nil {
		t.Fatalf("ListCommentsReport = %v, want a partial result inside the call deadline", err)
	}
	if !res.Threads.Truncated || res.Threads.Returned != 20 || res.Threads.Total != 100 {
		t.Errorf("threads report = %+v, want truncated, 20 of 100", res.Threads)
	}
	if r.calls["issues"] != 5 {
		t.Errorf("issue-comment pages = %d, want all 5 read after the cut", r.calls["issues"])
	}
}

// A failure that is not the share running out still fails the call: the
// partial-result path is only for the connection's own deadline.
func TestListCommentsReport_OtherPageErrorStillFails(t *testing.T) {
	p := NewWithRunner(&failingRunner{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := p.ListCommentsReport(ctx, "foo/bar", 1); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the runner's failure", err)
	}
}

type failingRunner struct{}

func (failingRunner) Run(context.Context, ...string) ([]byte, error) {
	return nil, errors.New("boom: HTTP 404")
}

func (failingRunner) RunStdin(context.Context, []byte, ...string) ([]byte, error) {
	return nil, errors.New("boom: HTTP 404")
}

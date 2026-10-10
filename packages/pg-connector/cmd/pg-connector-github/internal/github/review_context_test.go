package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// connFake answers the review-context GraphQL documents (reviews, review
// threads, a thread's later comment pages, issue comments) from in-memory
// lists, paging them 100 at a time by an offset cursor exactly as the real
// connections do. It is the fake gh execution seam for these tests.
type connFake struct {
	reviews  []map[string]any
	threads  []threadFixture
	issues   []map[string]any
	nullPR   bool
	gqlError string

	// calls counts the queries answered, by kind.
	calls map[string]int
}

type threadFixture struct {
	id         string
	resolved   bool
	outdated   bool
	comments   []map[string]any
	totalExtra int // added to the thread's comments totalCount (unfetchable remainder)
}

func newConnFake() *connFake { return &connFake{calls: map[string]int{}} }

func (f *connFake) RunStdin(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	return f.Run(ctx, args...)
}

func argValue(args []string, key string) string {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, key+"="); ok {
			return v
		}
	}
	return ""
}

// page renders one connection page of items starting at the cursor offset.
func page(items []map[string]any, after string, extraTotal int) map[string]any {
	off := 0
	if after != "" {
		off, _ = strconv.Atoi(after)
	}
	end := min(off+100, len(items))
	nodes := items[off:end]
	if nodes == nil {
		nodes = []map[string]any{}
	}
	next := ""
	if end < len(items) {
		next = strconv.Itoa(end)
	}
	return map[string]any{
		"totalCount": len(items) + extraTotal,
		"pageInfo":   map[string]any{"hasNextPage": end < len(items), "endCursor": next},
		"nodes":      nodes,
	}
}

func (f *connFake) Run(_ context.Context, args ...string) ([]byte, error) {
	query := argValue(args, "query")
	after := argValue(args, "after")
	if f.gqlError != "" {
		return []byte(fmt.Sprintf(`{"errors":[{"message":%q}]}`, f.gqlError)), nil
	}
	var data any
	switch {
	case strings.Contains(query, "node(id: $threadId)"):
		f.calls["threadComments"]++
		id := argValue(args, "threadId")
		for _, t := range f.threads {
			if t.id == id {
				data = map[string]any{"node": map[string]any{"comments": page(t.comments, after, t.totalExtra)}}
			}
		}
	case strings.Contains(query, "reviewThreads(first: 100"):
		f.calls["threads"]++
		items := make([]map[string]any, 0, len(f.threads))
		for _, t := range f.threads {
			// The first page of a thread's comments rides on the thread itself.
			items = append(items, map[string]any{
				"id": t.id, "isResolved": t.resolved, "isOutdated": t.outdated,
				"comments": page(t.comments, "", t.totalExtra),
			})
		}
		data = f.prData(map[string]any{"reviewThreads": page(items, after, 0)})
	case strings.Contains(query, "reviews(first: 100"):
		f.calls["reviews"]++
		data = f.prData(map[string]any{"reviews": page(f.reviews, after, 0)})
	case strings.Contains(query, "comments(first: 100, after: $after)"):
		f.calls["issueComments"]++
		data = f.prData(map[string]any{"comments": page(f.issues, after, 0)})
	default:
		return nil, fmt.Errorf("connFake: unrecognised query %q", query)
	}
	return json.Marshal(map[string]any{"data": data})
}

func (f *connFake) prData(pr map[string]any) any {
	if f.nullPR {
		return map[string]any{"repository": map[string]any{"pullRequest": nil}}
	}
	return map[string]any{"repository": map[string]any{"pullRequest": pr}}
}

// ts is the n-th minute of a fixed day, RFC3339.
func ts(n int) string {
	return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Minute).Format(time.RFC3339)
}

func review(i int, state, login string) map[string]any {
	return map[string]any{
		"id": fmt.Sprintf("PRR_%d", i), "state": state, "body": fmt.Sprintf("review %d", i),
		"createdAt": ts(i), "submittedAt": ts(i), "author": map[string]any{"login": login},
		"commit": map[string]any{"oid": "abc"},
	}
}

func threadComment(id, login, reviewID string, minute int) map[string]any {
	return map[string]any{
		"id": id, "author": map[string]any{"login": login}, "authorAssociation": "MEMBER",
		"body": "body " + id, "path": "main.go", "line": 10, "originalLine": 10,
		"createdAt": ts(minute), "updatedAt": ts(minute),
		"pullRequestReview": map[string]any{"id": reviewID},
	}
}

func issueComment(i int, login string) map[string]any {
	return map[string]any{
		"id": fmt.Sprintf("IC_%d", i), "author": map[string]any{"login": login},
		"authorAssociation": "NONE", "body": fmt.Sprintf("issue %d", i),
		"createdAt": ts(i), "updatedAt": ts(i),
	}
}

func manyReviews(n int) []map[string]any {
	out := make([]map[string]any, n)
	for i := range out {
		out[i] = review(i, "COMMENTED", "rev")
	}
	return out
}

func manyThreads(n int) []threadFixture {
	out := make([]threadFixture, n)
	for i := range out {
		out[i] = threadFixture{
			id:       fmt.Sprintf("PRRT_%d", i),
			comments: []map[string]any{threadComment(fmt.Sprintf("RC_%d", i), "rev", "PRR_0", i)},
		}
	}
	return out
}

func manyIssues(n int) []map[string]any {
	out := make([]map[string]any, n)
	for i := range out {
		out[i] = issueComment(i, "someone")
	}
	return out
}

// The thread's real id differs from the root comment's own id, and the
// existing ThreadID keeps carrying the comment's own id.
func TestListCommentsReport_ReviewThreadIDDiffersFromRootCommentID(t *testing.T) {
	f := newConnFake()
	f.threads = []threadFixture{{
		id: "PRRT_real", resolved: true, outdated: true,
		comments: []map[string]any{
			threadComment("RC_root", "alice", "PRR_1", 1),
			threadComment("RC_reply", "bob", "PRR_2", 2),
		},
	}}
	r, err := NewWithRunner(f).ListCommentsReport(context.Background(), "foo/bar", 42)
	if err != nil {
		t.Fatalf("ListCommentsReport: %v", err)
	}
	if len(r.Comments) != 2 {
		t.Fatalf("got %d comments, want 2: %+v", len(r.Comments), r.Comments)
	}
	for _, c := range r.Comments {
		if c.ReviewThreadID != "PRRT_real" {
			t.Errorf("%s: ReviewThreadID = %q, want PRRT_real", c.ID, c.ReviewThreadID)
		}
		if c.ThreadID != c.ID {
			t.Errorf("%s: ThreadID = %q, want the comment's own node id %q (unchanged meaning)", c.ID, c.ThreadID, c.ID)
		}
		if c.ThreadID == c.ReviewThreadID {
			t.Errorf("%s: ThreadID and ReviewThreadID must differ", c.ID)
		}
		if !c.Resolved || !c.ThreadIsOutdated {
			t.Errorf("%s: thread flags not carried: resolved=%v outdated=%v", c.ID, c.Resolved, c.ThreadIsOutdated)
		}
	}
	byID := map[string]string{}
	for _, c := range r.Comments {
		byID[c.ID] = c.ReviewID
	}
	if byID["RC_root"] != "PRR_1" || byID["RC_reply"] != "PRR_2" {
		t.Errorf("ReviewID not taken from the comment's owning review: %v", byID)
	}
}

// An issue comment, a bot comment and a pending-review comment are all
// present, and the viewer's pending review is listed with its state.
func TestListCommentsReport_IssueBotAndPendingCommentsPresent(t *testing.T) {
	f := newConnFake()
	f.issues = []map[string]any{issueComment(1, "alice"), issueComment(2, "ci-bot[bot]")}
	f.threads = []threadFixture{{
		id:       "PRRT_pending",
		comments: []map[string]any{threadComment("RC_pending", "me", "PRR_pending", 3)},
	}}
	f.reviews = []map[string]any{
		review(1, "APPROVED", "alice"),
		{
			"id": "PRR_pending", "state": "PENDING", "body": "draft", "createdAt": ts(3), "submittedAt": nil,
			"author": map[string]any{"login": "me"},
		},
	}
	p := NewWithRunner(f)

	cr, err := p.ListCommentsReport(context.Background(), "foo/bar", 42)
	if err != nil {
		t.Fatalf("ListCommentsReport: %v", err)
	}
	got := map[string]string{}
	for _, c := range cr.Comments {
		got[c.ID] = c.Author
	}
	for id, author := range map[string]string{"IC_1": "alice", "IC_2": "ci-bot[bot]", "RC_pending": "me"} {
		if got[id] != author {
			t.Errorf("comment %s: author %q, want %q (all comments: %v)", id, got[id], author, got)
		}
	}
	for _, c := range cr.Comments {
		if strings.HasPrefix(c.ID, "IC_") && (c.ThreadID != "" || c.ReviewThreadID != "" || c.Path != "") {
			t.Errorf("issue comment %s must carry no thread or path: %+v", c.ID, c)
		}
	}

	rr, err := p.ListReviewsReport(context.Background(), "foo/bar", 42)
	if err != nil {
		t.Fatalf("ListReviewsReport: %v", err)
	}
	states := map[string]string{}
	for _, r := range rr.Reviews {
		states[r.ID] = r.State + "/" + r.Body
	}
	if states["PRR_pending"] != "PENDING/draft" || states["PRR_1"] != "APPROVED/review 1" {
		t.Errorf("reviews with body and state: %v", states)
	}
}

// Cap boundary: exactly the cap, one under and one over. The flag is set only
// when over; total and returned are right in every case.
func TestListReportsCapBoundary(t *testing.T) {
	cases := []struct {
		name    string
		n       int
		wantRet int
		wantTrn bool
	}{
		{"one under", 999, 999, false},
		{"exactly the cap", 1000, 1000, false},
		{"one over", 1001, 1000, true},
	}
	for _, tc := range cases {
		t.Run("reviews/"+tc.name, func(t *testing.T) {
			f := newConnFake()
			f.reviews = manyReviews(tc.n)
			r, err := NewWithRunner(f).ListReviewsReport(context.Background(), "foo/bar", 1)
			if err != nil {
				t.Fatal(err)
			}
			assertReport(t, r.Report.Truncated, r.Report.Total, r.Report.Returned, tc.wantTrn, tc.n, tc.wantRet)
			if len(r.Reviews) != tc.wantRet {
				t.Errorf("reviews = %d, want %d", len(r.Reviews), tc.wantRet)
			}
			// 1000 items need 10 pages; a cap-sized read must not fetch an 11th.
			if want := min(10, (tc.n+99)/100); f.calls["reviews"] != want {
				t.Errorf("review pages fetched = %d, want %d", f.calls["reviews"], want)
			}
		})
		t.Run("threads/"+tc.name, func(t *testing.T) {
			f := newConnFake()
			f.threads = manyThreads(tc.n)
			r, err := NewWithRunner(f).ListCommentsReport(context.Background(), "foo/bar", 1)
			if err != nil {
				t.Fatal(err)
			}
			assertReport(t, r.Threads.Truncated, r.Threads.Total, r.Threads.Returned, tc.wantTrn, tc.n, tc.wantRet)
			if want := min(10, (tc.n+99)/100); f.calls["threads"] != want {
				t.Errorf("thread pages fetched = %d, want %d", f.calls["threads"], want)
			}
		})
		t.Run("comments/"+tc.name, func(t *testing.T) {
			f := newConnFake()
			f.issues = manyIssues(tc.n)
			r, err := NewWithRunner(f).ListCommentsReport(context.Background(), "foo/bar", 1)
			if err != nil {
				t.Fatal(err)
			}
			assertReport(t, r.CommentsReport.Truncated, r.CommentsReport.Total, r.CommentsReport.Returned, tc.wantTrn, tc.n, tc.wantRet)
			if len(r.Comments) != tc.wantRet {
				t.Errorf("comments = %d, want %d", len(r.Comments), tc.wantRet)
			}
			if r.Threads.Truncated || r.Threads.Total != 0 {
				t.Errorf("threads report should be empty, got %+v", r.Threads)
			}
		})
	}
}

func assertReport(t *testing.T, gotTrn bool, gotTotal, gotRet int, wantTrn bool, wantTotal, wantRet int) {
	t.Helper()
	if gotTrn != wantTrn || gotTotal != wantTotal || gotRet != wantRet {
		t.Errorf("report = {truncated:%v total:%d returned:%d}, want {truncated:%v total:%d returned:%d}",
			gotTrn, gotTotal, gotRet, wantTrn, wantTotal, wantRet)
	}
}

// More than 100 reviews, threads or comments are paginated, and a thread with
// more than 100 comments is followed past its first page.
func TestListReports_Paginate(t *testing.T) {
	f := newConnFake()
	f.reviews = manyReviews(250)
	f.threads = manyThreads(250)
	f.threads[0].comments = nil
	for i := 0; i < 150; i++ {
		f.threads[0].comments = append(f.threads[0].comments, threadComment(fmt.Sprintf("RCbig_%d", i), "rev", "PRR_0", 1000+i))
	}
	f.issues = manyIssues(120)
	p := NewWithRunner(f)

	rr, err := p.ListReviewsReport(context.Background(), "foo/bar", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rr.Reviews) != 250 || rr.Report.Truncated || f.calls["reviews"] != 3 {
		t.Errorf("reviews: got %d (truncated %v) in %d pages, want 250 in 3", len(rr.Reviews), rr.Report.Truncated, f.calls["reviews"])
	}

	cr, err := p.ListCommentsReport(context.Background(), "foo/bar", 1)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls["threads"] != 3 {
		t.Errorf("thread pages = %d, want 3", f.calls["threads"])
	}
	if f.calls["threadComments"] != 1 {
		t.Errorf("thread-comment follow-up pages = %d, want 1 (the 150-comment thread)", f.calls["threadComments"])
	}
	if f.calls["issueComments"] != 2 {
		t.Errorf("issue-comment pages = %d, want 2", f.calls["issueComments"])
	}
	wantComments := 150 + 249 + 120
	if len(cr.Comments) != wantComments || cr.CommentsReport.Truncated || cr.CommentsReport.Total != wantComments {
		t.Errorf("comments = %d, report %+v, want %d complete", len(cr.Comments), cr.CommentsReport, wantComments)
	}
	if cr.Threads.Total != 250 || cr.Threads.Returned != 250 || cr.Threads.Truncated {
		t.Errorf("threads report = %+v", cr.Threads)
	}
}

// Issue comments and thread comments share one comment budget: a thread that
// alone exceeds it truncates the comment set and the report says so.
func TestListCommentsReport_SharedCommentBudget(t *testing.T) {
	f := newConnFake()
	f.threads = []threadFixture{{id: "PRRT_big"}}
	for i := 0; i < 1050; i++ {
		f.threads[0].comments = append(f.threads[0].comments, threadComment(fmt.Sprintf("RC_%d", i), "rev", "PRR_0", i))
	}
	f.issues = manyIssues(30)
	r, err := NewWithRunner(f).ListCommentsReport(context.Background(), "foo/bar", 1)
	if err != nil {
		t.Fatal(err)
	}
	assertReport(t, r.CommentsReport.Truncated, r.CommentsReport.Total, r.CommentsReport.Returned, true, 1080, 1000)
	if len(r.Comments) != 1000 {
		t.Errorf("comments = %d, want 1000", len(r.Comments))
	}
	if r.Threads.Truncated {
		t.Errorf("the thread connection itself was not cut: %+v", r.Threads)
	}
}

// What was fetched is sorted newest-first on the client.
func TestListReports_SortNewestFirst(t *testing.T) {
	f := newConnFake()
	// Server order is oldest first; shuffle it to prove the client sorts.
	f.reviews = []map[string]any{
		review(5, "COMMENTED", "a"),
		review(9, "APPROVED", "b"),
		{"id": "PRR_pending", "state": "PENDING", "body": "", "createdAt": ts(7), "submittedAt": nil, "author": map[string]any{"login": "me"}},
		review(2, "COMMENTED", "c"),
	}
	f.threads = []threadFixture{
		{id: "PRRT_a", comments: []map[string]any{threadComment("RC_4", "a", "PRR_5", 4), threadComment("RC_8", "a", "PRR_5", 8)}},
		{id: "PRRT_b", comments: []map[string]any{threadComment("RC_6", "b", "PRR_9", 6)}},
	}
	f.issues = []map[string]any{issueComment(3, "x"), issueComment(10, "y")}
	p := NewWithRunner(f)

	rr, err := p.ListReviewsReport(context.Background(), "foo/bar", 1)
	if err != nil {
		t.Fatal(err)
	}
	var gotR []string
	for _, r := range rr.Reviews {
		gotR = append(gotR, r.ID)
	}
	if want := "PRR_9,PRR_pending,PRR_5,PRR_2"; strings.Join(gotR, ",") != want {
		t.Errorf("reviews order = %v, want %s (pending review dated by its creation)", gotR, want)
	}

	cr, err := p.ListCommentsReport(context.Background(), "foo/bar", 1)
	if err != nil {
		t.Fatal(err)
	}
	var gotC []string
	for _, c := range cr.Comments {
		gotC = append(gotC, c.ID)
	}
	if want := "IC_10,RC_8,RC_6,RC_4,IC_3"; strings.Join(gotC, ",") != want {
		t.Errorf("comments order = %v, want %s", gotC, want)
	}
}

func TestListReports_GraphQLErrorsAndMissingPR(t *testing.T) {
	f := newConnFake()
	f.gqlError = "something broke"
	p := NewWithRunner(f)
	if _, err := p.ListReviewsReport(context.Background(), "foo/bar", 1); err == nil || !strings.Contains(err.Error(), "something broke") {
		t.Errorf("reviews: err = %v, want the graphql message", err)
	}
	if _, err := p.ListCommentsReport(context.Background(), "foo/bar", 1); err == nil || !strings.Contains(err.Error(), "something broke") {
		t.Errorf("comments: err = %v, want the graphql message", err)
	}

	f = newConnFake()
	f.nullPR = true
	_, err := NewWithRunner(f).ListReviewsReport(context.Background(), "foo/bar", 9)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "could not resolve to a") {
		t.Errorf("null PR: err = %v, want an unresolved-PR error the backend classifies as not found", err)
	}
}

func TestListReports_ValidateInput(t *testing.T) {
	p := NewWithRunner(newConnFake())
	for _, repo := range []string{"", "no-slash"} {
		if _, err := p.ListComments(context.Background(), repo, 1); err == nil {
			t.Errorf("ListComments(%q): want error", repo)
		}
		if _, err := p.ListReviews(context.Background(), repo, 1); err == nil {
			t.Errorf("ListReviews(%q): want error", repo)
		}
	}
	if _, err := p.ListComments(context.Background(), "a/b", 0); err == nil {
		t.Error("ListComments: want error for PR number 0")
	}
	if _, err := p.ListReviews(context.Background(), "a/b", 0); err == nil {
		t.Error("ListReviews: want error for PR number 0")
	}
}

// The plain list methods return the same, newest-first, items as the report
// variants, and an empty PR is an empty (non-nil) comment set.
func TestListComments_And_ListReviews_WrapTheReports(t *testing.T) {
	f := newConnFake()
	f.reviews = []map[string]any{review(1, "APPROVED", "alice"), review(2, "COMMENTED", "claude[bot]")}
	p := NewWithRunner(f)
	rs, err := p.ListReviews(context.Background(), "foo/bar", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].ID != "PRR_2" || rs[0].Author != "claude[bot]" || rs[1].CommitOID != "abc" {
		t.Errorf("ListReviews = %+v", rs)
	}
	cs, err := p.ListComments(context.Background(), "foo/bar", 1)
	if err != nil {
		t.Fatal(err)
	}
	if cs == nil || len(cs) != 0 {
		t.Errorf("ListComments on an empty PR = %#v, want a non-nil empty slice", cs)
	}
}

// ReviewsWithCommit reads every review; a PR past the cap is an error rather
// than a quietly partial answer.
func TestReviewsWithCommit_PagesAndRefusesTruncation(t *testing.T) {
	f := newConnFake()
	f.reviews = manyReviews(150)
	got, err := NewWithRunner(f).ReviewsWithCommit(context.Background(), "foo/bar", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 150 || f.calls["reviews"] != 2 || got[0].CommitOID != "abc" {
		t.Errorf("got %d reviews in %d pages (first %+v)", len(got), f.calls["reviews"], got[0])
	}

	f = newConnFake()
	f.reviews = manyReviews(1001)
	if _, err := NewWithRunner(f).ReviewsWithCommit(context.Background(), "foo/bar", 1); err == nil {
		t.Error("want an error when the PR has more reviews than the cap")
	}
}

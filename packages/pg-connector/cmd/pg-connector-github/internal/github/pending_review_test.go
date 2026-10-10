package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// This file tests the review_pending read path against an in-memory fake of
// the GraphQL documents it issues: the main read, further pages of the
// pending and submitted review connections, further pages of one review's
// comments, and the review-thread id reads. Logins are the generic
// placeholders this module's identifier guard allows.

type lookupReview struct {
	id       string
	dbID     int64
	state    string
	author   string
	commit   string // "" renders a null commit
	body     string
	comments []map[string]any
}

type lookupThread struct {
	id         string
	commentIDs []string
}

type lookupFake struct {
	head, viewer string
	pending      []lookupReview
	submitted    []lookupReview
	threads      []lookupThread
	// raw, when set, answers every call verbatim.
	raw []byte
	// err, when set, fails every call.
	err error

	calls []string // kind of each call, in order
}

func (f *lookupFake) RunStdin(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	return f.Run(ctx, args...)
}

func commitNode(c string) any {
	if c == "" {
		return nil
	}
	return map[string]any{"oid": c}
}

// lcmt builds one comment node. line < 0 renders a null line (originalLine is
// then 8).
func lcmt(id string, dbID int64, line int, body, originalCommit string) map[string]any {
	n := map[string]any{"id": id, "databaseId": dbID, "path": "a.go", "body": body, "originalCommit": commitNode(originalCommit)}
	if line < 0 {
		n["line"], n["originalLine"] = nil, 8
	} else {
		n["line"], n["originalLine"] = line, line
	}
	return n
}

// manyComments builds n comments with ids prefix-0..prefix-(n-1), alternating
// originalCommit between h1 and h2.
func manyComments(prefix string, n int) []map[string]any {
	out := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		c := "h1"
		if i%2 == 1 {
			c = "h2"
		}
		out = append(out, lcmt(fmt.Sprintf("%s-%d", prefix, i), int64(i+1), i+1, "c", c))
	}
	return out
}

func (r lookupReview) node(full bool) map[string]any {
	n := map[string]any{
		"id": r.id, "databaseId": r.dbID, "state": r.state, "author": map[string]any{"login": r.author},
		"commit": commitNode(r.commit),
	}
	if full {
		n["url"] = "https://example.invalid/foo/bar/pull/42#pullrequestreview-" + fmt.Sprint(r.dbID)
		n["body"] = r.body
		n["comments"] = page(r.comments, "", 0)
	} else {
		n["comments"] = map[string]any{"totalCount": len(r.comments)}
	}
	return n
}

func reviewNodes(rs []lookupReview, full bool) []map[string]any {
	out := make([]map[string]any, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.node(full))
	}
	return out
}

func (t lookupThread) idNodes() []map[string]any {
	ids := make([]map[string]any, 0, len(t.commentIDs))
	for _, id := range t.commentIDs {
		ids = append(ids, map[string]any{"id": id})
	}
	return ids
}

func (f *lookupFake) threadNodes() []map[string]any {
	out := make([]map[string]any, 0, len(f.threads))
	for _, t := range f.threads {
		out = append(out, map[string]any{"id": t.id, "comments": page(t.idNodes(), "", 0)})
	}
	return out
}

func (f *lookupFake) find(id string) *lookupReview {
	for _, set := range [][]lookupReview{f.pending, f.submitted} {
		for i := range set {
			if set[i].id == id {
				return &set[i]
			}
		}
	}
	return nil
}

func prEnvelope(fields map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"data": map[string]any{
		"repository": map[string]any{"pullRequest": fields},
	}})
	return b
}

func (f *lookupFake) Run(_ context.Context, args ...string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.raw != nil {
		return f.raw, nil
	}
	query := argValue(args, "query")
	after := argValue(args, "after")
	switch {
	case strings.Contains(query, "viewer { login }"):
		f.calls = append(f.calls, "main")
		b, _ := json.Marshal(map[string]any{"data": map[string]any{
			"viewer": map[string]any{"login": f.viewer},
			"repository": map[string]any{"pullRequest": map[string]any{
				"headRefOid":    f.head,
				"pending":       page(reviewNodes(f.pending, true), "", 0),
				"submitted":     page(reviewNodes(f.submitted, false), "", 0),
				"reviewThreads": page(f.threadNodes(), "", 0),
			}},
		}})
		return b, nil
	case strings.Contains(query, "[PENDING]"):
		f.calls = append(f.calls, "pending-page")
		return prEnvelope(map[string]any{"reviews": page(reviewNodes(f.pending, true), after, 0)}), nil
	case strings.Contains(query, "after: $after, states: [COMMENTED"):
		f.calls = append(f.calls, "submitted-page")
		return prEnvelope(map[string]any{"reviews": page(reviewNodes(f.submitted, false), after, 0)}), nil
	case strings.Contains(query, "$reviewId"):
		id := argValue(args, "reviewId")
		f.calls = append(f.calls, "review-comments:"+id)
		r := f.find(id)
		if r == nil {
			return []byte(`{"data":{"node":null}}`), nil
		}
		b, _ := json.Marshal(map[string]any{"data": map[string]any{"node": map[string]any{"comments": page(r.comments, after, 0)}}})
		return b, nil
	case strings.Contains(query, "reviewThreads(first: 100, after: $after)"):
		f.calls = append(f.calls, "threads-page")
		return prEnvelope(map[string]any{"reviewThreads": page(f.threadNodes(), after, 0)}), nil
	case strings.Contains(query, "$threadId"):
		id := argValue(args, "threadId")
		f.calls = append(f.calls, "thread-comments:"+id)
		for _, t := range f.threads {
			if t.id == id {
				b, _ := json.Marshal(map[string]any{"data": map[string]any{"node": map[string]any{"comments": page(t.idNodes(), after, 0)}}})
				return b, nil
			}
		}
		return []byte(`{"data":{"node":null}}`), nil
	}
	return nil, fmt.Errorf("unexpected query: %s", query)
}

func newLookupFake() *lookupFake {
	return &lookupFake{head: "h2", viewer: "review-bot"}
}

func pendingRev(id string, dbID int64, comments ...map[string]any) lookupReview {
	return lookupReview{id: id, dbID: dbID, state: "PENDING", author: "review-bot", commit: "h1", body: "b", comments: comments}
}

func get(t *testing.T, f *lookupFake) *PendingReviewData {
	t.Helper()
	got, err := NewWithRunner(f).GetPendingReview(context.Background(), "foo/bar", 42)
	if err != nil {
		t.Fatalf("GetPendingReview: %v", err)
	}
	return got
}

func TestGetPendingReview_None(t *testing.T) {
	f := newLookupFake()
	got := get(t, f)
	if got.HeadSHA != "h2" || got.Lowest() != nil || len(got.Reviews) != 0 || len(got.Submitted) != 0 {
		t.Fatalf("got %+v", got)
	}
	if strings.Join(f.calls, ",") != "main" {
		t.Errorf("calls = %v, want exactly the one main read", f.calls)
	}
}

func TestGetPendingReview_OneRecordAndQueryShape(t *testing.T) {
	f := newLookupFake()
	f.pending = []lookupReview{pendingRev("PRR_1", 77,
		lcmt("C1", 1, 4, "c1", "h1"),
		lcmt("C2", 2, -1, "c2", "h2"))}
	f.threads = []lookupThread{{"PRRT_x", []string{"other"}}, {"PRRT_1", []string{"C1"}}, {"PRRT_2", []string{"C2", "reply"}}}
	ghCalls := &argRecorder{lookupFake: f}
	got, err := NewWithRunner(ghCalls).GetPendingReview(context.Background(), "foo/bar", 42)
	if err != nil {
		t.Fatalf("GetPendingReview: %v", err)
	}
	r := got.Lowest()
	if got.HeadSHA != "h2" || r == nil {
		t.Fatalf("got %+v", got)
	}
	if r.URL != "https://example.invalid/foo/bar/pull/42#pullrequestreview-77" || r.ID != "PRR_1" || r.DatabaseID != 77 || r.CommitOID != "h1" || r.Body != "b" || len(r.Comments) != 2 {
		t.Fatalf("review = %+v", r)
	}
	c0, c1 := r.Comments[0], r.Comments[1]
	if c0.ID != "C1" || c0.DatabaseID != 1 || c0.Path != "a.go" || c0.Line != 4 || c0.Body != "c1" || c0.OriginalCommitOID != "h1" || c0.ReviewThreadID != "PRRT_1" {
		t.Errorf("comment 0 = %+v", c0)
	}
	if c1.Line != 8 || c1.OriginalCommitOID != "h2" || c1.ReviewThreadID != "PRRT_2" {
		t.Errorf("comment 1 = %+v (null line must fall back to originalLine)", c1)
	}
	if len(ghCalls.args) != 1 {
		t.Fatalf("want exactly one round trip, got %d: %v", len(ghCalls.args), f.calls)
	}
	joined := strings.Join(ghCalls.args[0], " ")
	for _, want := range []string{"api graphql", "headRefOid", "states: [PENDING]", "originalCommit { oid }", "reviewThreads", "owner=foo", "name=bar", "number=42"} {
		if !strings.Contains(joined, want) {
			t.Errorf("call args missing %q", want)
		}
	}
}

type argRecorder struct {
	*lookupFake
	args [][]string
}

func (a *argRecorder) Run(ctx context.Context, args ...string) ([]byte, error) {
	a.args = append(a.args, append([]string(nil), args...))
	return a.lookupFake.Run(ctx, args...)
}

func TestGetPendingReview_NullCommitDecodesEmpty(t *testing.T) {
	f := newLookupFake()
	r := pendingRev("PRR_1", 7, lcmt("C1", 1, 1, "c", ""))
	r.commit = ""
	f.pending = []lookupReview{r}
	got := get(t, f)
	if got.Lowest().CommitOID != "" || got.Lowest().Comments[0].OriginalCommitOID != "" {
		t.Fatalf("null commits must decode empty: %+v", got.Lowest())
	}
}

// Three pending reviews are not an error: all are returned, lowest databaseId
// first, whatever order the host listed them in.
func TestGetPendingReview_ThreeReviewsLowestFirst(t *testing.T) {
	f := newLookupFake()
	f.pending = []lookupReview{pendingRev("PRR_c", 33), pendingRev("PRR_a", 11, lcmt("C1", 1, 1, "c", "h1")), pendingRev("PRR_b", 22)}
	got := get(t, f)
	if len(got.Reviews) != 3 || got.Lowest().DatabaseID != 11 || got.Reviews[1].DatabaseID != 22 || got.Reviews[2].DatabaseID != 33 {
		t.Fatalf("reviews = %+v", got.Reviews)
	}
	if len(got.Lowest().Comments) != 1 {
		t.Errorf("the lowest review keeps its comments: %+v", got.Lowest())
	}
}

// More than one page of pending reviews: the connection is paged to the end.
func TestGetPendingReview_PagesTheReviewConnection(t *testing.T) {
	f := newLookupFake()
	for i := 0; i < 103; i++ {
		f.pending = append(f.pending, pendingRev(fmt.Sprintf("PRR_%d", i), int64(1000-i)))
	}
	got := get(t, f)
	if len(got.Reviews) != 103 || got.Lowest().DatabaseID != 898 {
		t.Fatalf("got %d reviews, lowest %d, want 103 / 898", len(got.Reviews), got.Lowest().DatabaseID)
	}
	if strings.Join(f.calls, ",") != "main,pending-page" {
		t.Errorf("calls = %v", f.calls)
	}
}

// A review with 103 comments is read in full: the first page of 100 from the
// main document, then the rest by cursor.
func TestGetPendingReview_PagesTheCommentsOfAReview(t *testing.T) {
	f := newLookupFake()
	comments := manyComments("C", 103)
	f.pending = []lookupReview{pendingRev("PRR_1", 5, comments...)}
	ids := make([]string, 0, 103)
	for _, c := range comments {
		ids = append(ids, c["id"].(string))
	}
	f.threads = []lookupThread{{"PRRT_all", ids}}
	got := get(t, f)
	r := got.Lowest()
	if len(r.Comments) != 103 {
		t.Fatalf("got %d comments, want 103", len(r.Comments))
	}
	if r.Comments[102].ID != "C-102" || r.Comments[102].ReviewThreadID != "PRRT_all" || r.Comments[0].ReviewThreadID != "PRRT_all" {
		t.Errorf("tail comment = %+v", r.Comments[102])
	}
	if strings.Join(f.calls, ",") != "main,review-comments:PRR_1,thread-comments:PRRT_all" {
		t.Errorf("calls = %v", f.calls)
	}
}

func TestGetPendingReview_ReadsViewersSubmittedReviews(t *testing.T) {
	f := newLookupFake()
	mine := lookupReview{
		id: "REV_mine", dbID: 9, state: "COMMENTED", author: "review-bot", commit: "h2",
		comments: []map[string]any{lcmt("S1", 1, 3, "old finding", "h0")},
	}
	mineNoComments := lookupReview{id: "REV_mine2", dbID: 10, state: "APPROVED", author: "Review-Bot", commit: "h1"}
	theirs := lookupReview{
		id: "REV_theirs", dbID: 11, state: "COMMENTED", author: "teammate", commit: "h2",
		comments: []map[string]any{lcmt("T1", 1, 3, "not mine", "h2")},
	}
	f.submitted = []lookupReview{theirs, mine, mineNoComments}
	f.threads = []lookupThread{{"PRRT_s", []string{"S1"}}}
	got := get(t, f)
	if len(got.Submitted) != 2 {
		t.Fatalf("submitted = %+v, want only the viewer's two", got.Submitted)
	}
	s := got.Submitted[0]
	if s.ID != "REV_mine" || s.CommitOID != "h2" || len(s.Comments) != 1 || s.Comments[0].Body != "old finding" ||
		s.Comments[0].OriginalCommitOID != "h0" || s.Comments[0].ReviewThreadID != "PRRT_s" {
		t.Errorf("submitted review = %+v", s)
	}
	if got.Submitted[1].CommitOID != "h1" || len(got.Submitted[1].Comments) != 0 {
		t.Errorf("second submitted review = %+v", got.Submitted[1])
	}
	for _, c := range f.calls {
		if c == "review-comments:REV_theirs" {
			t.Errorf("another author's comments must not be read: %v", f.calls)
		}
	}
}

// Review threads are read only as far as needed to find every comment's thread.
func TestGetPendingReview_StopsReadingThreadsOnceResolved(t *testing.T) {
	build := func(targetAt int) *lookupFake {
		f := newLookupFake()
		f.pending = []lookupReview{pendingRev("PRR_1", 5, lcmt("C1", 1, 1, "c", "h2"))}
		for i := 0; i < 250; i++ {
			ids := []string{fmt.Sprintf("other-%d", i)}
			if i == targetAt {
				ids = append(ids, "C1")
			}
			f.threads = append(f.threads, lookupThread{fmt.Sprintf("PRRT_%d", i), ids})
		}
		return f
	}
	f := build(5)
	if got := get(t, f); got.Lowest().Comments[0].ReviewThreadID != "PRRT_5" || len(f.calls) != 1 {
		t.Errorf("target on the first page: thread %q, calls %v", got.Lowest().Comments[0].ReviewThreadID, f.calls)
	}
	f = build(150)
	if got := get(t, f); got.Lowest().Comments[0].ReviewThreadID != "PRRT_150" || strings.Join(f.calls, ",") != "main,threads-page" {
		t.Errorf("target on the second page: thread %q, calls %v", got.Lowest().Comments[0].ReviewThreadID, f.calls)
	}
	f = build(-1)
	got := get(t, f)
	if got.Lowest().Comments[0].ReviewThreadID != "" || strings.Join(f.calls, ",") != "main,threads-page,threads-page" {
		t.Errorf("unresolvable thread must stay empty after reading every page: %q, calls %v", got.Lowest().Comments[0].ReviewThreadID, f.calls)
	}
}

func TestGetPendingReview_FailClosed(t *testing.T) {
	good := func() *lookupFake {
		f := newLookupFake()
		f.pending = []lookupReview{pendingRev("PRR_1", 5)}
		return f
	}
	other := good()
	other.pending[0].author = "teammate"
	notPending := good()
	notPending.pending[0].state = "COMMENTED"
	noViewer := good()
	noViewer.viewer = ""
	noHead := good()
	noHead.head = ""
	cases := map[string]*lookupFake{
		"malformed":          {raw: []byte(`not json`)},
		"pr did not resolve": {raw: []byte(`{"data":{"viewer":{"login":"review-bot"},"repository":{"pullRequest":null}}}`)},
		"empty object":       {raw: []byte(`{}`)},
		"graphql errors":     {raw: []byte(`{"errors":[{"message":"boom"}]}`)},
		"no head":            noHead,
		"no viewer":          noViewer,
		"other author":       other,
		"not pending":        notPending,
		"count mismatch": {raw: []byte(`{"data":{"viewer":{"login":"review-bot"},"repository":{"pullRequest":{"headRefOid":"h2",
			"pending":{"totalCount":2,"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]},
			"submitted":{"totalCount":0,"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]},
			"reviewThreads":{"totalCount":0,"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}}`)},
		"next page without a cursor": {raw: []byte(`{"data":{"viewer":{"login":"review-bot"},"repository":{"pullRequest":{"headRefOid":"h2",
			"pending":{"totalCount":1,"pageInfo":{"hasNextPage":true,"endCursor":""},"nodes":[]},
			"submitted":{"totalCount":0,"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]},
			"reviewThreads":{"totalCount":0,"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}}`)},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := NewWithRunner(f).GetPendingReview(context.Background(), "foo/bar", 42)
			if err == nil || got != nil {
				t.Fatalf("must fail closed; got %+v err %v", got, err)
			}
		})
	}
}

// A follow-up page that fails must fail the whole lookup, not shorten it.
func TestGetPendingReview_FollowUpFailureFailsTheLookup(t *testing.T) {
	f := newLookupFake()
	f.pending = []lookupReview{pendingRev("PRR_1", 5, manyComments("C", 103)...)}
	broken := &breakAfterMain{lookupFake: f}
	if got, err := NewWithRunner(broken).GetPendingReview(context.Background(), "foo/bar", 42); err == nil || got != nil {
		t.Fatalf("got %+v err %v, want a failed lookup", got, err)
	}
}

type breakAfterMain struct{ *lookupFake }

func (b *breakAfterMain) Run(ctx context.Context, args ...string) ([]byte, error) {
	if !strings.Contains(argValue(args, "query"), "viewer { login }") {
		return nil, errors.New("gh: Bad Gateway (HTTP 502)")
	}
	return b.lookupFake.Run(ctx, args...)
}

func TestGetPendingReview_PropagatesGHError(t *testing.T) {
	f := newLookupFake()
	boom := errors.New("boom")
	f.err = boom
	if _, err := NewWithRunner(f).GetPendingReview(context.Background(), "foo/bar", 42); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom propagated", err)
	}
}

func TestGetPendingReview_ValidatesInputs(t *testing.T) {
	p := NewWithRunner(newLookupFake())
	for name, c := range map[string]struct {
		repo string
		n    int
	}{"empty repo": {"", 1}, "no slash": {"nope", 1}, "bad number": {"foo/bar", 0}} {
		if _, err := p.GetPendingReview(context.Background(), c.repo, c.n); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

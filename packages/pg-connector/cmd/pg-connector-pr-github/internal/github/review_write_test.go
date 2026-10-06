package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// This file tests the review write primitives (review_write.go) against an
// in-memory fake gh that answers each GraphQL document the way GitHub does:
// one alias per mutation, a null alias for a refused anchor, no document error.

// writeFake is a ghRunner double whose answer is computed from each call's
// stdin payload.
type writeFake struct {
	// handle answers one call; nil means an empty object.
	handle func(args []string, stdin []byte) ([]byte, error)

	calls  [][]string
	stdins [][]byte
}

func (f *writeFake) Run(ctx context.Context, args ...string) ([]byte, error) {
	return f.RunStdin(ctx, nil, args...)
}

func (f *writeFake) RunStdin(_ context.Context, stdin []byte, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	f.stdins = append(f.stdins, append([]byte(nil), stdin...))
	if f.handle == nil {
		return []byte("{}"), nil
	}
	return f.handle(args, stdin)
}

// sentDoc decodes the GraphQL request a call carried.
type sentDoc struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

func decodeSentDoc(t *testing.T, stdin []byte) sentDoc {
	t.Helper()
	var d sentDoc
	if err := json.Unmarshal(stdin, &d); err != nil {
		t.Fatalf("stdin is not a graphql request: %v\n%s", err, stdin)
	}
	return d
}

var aliasDeclRE = regexp.MustCompile(`(?m)^\s+(w\d+): (addPullRequestReviewThread(?:Reply)?)\(`)

// aliasesOf lists (alias, mutation) pairs of a document in order.
func aliasesOf(doc string) [][2]string {
	var out [][2]string
	for _, m := range aliasDeclRE.FindAllStringSubmatch(doc, -1) {
		out = append(out, [2]string{m[1], m[2]})
	}
	return out
}

// answerLanded builds a data object where every alias landed; nullAliases
// answers those aliases with the refused shape instead.
func answerLanded(doc string, nullAliases map[string]bool) []byte {
	data := map[string]any{}
	for _, a := range aliasesOf(doc) {
		name, mut := a[0], a[1]
		switch {
		case mut == "addPullRequestReviewThreadReply" && nullAliases[name]:
			data[name] = map[string]any{"comment": nil}
		case mut == "addPullRequestReviewThreadReply":
			data[name] = map[string]any{"comment": map[string]any{"id": "C_" + name}}
		case nullAliases[name]:
			data[name] = map[string]any{"thread": nil}
		default:
			data[name] = map[string]any{"thread": map[string]any{
				"id":       "T_" + name,
				"comments": map[string]any{"nodes": []any{map[string]any{"id": "C_" + name}}},
			}}
		}
	}
	b, _ := json.Marshal(map[string]any{"data": data})
	return b
}

func pointItems(n int) []ReviewWriteItem {
	items := make([]ReviewWriteItem, n)
	for i := range items {
		items[i] = ReviewWriteItem{Path: fmt.Sprintf("f%d.go", i), Line: i + 1, Body: fmt.Sprintf("point %d", i)}
	}
	return items
}

func newWriteProvider(f *writeFake) *Provider { return NewWithRunner(f) }

// --- create -----------------------------------------------------------

func TestCreateBodyOnlyPendingReview_Success(t *testing.T) {
	f := &writeFake{handle: func([]string, []byte) ([]byte, error) {
		return []byte(`{"id": 991, "node_id": "PRR_node", "state": "PENDING"}`), nil
	}}
	got, err := newWriteProvider(f).CreateBodyOnlyPendingReview(context.Background(), "owner/repo", 7, "abc123", "the body")
	if err != nil {
		t.Fatalf("CreateBodyOnlyPendingReview: %v", err)
	}
	if got.NodeID != "PRR_node" || got.ID != 991 || got.State != "pending" {
		t.Errorf("got %+v", got)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(f.calls))
	}
	call := strings.Join(f.calls[0], " ")
	if !strings.Contains(call, "repos/owner/repo/pulls/7/reviews") || !strings.Contains(call, "--method POST") {
		t.Errorf("unexpected call %q", call)
	}
	var payload map[string]any
	if err := json.Unmarshal(f.stdins[0], &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if _, has := payload["comments"]; has {
		t.Errorf("create must carry no comments, payload = %v", payload)
	}
	if _, has := payload["event"]; has {
		t.Errorf("create must not submit (event set), payload = %v", payload)
	}
	if payload["commit_id"] != "abc123" || payload["body"] != "the body" {
		t.Errorf("payload = %v", payload)
	}
}

func TestCreateBodyOnlyPendingReview_OnePendingReview422(t *testing.T) {
	f := &writeFake{handle: func([]string, []byte) ([]byte, error) {
		return []byte(`{"message":"Unprocessable Entity","errors":["User can only have one pending review per pull request"]}`),
			&ghExecError{
				msg:    "gh api repos/owner/repo/pulls/7/reviews: exit status 1: gh: Unprocessable Entity (HTTP 422)",
				stderr: "gh: Unprocessable Entity (HTTP 422)",
				err:    errors.New("exit status 1"),
			}
	}}
	_, err := newWriteProvider(f).CreateBodyOnlyPendingReview(context.Background(), "owner/repo", 7, "abc123", "b")
	if !errors.Is(err, ErrPendingReviewExists) {
		t.Fatalf("err = %v, want ErrPendingReviewExists", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("calls = %d, want 1 (a write is never retried)", len(f.calls))
	}
}

func TestCreateBodyOnlyPendingReview_OtherFailureIsNotTheSentinel(t *testing.T) {
	f := &writeFake{handle: func([]string, []byte) ([]byte, error) {
		return nil, errors.New("gh: Not Found (HTTP 404)")
	}}
	_, err := newWriteProvider(f).CreateBodyOnlyPendingReview(context.Background(), "owner/repo", 7, "abc123", "b")
	if err == nil || errors.Is(err, ErrPendingReviewExists) {
		t.Fatalf("err = %v, want a non-sentinel error", err)
	}
}

func TestCreateBodyOnlyPendingReview_RejectsBadArguments(t *testing.T) {
	p := newWriteProvider(&writeFake{})
	for name, call := range map[string]func() error{
		"repo": func() error {
			_, e := p.CreateBodyOnlyPendingReview(context.Background(), "norepo", 7, "c", "b")
			return e
		},
		"number": func() error {
			_, e := p.CreateBodyOnlyPendingReview(context.Background(), "o/r", 0, "c", "b")
			return e
		},
		"commit": func() error {
			_, e := p.CreateBodyOnlyPendingReview(context.Background(), "o/r", 7, " ", "b")
			return e
		},
	} {
		if call() == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// --- batches ----------------------------------------------------------

func TestWriteReviewItems_BatchOfTenAllLanded(t *testing.T) {
	f := &writeFake{handle: func(_ []string, stdin []byte) ([]byte, error) {
		return answerLanded(decodeSentDoc(t, stdin).Query, nil), nil
	}}
	res, err := newWriteProvider(f).WriteReviewItems(context.Background(), "PRR_node", pointItems(10))
	if err != nil {
		t.Fatalf("WriteReviewItems: %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(f.calls))
	}
	if len(res) != 10 {
		t.Fatalf("results = %d, want 10", len(res))
	}
	for i, r := range res {
		if !r.Landed || r.Reason != "" || r.ThreadID == "" || r.CommentID == "" {
			t.Errorf("result %d = %+v, want landed with ids", i, r)
		}
	}
	doc := decodeSentDoc(t, f.stdins[0])
	if doc.Variables["review"] != "PRR_node" {
		t.Errorf("pullRequestReviewId variable = %v", doc.Variables["review"])
	}
}

func TestWriteReviewItems_25ItemsSplitTenTenFive(t *testing.T) {
	f := &writeFake{handle: func(_ []string, stdin []byte) ([]byte, error) {
		return answerLanded(decodeSentDoc(t, stdin).Query, nil), nil
	}}
	res, err := newWriteProvider(f).WriteReviewItems(context.Background(), "PRR_node", pointItems(25))
	if err != nil {
		t.Fatalf("WriteReviewItems: %v", err)
	}
	var sizes []int
	for _, in := range f.stdins {
		n := len(aliasesOf(decodeSentDoc(t, in).Query))
		if n > 10 {
			t.Errorf("a document carried %d aliases, max is 10", n)
		}
		sizes = append(sizes, n)
	}
	if fmt.Sprint(sizes) != "[10 10 5]" {
		t.Errorf("document sizes = %v, want [10 10 5]", sizes)
	}
	if len(res) != 25 {
		t.Fatalf("results = %d, want 25", len(res))
	}
	for i, r := range res {
		if !r.Landed {
			t.Errorf("result %d not landed: %+v", i, r)
		}
	}
	// Order is preserved across documents: the last item maps to the last
	// alias of the last document.
	if res[24].ThreadID != "T_w4" {
		t.Errorf("result 24 thread = %q, want T_w4", res[24].ThreadID)
	}
}

func TestWriteReviewItems_NullThreadIsAnchorRejectedNeighboursLanded(t *testing.T) {
	f := &writeFake{handle: func(_ []string, stdin []byte) ([]byte, error) {
		// thread: null and NO GraphQL error, exactly as GitHub answers an
		// invalid line.
		return answerLanded(decodeSentDoc(t, stdin).Query, map[string]bool{"w3": true}), nil
	}}
	res, err := newWriteProvider(f).WriteReviewItems(context.Background(), "PRR_node", pointItems(6))
	if err != nil {
		t.Fatalf("WriteReviewItems: %v", err)
	}
	for i, r := range res {
		if i == 3 {
			if r.Landed || r.Reason != ReasonAnchorRejected {
				t.Errorf("result 3 = %+v, want anchor_rejected", r)
			}
			continue
		}
		if !r.Landed {
			t.Errorf("neighbour %d = %+v, want landed", i, r)
		}
	}
}

func TestWriteReviewItems_ReplyWithoutCommentIsThreadNotFound(t *testing.T) {
	items := []ReviewWriteItem{
		{ReplyToThreadID: "PRRT_good", Body: "reply a"},
		{ReplyToThreadID: "PRRT_foreign", Body: "reply b"},
		{Path: "a.go", Line: 4, Body: "point"},
	}
	f := &writeFake{handle: func(_ []string, stdin []byte) ([]byte, error) {
		return answerLanded(decodeSentDoc(t, stdin).Query, map[string]bool{"w1": true}), nil
	}}
	res, err := newWriteProvider(f).WriteReviewItems(context.Background(), "PRR_node", items)
	if err != nil {
		t.Fatalf("WriteReviewItems: %v", err)
	}
	if !res[0].Landed || res[0].CommentID != "C_w0" {
		t.Errorf("reply 0 = %+v", res[0])
	}
	if res[1].Landed || res[1].Reason != ReasonThreadNotFound {
		t.Errorf("reply 1 = %+v, want thread_not_found", res[1])
	}
	if !res[2].Landed {
		t.Errorf("point 2 = %+v, want landed", res[2])
	}
	doc := decodeSentDoc(t, f.stdins[0])
	got := aliasesOf(doc.Query)
	if len(got) != 3 || got[0][1] != "addPullRequestReviewThreadReply" || got[2][1] != "addPullRequestReviewThread" {
		t.Errorf("mutations = %v", got)
	}
	// A reply always passes the pending review's id.
	if !strings.Contains(doc.Query, "pullRequestReviewId: $review, pullRequestReviewThreadId: $t0") {
		t.Errorf("reply does not pass pullRequestReviewId:\n%s", doc.Query)
	}
	if doc.Variables["t0"] != "PRRT_good" || doc.Variables["t1"] != "PRRT_foreign" {
		t.Errorf("thread variables = %v", doc.Variables)
	}
}

func TestWriteReviewItems_AliasErrorWithNullData(t *testing.T) {
	// A per-alias GraphQL error (gh exits non-zero and still prints stdout)
	// for a reply to a thread that is not on this PR.
	body := `{"data":{"w0":{"comment":null},"w1":{"comment":{"id":"C_ok"}}},
	  "errors":[{"message":"Could not resolve to a node with the global id of 'PRRT_foreign'","path":["w0"]}]}`
	f := &writeFake{handle: func([]string, []byte) ([]byte, error) {
		return []byte(body), errors.New("exit status 1")
	}}
	items := []ReviewWriteItem{{ReplyToThreadID: "PRRT_foreign", Body: "a"}, {ReplyToThreadID: "PRRT_ok", Body: "b"}}
	res, err := newWriteProvider(f).WriteReviewItems(context.Background(), "PRR_node", items)
	if err != nil {
		t.Fatalf("WriteReviewItems: %v", err)
	}
	if res[0].Landed || res[0].Reason != ReasonThreadNotFound {
		t.Errorf("result 0 = %+v, want thread_not_found", res[0])
	}
	if !res[1].Landed {
		t.Errorf("result 1 = %+v, want landed (demonstrably in the answer)", res[1])
	}
}

func TestWriteReviewItems_DocumentErrorMarksBatchUnconfirmed(t *testing.T) {
	calls := 0
	f := &writeFake{handle: func(_ []string, stdin []byte) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(`{"errors":[{"message":"Resource limits for this query exceeded"}]}`), errors.New("exit status 1")
		}
		return answerLanded(decodeSentDoc(t, stdin).Query, nil), nil
	}}
	res, err := newWriteProvider(f).WriteReviewItems(context.Background(), "PRR_node", pointItems(12))
	if err != nil {
		t.Fatalf("WriteReviewItems: %v", err)
	}
	for i := 0; i < 10; i++ {
		if res[i].Landed || res[i].Reason != ReasonUnconfirmed {
			t.Errorf("result %d = %+v, want unconfirmed", i, res[i])
		}
	}
	// A document-level failure does not stop the next document.
	for i := 10; i < 12; i++ {
		if !res[i].Landed {
			t.Errorf("result %d = %+v, want landed", i, res[i])
		}
	}
}

func TestWriteReviewItems_TransportErrorIsUnconfirmed(t *testing.T) {
	f := &writeFake{handle: func([]string, []byte) ([]byte, error) {
		return nil, errors.New("error connecting to api.github.com")
	}}
	res, _ := newWriteProvider(f).WriteReviewItems(context.Background(), "PRR_node", pointItems(3))
	for i, r := range res {
		if r.Landed || r.Reason != ReasonUnconfirmed {
			t.Errorf("result %d = %+v, want unconfirmed", i, r)
		}
	}
	if len(f.calls) != 1 {
		t.Errorf("calls = %d, want 1 (a write is never retried, even a transient one)", len(f.calls))
	}
}

func TestWriteReviewItems_SecondaryRateLimitIsRateLimitedAndNotRetried(t *testing.T) {
	f := &writeFake{handle: func([]string, []byte) ([]byte, error) {
		return []byte(`{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again."}`),
			&ghExecError{
				msg:    "gh api graphql: exit status 1: gh: You have exceeded a secondary rate limit (HTTP 403)",
				stderr: "gh: You have exceeded a secondary rate limit (HTTP 403)",
				err:    errors.New("exit status 1"),
			}
	}}
	res, err := newWriteProvider(f).WriteReviewItems(context.Background(), "PRR_node", pointItems(15))
	if err != nil {
		t.Fatalf("WriteReviewItems: %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("fake saw %d calls, want ONE (no retry, no further documents after a rate limit)", len(f.calls))
	}
	for i, r := range res {
		if r.Landed || r.Reason != ReasonRateLimited {
			t.Errorf("result %d = %+v, want rate_limited", i, r)
		}
	}
}

func TestWriteReviewItems_SideNormalizedAndBodiesTravelAsVariables(t *testing.T) {
	f := &writeFake{handle: func(_ []string, stdin []byte) ([]byte, error) {
		return answerLanded(decodeSentDoc(t, stdin).Query, nil), nil
	}}
	items := []ReviewWriteItem{
		{Path: "a.go", Line: 2, Side: "", Body: "default side"},
		{Path: "a.go", Line: 3, Side: "left", Body: `quote " and @file and {braces}`},
	}
	if _, err := newWriteProvider(f).WriteReviewItems(context.Background(), "PRR_node", items); err != nil {
		t.Fatal(err)
	}
	doc := decodeSentDoc(t, f.stdins[0])
	if doc.Variables["s0"] != "RIGHT" || doc.Variables["s1"] != "LEFT" {
		t.Errorf("sides = %v / %v, want RIGHT / LEFT", doc.Variables["s0"], doc.Variables["s1"])
	}
	if doc.Variables["b1"] != items[1].Body {
		t.Errorf("body variable = %v", doc.Variables["b1"])
	}
	if strings.Contains(doc.Query, "braces") || strings.Contains(doc.Query, "default side") {
		t.Errorf("a body leaked into the document text:\n%s", doc.Query)
	}
}

func TestWriteReviewItems_InvalidArgumentsSendNothing(t *testing.T) {
	f := &writeFake{}
	p := newWriteProvider(f)
	if _, err := p.WriteReviewItems(context.Background(), " ", pointItems(1)); err == nil {
		t.Error("empty review id: want an error")
	}
	if _, err := p.WriteReviewItems(context.Background(), "PRR_node", []ReviewWriteItem{{Path: "a.go", Line: 0, Body: "x"}}); err == nil {
		t.Error("zero line: want an error")
	}
	if len(f.calls) != 0 {
		t.Errorf("calls = %d, want 0", len(f.calls))
	}
}

func TestWriteReviewItems_EmptyItemsSendsNothing(t *testing.T) {
	f := &writeFake{}
	res, err := newWriteProvider(f).WriteReviewItems(context.Background(), "PRR_node", nil)
	if err != nil || len(res) != 0 || len(f.calls) != 0 {
		t.Errorf("res=%v err=%v calls=%d", res, err, len(f.calls))
	}
}

// --- body update ------------------------------------------------------

func TestUpdateReviewBody_Success(t *testing.T) {
	f := &writeFake{handle: func([]string, []byte) ([]byte, error) {
		return []byte(`{"data":{"updatePullRequestReview":{"pullRequestReview":{"id":"PRR_node"}}}}`), nil
	}}
	if err := newWriteProvider(f).UpdateReviewBody(context.Background(), "PRR_node", "whole new body"); err != nil {
		t.Fatalf("UpdateReviewBody: %v", err)
	}
	doc := decodeSentDoc(t, f.stdins[0])
	if !strings.Contains(doc.Query, "updatePullRequestReview") || doc.Variables["review"] != "PRR_node" || doc.Variables["body"] != "whole new body" {
		t.Errorf("doc = %+v", doc)
	}
}

func TestUpdateReviewBody_TwoPendingReviewsRefusalRecognized(t *testing.T) {
	cases := map[string]func() ([]byte, error){
		"gh error": func() ([]byte, error) {
			return []byte(`{"data":{"updatePullRequestReview":null},"errors":[{"type":"UNPROCESSABLE","message":"User can only have one pending review per pull request"}]}`),
				&ghExecError{
					msg:    "gh api graphql: exit status 1: gh: User can only have one pending review per pull request",
					stderr: "gh: User can only have one pending review per pull request",
					err:    errors.New("exit status 1"),
				}
		},
		"errors in a clean exit": func() ([]byte, error) {
			return []byte(`{"data":{"updatePullRequestReview":null},"errors":[{"type":"UNPROCESSABLE","message":"User can only have one pending review per pull request"}]}`), nil
		},
	}
	for name, answer := range cases {
		t.Run(name, func(t *testing.T) {
			f := &writeFake{handle: func([]string, []byte) ([]byte, error) { return answer() }}
			err := newWriteProvider(f).UpdateReviewBody(context.Background(), "PRR_node", "b")
			if !errors.Is(err, ErrTwoPendingReviews) {
				t.Fatalf("err = %v, want ErrTwoPendingReviews", err)
			}
			if len(f.calls) != 1 {
				t.Errorf("calls = %d, want 1", len(f.calls))
			}
		})
	}
}

func TestUpdateReviewBody_OtherFailureIsNotTheSentinel(t *testing.T) {
	f := &writeFake{handle: func([]string, []byte) ([]byte, error) {
		return nil, errors.New("error connecting to api.github.com")
	}}
	err := newWriteProvider(f).UpdateReviewBody(context.Background(), "PRR_node", "b")
	if err == nil || errors.Is(err, ErrTwoPendingReviews) {
		t.Fatalf("err = %v, want a non-sentinel error", err)
	}
	if err := newWriteProvider(&writeFake{}).UpdateReviewBody(context.Background(), "", "b"); err == nil {
		t.Error("empty review id: want an error")
	}
}

func TestUpdateReviewBody_EmptyAnswerIsNotSuccess(t *testing.T) {
	f := &writeFake{handle: func([]string, []byte) ([]byte, error) {
		return []byte(`{"data":{"updatePullRequestReview":null}}`), nil
	}}
	if err := newWriteProvider(f).UpdateReviewBody(context.Background(), "PRR_node", "b"); err == nil {
		t.Fatal("an answer carrying no review must not count as success")
	}
}

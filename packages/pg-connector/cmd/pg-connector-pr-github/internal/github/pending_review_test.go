package github

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// pendingJSON builds a pending-review graphql response around one review node
// body (or none when reviews is empty). Logins are the generic placeholders
// this module's identifier guard allows.
func pendingJSON(head, viewer, reviews string, total int) []byte {
	return []byte(`{"data":{"viewer":{"login":"` + viewer + `"},"repository":{"pullRequest":{"headRefOid":"` + head +
		`","reviews":{"totalCount":` + strconv.Itoa(total) + `,"nodes":[` + reviews + `]}}}}}`)
}

const oneReviewNode = `{"id":"PRR_1","databaseId":77,"url":"https://example.invalid/foo/bar/pull/42#pullrequestreview-77","state":"PENDING","author":{"login":"review-bot"},
"commit":{"oid":"h1"},"body":"b","comments":{"totalCount":2,"nodes":[
{"id":"C1","databaseId":1,"path":"a.go","line":4,"originalLine":4,"body":"c1"},
{"id":"C2","databaseId":2,"path":"b.go","line":null,"originalLine":8,"body":"c2"}]}}`

func TestGetPendingReview_RecordAndQueryShape(t *testing.T) {
	gh := newFakeGH()
	gh.responses["api graphql"] = pendingJSON("h2", "review-bot", oneReviewNode, 1)
	got, err := NewWithRunner(gh).GetPendingReview(context.Background(), "foo/bar", 42)
	if err != nil {
		t.Fatalf("GetPendingReview: %v", err)
	}
	if got.HeadSHA != "h2" || got.Review == nil {
		t.Fatalf("got %+v", got)
	}
	r := got.Review
	if r.URL != "https://example.invalid/foo/bar/pull/42#pullrequestreview-77" {
		t.Errorf("URL = %q", r.URL)
	}
	if r.ID != "PRR_1" || r.DatabaseID != 77 || r.CommitOID != "h1" || r.Body != "b" || len(r.Comments) != 2 {
		t.Fatalf("review = %+v", r)
	}
	if r.Comments[0].Line != 4 || r.Comments[1].Line != 8 || r.Comments[1].Path != "b.go" || r.Comments[1].DatabaseID != 2 {
		t.Errorf("comments = %+v (null line must fall back to originalLine)", r.Comments)
	}
	if len(gh.calls) != 1 {
		t.Fatalf("want exactly one round trip, got %d", len(gh.calls))
	}
	joined := strings.Join(gh.calls[0], " ")
	for _, want := range []string{"api graphql", "headRefOid", "url", "states: [PENDING]", "commit { oid }", "owner=foo", "name=bar", "number=42"} {
		if !strings.Contains(joined, want) {
			t.Errorf("call args missing %q: %s", want, joined)
		}
	}
}

func TestGetPendingReview_NullCommitDecodesEmpty(t *testing.T) {
	node := strings.Replace(oneReviewNode, `"commit":{"oid":"h1"}`, `"commit":null`, 1)
	gh := newFakeGH()
	gh.responses["api graphql"] = pendingJSON("h2", "review-bot", node, 1)
	got, err := NewWithRunner(gh).GetPendingReview(context.Background(), "foo/bar", 42)
	if err != nil || got.Review == nil || got.Review.CommitOID != "" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestGetPendingReview_NoneIsAffirmative(t *testing.T) {
	gh := newFakeGH()
	gh.responses["api graphql"] = pendingJSON("h2", "review-bot", "", 0)
	got, err := NewWithRunner(gh).GetPendingReview(context.Background(), "foo/bar", 42)
	if err != nil || got == nil || got.Review != nil || got.HeadSHA != "h2" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestGetPendingReview_FailClosed(t *testing.T) {
	cases := map[string][]byte{
		"malformed":          []byte(`not json`),
		"pr did not resolve": []byte(`{"data":{"viewer":{"login":"review-bot"},"repository":{"pullRequest":null}}}`),
		"empty object":       []byte(`{}`),
		"no head":            pendingJSON("", "review-bot", "", 0),
		"other author":       pendingJSON("h2", "teammate", oneReviewNode, 1),
		"no viewer":          pendingJSON("h2", "", oneReviewNode, 1),
		"two reviews":        pendingJSON("h2", "review-bot", oneReviewNode+","+oneReviewNode, 2),
		"count mismatch":     pendingJSON("h2", "review-bot", oneReviewNode, 2),
		"not pending":        pendingJSON("h2", "review-bot", strings.Replace(oneReviewNode, `"PENDING"`, `"COMMENTED"`, 1), 1),
		"comments truncated": pendingJSON("h2", "review-bot",
			strings.Replace(oneReviewNode, `"totalCount":2`, `"totalCount":3`, 1), 1),
	}
	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			gh := newFakeGH()
			gh.responses["api graphql"] = resp
			got, err := NewWithRunner(gh).GetPendingReview(context.Background(), "foo/bar", 42)
			if err == nil || got != nil {
				t.Fatalf("must fail closed; got %+v err %v", got, err)
			}
		})
	}
}

func TestGetPendingReview_PropagatesGHError(t *testing.T) {
	gh := newFakeGH()
	boom := errors.New("boom")
	gh.errs["api graphql"] = boom
	if _, err := NewWithRunner(gh).GetPendingReview(context.Background(), "foo/bar", 42); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom propagated", err)
	}
}

func TestGetPendingReview_ValidatesInputs(t *testing.T) {
	p := NewWithRunner(newFakeGH())
	for name, c := range map[string]struct {
		repo string
		n    int
	}{"empty repo": {"", 1}, "no slash": {"nope", 1}, "bad number": {"foo/bar", 0}} {
		if _, err := p.GetPendingReview(context.Background(), c.repo, c.n); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

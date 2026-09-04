package github

import (
	"context"
	"errors"
	"testing"
)

func TestParseFingerprints_Basic(t *testing.T) {
	raw := []byte(`{"data":{"rateLimit":{"cost":1,"remaining":4999},
	  "search":{"pageInfo":{"hasNextPage":false,"endCursor":"Y"},"nodes":[
	    {"number":7,"updatedAt":"2026-06-09T00:00:00Z","isDraft":false,"state":"OPEN",
	     "author":{"__typename":"User","login":"alice"},
	     "repository":{"nameWithOwner":"o/r"},
	     "commits":{"nodes":[{"commit":{"oid":"abc","statusCheckRollup":{"state":"SUCCESS"}}}]},
	     "reviews":{"totalCount":2},"comments":{"totalCount":3},"reviewThreads":{"totalCount":1}},
	    {"number":8,"updatedAt":"2026-06-09T01:00:00Z","isDraft":true,"state":"OPEN",
	     "author":{"__typename":"Bot","login":"claude"},
	     "repository":{"nameWithOwner":"o/r2"},
	     "commits":{"nodes":[{"commit":{"oid":"def","statusCheckRollup":null}}]},
	     "reviews":{"totalCount":0},"comments":{"totalCount":0},"reviewThreads":{"totalCount":0}}
	  ]}}}`)
	res, cursor, more, err := parseFingerprints(raw)
	if err != nil {
		t.Fatalf("parseFingerprints: %v", err)
	}
	if more || cursor != "Y" {
		t.Fatalf("pageInfo: more=%v cursor=%q", more, cursor)
	}
	if res.RateCost != 1 || res.RateLeft != 4999 {
		t.Errorf("rate: cost=%d left=%d", res.RateCost, res.RateLeft)
	}
	if len(res.PRs) != 2 {
		t.Fatalf("want 2 PRs, got %d", len(res.PRs))
	}
	p0 := res.PRs[0]
	if p0.Repo != "o/r" || p0.Number != 7 || p0.Author != "alice" ||
		p0.State != "open" || p0.HeadOID != "abc" || p0.StatusRollup != "SUCCESS" ||
		p0.ReviewCount != 2 || p0.CommentCount != 3 || p0.ReviewThreadCount != 1 {
		t.Errorf("p0 mismatch: %+v", p0)
	}
	if res.PRs[1].Author != "claude[bot]" {
		t.Errorf("bot login not canonicalized: %q", res.PRs[1].Author)
	}
	if res.PRs[1].StatusRollup != "" {
		t.Errorf("nil rollup should yield empty StatusRollup, got %q", res.PRs[1].StatusRollup)
	}
}

func TestParseFingerprints_GraphQLError(t *testing.T) {
	_, _, _, err := parseFingerprints([]byte(`{"errors":[{"message":"boom"}]}`))
	if err == nil {
		t.Fatal("want error on errors envelope")
	}
}

func TestFingerprintPRs_SinglePage(t *testing.T) {
	raw := []byte(`{"data":{"rateLimit":{"cost":1,"remaining":10},
	  "search":{"pageInfo":{"hasNextPage":false},"nodes":[
	    {"number":1,"state":"OPEN","author":{"login":"me"},"repository":{"nameWithOwner":"o/r"},
	     "commits":{"nodes":[{"commit":{"oid":"x"}}]}}]}}}`)
	p := NewWithRunner(&fakeStdinRunner{out: raw})
	res, err := p.FingerprintPRs(context.Background(), "is:pr is:open author:me")
	if err != nil {
		t.Fatalf("FingerprintPRs: %v", err)
	}
	if res.Truncated || len(res.PRs) != 1 || res.PRs[0].Number != 1 {
		t.Fatalf("unexpected: %+v", res)
	}
}

func TestFingerprintPRs_EmptySearchRejected(t *testing.T) {
	p := NewWithRunner(&fakeStdinRunner{out: []byte("{}")})
	if _, err := p.FingerprintPRs(context.Background(), "  "); err == nil {
		t.Fatal("want error on empty search")
	}
}

func TestFingerprintPRs_RunnerError(t *testing.T) {
	p := NewWithRunner(&fakeStdinRunner{err: errors.New("gh boom")})
	if _, err := p.FingerprintPRs(context.Background(), "is:pr"); err == nil {
		t.Fatal("want error when runner fails")
	}
}

// pagingRunner returns a different page per RunStdin call.
type pagingRunner struct {
	pages [][]byte
	calls int
}

func (r *pagingRunner) Run(_ context.Context, _ ...string) ([]byte, error) { return nil, nil }
func (r *pagingRunner) RunStdin(_ context.Context, _ []byte, _ ...string) ([]byte, error) {
	out := r.pages[r.calls]
	r.calls++
	return out, nil
}

func TestFingerprintPRs_Paginates(t *testing.T) {
	page1 := []byte(`{"data":{"rateLimit":{"cost":1,"remaining":9},
	  "search":{"pageInfo":{"hasNextPage":true,"endCursor":"C1"},"nodes":[
	    {"number":1,"state":"OPEN","author":{"login":"me"},"repository":{"nameWithOwner":"o/r"},"commits":{"nodes":[{"commit":{"oid":"a"}}]}}]}}}`)
	page2 := []byte(`{"data":{"rateLimit":{"cost":1,"remaining":8},
	  "search":{"pageInfo":{"hasNextPage":false},"nodes":[
	    {"number":2,"state":"OPEN","author":{"login":"me"},"repository":{"nameWithOwner":"o/r"},"commits":{"nodes":[{"commit":{"oid":"b"}}]}}]}}}`)
	r := &pagingRunner{pages: [][]byte{page1, page2}}
	res, err := NewWithRunner(r).FingerprintPRs(context.Background(), "is:pr is:open author:me")
	if err != nil {
		t.Fatalf("FingerprintPRs: %v", err)
	}
	if r.calls != 2 {
		t.Errorf("want 2 page fetches, got %d", r.calls)
	}
	if len(res.PRs) != 2 || res.PRs[0].Number != 1 || res.PRs[1].Number != 2 {
		t.Errorf("pages not accumulated: %+v", res.PRs)
	}
}

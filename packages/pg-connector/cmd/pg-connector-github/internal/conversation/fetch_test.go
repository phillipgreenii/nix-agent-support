package conversation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/metered"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

var background = ratefeed.Meta{Class: governor.ClassKeepAlive, Purpose: governor.PurposeRefresh}

// fakeGH is the gh the metered Runner runs. It records every call and answers
// from reply.
type fakeGH struct {
	mu    sync.Mutex
	lines []string
	reply func(line string) ([]byte, error)
}

func (g *fakeGH) Run(_ context.Context, args ...string) ([]byte, error) {
	line := strings.Join(args, " ")
	g.mu.Lock()
	g.lines = append(g.lines, line)
	g.mu.Unlock()
	return g.reply(line)
}

func (g *fakeGH) RunStdin(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	return g.Run(ctx, args...)
}

func (g *fakeGH) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.lines)
}

// rig is a Fetcher over the real governor and the real metered Provider, with a
// fake gh at the bottom.
type rig struct {
	gh     *fakeGH
	gov    *governor.Governor
	events []governor.SpendEvent
	f      *Fetcher
}

func newRig(t *testing.T, reply func(line string) ([]byte, error)) *rig {
	t.Helper()
	clk := governor.NewManualClock(t0)
	r := &rig{gh: &fakeGH{reply: reply}}
	gov, err := governor.New(ratefeed.GovernorConfig(config.Defaults().Settings), governor.Options{
		Clock:   clk,
		OnSpend: func(e governor.SpendEvent) { r.events = append(r.events, e) },
	})
	if err != nil {
		t.Fatal(err)
	}
	r.gov = gov
	runner := &metered.Runner{Inner: r.gh, Governor: gov, Feed: ratefeed.New(gov, r.gh, clk)}
	r.f = &Fetcher{Reader: metered.NewProvider(runner)}
	return r
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// pagesFromFixtures answers each of the documents of the Provider's reads from
// the fixture of the same name: the threads, the issue comments and the reviews.
func pagesFromFixtures(t *testing.T) func(string) ([]byte, error) {
	t.Helper()
	threads, issue, reviews := fixture(t, "threads.json"), fixture(t, "issue_comments.json"), fixture(t, "reviews.json")
	return func(line string) ([]byte, error) {
		switch documentOf(line) {
		case "threads":
			return threads, nil
		case "reviews":
			return reviews, nil
		default:
			return issue, nil
		}
	}
}

// documentOf says which of the three connections a gh invocation reads.
func documentOf(line string) string {
	switch {
	case strings.Contains(line, "reviewThreads(first"):
		return "threads"
	case strings.Contains(line, "reviews(first"):
		return "reviews"
	default:
		return "issue"
	}
}

func target(n int) groups.Target {
	return groups.Target{
		NodeID: fmt.Sprintf("PR_node_%d", n),
		Ref:    entity.Ref{Owner: "example-org", Repo: "example-repo", Number: n},
		Stamps: groups.Stamps{Head: "head-" + strconv.Itoa(n), Base: "base-1"},
	}
}

func str(s string) *string { return &s }

// want is what the old Show puts in comments, reviews and connections for the
// pages of the fixtures (newest first; the review comment nested in its review;
// the review whose commit is gone carries a pointer to the empty string).
func wantParity() Content {
	return Content{
		Comments: []schema.PRComment{
			{ID: "IC_2", Author: "teammate", Body: "second"},
			{ID: "IC_1", Author: "teammate", Body: "first"},
		},
		Reviews: []schema.PRReview{
			{
				ID: "PRR_1", Author: "review-bot", State: "CHANGES_REQUESTED", Body: "please fix",
				SubmittedAt: "2026-10-12T10:30:00Z", CommitOID: str("abc123"),
				Comments: []schema.PRComment{{
					ID: "PRRC_1", Author: "review-bot", Body: "nit", Path: "a.go", Line: 3,
					ThreadID: "PRRC_1", ReviewThreadID: "PRRT_1", Resolved: true, ThreadOutdated: true,
				}},
			},
			{ID: "PRR_2", Author: "teammate", State: "COMMENTED", SubmittedAt: "2026-10-12T08:00:00Z", CommitOID: str("")},
		},
		Connections: schema.PRConnections{
			Reviews:  schema.PRConnectionReport{Total: 2, Returned: 2},
			Threads:  schema.PRConnectionReport{Total: 1, Returned: 1},
			Comments: schema.PRConnectionReport{Total: 3, Returned: 3},
		},
	}
}

func TestFetcher_Identity(t *testing.T) {
	f := &Fetcher{}
	if f.Group() != groups.Conversation || f.Group() != "conversation" {
		t.Errorf("Group = %q", f.Group())
	}
	if f.MaxBatch() != 1 {
		t.Errorf("MaxBatch = %d, want 1", f.MaxBatch())
	}
	for n, wantCost := range map[int]int64{1: 3, 2: 6, 5: 15} {
		req := f.Spend(governor.ClassInvalidated, n)
		if req.Bucket != governor.BucketGraphQL || req.Class != governor.ClassInvalidated || req.Cost != wantCost {
			t.Errorf("Spend(%d) = %+v, want GraphQL cost %d", n, req, wantCost)
		}
	}
}

func TestFetch_ContentEqualsWhatTheOldShowPlacesForTheSamePages(t *testing.T) {
	r := newRig(t, pagesFromFixtures(t))
	tg := target(7)

	got := r.f.Fetch(context.Background(), background, []groups.Target{tg})
	if len(got) != 1 || got[0].Err != nil {
		t.Fatalf("outcomes = %+v", got)
	}
	c, err := Decode(got[0].Content)
	if err != nil {
		t.Fatal(err)
	}
	if want := wantParity(); !reflect.DeepEqual(c, want) {
		t.Errorf("content =\n%+v\nwant\n%+v", c, want)
	}
	if got[0].Stamps != tg.Stamps {
		t.Errorf("Stamps = %+v, want the target's %+v", got[0].Stamps, tg.Stamps)
	}
	if fp, _ := Fingerprint(c); got[0].Fingerprint == "" || got[0].Fingerprint != fp {
		t.Errorf("Fingerprint = %q, want %q", got[0].Fingerprint, fp)
	}
	// The Reader was asked for the PR's repo and number.
	for _, line := range r.gh.lines {
		if !strings.Contains(line, "owner=example-org") || !strings.Contains(line, "name=example-repo") {
			t.Errorf("call %q does not name the PR's repository", line)
		}
		if strings.Contains(line, "number=") && !strings.Contains(line, "number=7") {
			t.Errorf("call %q does not name PR 7", line)
		}
	}
}

func TestFetch_EveryPageIsAdmittedAndBookedAsTheCallStates(t *testing.T) {
	r := newRig(t, pagesFromFixtures(t))
	r.f.Fetch(context.Background(), ratefeed.Meta{Class: governor.ClassInvalidated, Purpose: governor.PurposeRefresh}, []groups.Target{target(7)})

	// One page of threads, one of issue comments, one of reviews.
	if r.gh.count() != 3 || len(r.events) != 3 {
		t.Fatalf("%d gh calls and %d booked spends, want 3 and 3 (one admission per page)", r.gh.count(), len(r.events))
	}
	for i, e := range r.events {
		if e.Bucket != governor.BucketGraphQL || e.Class != governor.ClassInvalidated || e.Purpose != governor.PurposeRefresh ||
			e.Group != groups.Conversation || e.BatchSize != 1 || e.Outcome != ratefeed.OutcomeOK {
			t.Errorf("spend %d = %+v", i, e)
		}
	}
}

func TestFetch_TheFingerprintIsStableAndFollowsTheContent(t *testing.T) {
	issue := fixture(t, "issue_comments.json")
	edited := []byte(strings.Replace(string(issue), `"second"`, `"second, edited"`, 1))
	if string(edited) == string(issue) {
		t.Fatal("fixture has no comment body to edit")
	}
	current := issue
	pages := pagesFromFixtures(t)
	r := newRig(t, func(line string) ([]byte, error) {
		if documentOf(line) == "issue" {
			return current, nil
		}
		return pages(line)
	})
	fetch := func() groups.Outcome {
		t.Helper()
		o := r.f.Fetch(context.Background(), background, []groups.Target{target(7)})[0]
		if o.Err != nil {
			t.Fatal(o.Err)
		}
		return o
	}
	a, b := fetch(), fetch()
	if a.Fingerprint != b.Fingerprint || string(a.Content) != string(b.Content) {
		t.Errorf("two identical fetches differ: %s vs %s", a.Fingerprint, b.Fingerprint)
	}
	current = edited
	if c := fetch(); c.Fingerprint == a.Fingerprint {
		t.Errorf("fingerprint did not change when a comment body changed: %s", c.Fingerprint)
	}
}

// reviewsPage is one page of the reviews connection of a PR that holds total
// reviews, at the page number the cursor names (R<k> is after page k).
func reviewsPage(line string, total int) []byte {
	page := 1
	if i := strings.Index(line, "after=R"); i >= 0 {
		page, _ = strconv.Atoi(strings.Fields(line[i+len("after=R"):])[0])
		page++
	}
	first := (page-1)*100 + 1
	last := min(page*100, total)
	var nodes []string
	for n := first; n <= last; n++ {
		nodes = append(nodes, fmt.Sprintf(`{"id":"PRR_%d","state":"COMMENTED","body":"r%d","createdAt":"2026-10-12T08:00:00Z",`+
			`"submittedAt":"2026-10-12T08:00:00Z","author":{"login":"teammate"},"commit":{"oid":"abc123"}}`, n, n))
	}
	return []byte(fmt.Sprintf(`{"data":{"rateLimit":{"cost":1},"repository":{"pullRequest":{"reviews":{"totalCount":%d,`+
		`"pageInfo":{"hasNextPage":%t,"endCursor":"R%d"},"nodes":[%s]}}}}}`, total, last < total, page, strings.Join(nodes, ",")))
}

func TestFetch_AConnectionOverItsCapIsReportedTruncatedAndReturned(t *testing.T) {
	const total = 1050
	pages := pagesFromFixtures(t)
	r := newRig(t, func(line string) ([]byte, error) {
		if documentOf(line) == "reviews" {
			return reviewsPage(line, total), nil
		}
		return pages(line)
	})

	o := r.f.Fetch(context.Background(), background, []groups.Target{target(7)})[0]
	if o.Err != nil {
		t.Fatal(o.Err)
	}
	c, err := Decode(o.Content)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Reviews) != 1000 {
		t.Errorf("returned %d reviews, want the cap of 1000", len(c.Reviews))
	}
	if want := (schema.PRConnectionReport{Truncated: true, Total: total, Returned: 1000}); c.Connections.Reviews != want {
		t.Errorf("reviews report = %+v, want %+v", c.Connections.Reviews, want)
	}
	if c.Connections.Threads.Truncated || c.Connections.Comments.Truncated {
		t.Errorf("connections within their caps are reported truncated: %+v", c.Connections)
	}
	// Reads page 100 at a time until the cap: ten review pages, plus the one page
	// of threads and the one of issue comments; each page is its own admission.
	var reviewPages int
	for _, line := range r.gh.lines {
		if !strings.Contains(line, "first: 100") {
			t.Errorf("call %q does not page by 100", line)
		}
		if documentOf(line) == "reviews" {
			reviewPages++
		}
	}
	if reviewPages != 10 || r.gh.count() != 12 || len(r.events) != 12 {
		t.Errorf("%d review pages, %d gh calls, %d admissions; want 10, 12 and 12", reviewPages, r.gh.count(), len(r.events))
	}
}

// stubReader is a ConversationReader answering from fixed values.
type stubReader struct {
	comments    *api.CommentsResult
	commentsErr error
	reviews     *api.ReviewsResult
	reviewsErr  error
	calls       []string
}

func (s *stubReader) ListCommentsReport(_ context.Context, _ string, _ int) (*api.CommentsResult, error) {
	s.calls = append(s.calls, "comments")
	return s.comments, s.commentsErr
}

func (s *stubReader) ListReviewsReport(_ context.Context, _ string, _ int) (*api.ReviewsResult, error) {
	s.calls = append(s.calls, "reviews")
	return s.reviews, s.reviewsErr
}

func TestFetch_AFailingReadIsThatTargetsErrorAndNoContent(t *testing.T) {
	boom := errors.New("boom: connection reset")
	for name, s := range map[string]*stubReader{
		"comments": {commentsErr: boom},
		"reviews":  {comments: &api.CommentsResult{}, reviewsErr: boom},
	} {
		f := &Fetcher{Reader: s}
		tg := target(7)
		got := f.Fetch(context.Background(), background, []groups.Target{tg, target(8)})
		for i, o := range got {
			if !errors.Is(o.Err, boom) || o.Content != nil || o.Fingerprint != "" {
				t.Errorf("%s: outcome %d = %+v, want that error and no content", name, i, o)
			}
			if o.Stamps != []groups.Target{tg, target(8)}[i].Stamps {
				t.Errorf("%s: outcome %d stamps = %+v", name, i, o.Stamps)
			}
		}
	}
	// A failed comments read is not followed by the reviews read.
	s := &stubReader{commentsErr: boom}
	(&Fetcher{Reader: s}).Fetch(context.Background(), background, []groups.Target{target(7)})
	if !reflect.DeepEqual(s.calls, []string{"comments"}) {
		t.Errorf("reads = %v, want just the failed one", s.calls)
	}
}

func TestFetch_APRGitHubNoLongerFindsIsGone(t *testing.T) {
	r := newRig(t, func(string) ([]byte, error) {
		return []byte(`{"data":{"rateLimit":{"cost":1},"repository":{"pullRequest":null}}}`), nil
	})
	got := r.f.Fetch(context.Background(), background, []groups.Target{target(7)})
	if !errors.Is(got[0].Err, groups.ErrGone) || got[0].Content != nil {
		t.Errorf("outcome = %+v, want ErrGone and no content", got[0])
	}

	// The same through the gh failure an unresolved id makes.
	r = newRig(t, func(string) ([]byte, error) {
		return nil, errors.New("gh api graphql: exit status 1: Could not resolve to a PullRequest with the number of 7")
	})
	got = r.f.Fetch(context.Background(), background, []groups.Target{target(7)})
	if !errors.Is(got[0].Err, groups.ErrGone) {
		t.Errorf("err = %v, want ErrGone", got[0].Err)
	}
}

func TestFetch_AGovernorRefusalIsReturnedUntouchedAndStopsTheRest(t *testing.T) {
	r := newRig(t, pagesFromFixtures(t))
	// Room for exactly one page of background spend.
	bs, _ := r.gov.State().Bucket(governor.BucketGraphQL)
	if err := r.gov.Record(governor.Spend{Bucket: governor.BucketGraphQL, Class: governor.ClassQuery, Purpose: governor.PurposeQuery, Cost: bs.BackgroundCap - 1}); err != nil {
		t.Fatal(err)
	}

	got := r.f.Fetch(context.Background(), background, []groups.Target{target(7), target(8)})
	for i, o := range got {
		var refusal *governor.Refusal
		if !errors.As(o.Err, &refusal) || refusal.Kind != governor.RefusedBackgroundShare || o.Content != nil {
			t.Errorf("outcome %d = %+v, want the background-share refusal and no content", i, o)
		}
		if errors.Is(o.Err, groups.ErrGone) {
			t.Errorf("outcome %d: a refusal is not a gone PR", i)
		}
	}
	if r.gh.count() != 1 {
		t.Errorf("gh ran %d times, want only the page that was admitted", r.gh.count())
	}
}

func TestFetch_ADoneContextFailsTheTargetsWithoutReads(t *testing.T) {
	s := &stubReader{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := (&Fetcher{Reader: s}).Fetch(ctx, background, []groups.Target{target(7)})
	if !errors.Is(got[0].Err, context.Canceled) || len(s.calls) != 0 {
		t.Errorf("outcome = %+v, reads = %v", got[0], s.calls)
	}
}

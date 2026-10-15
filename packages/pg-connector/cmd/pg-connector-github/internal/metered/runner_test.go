package metered

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var (
	t0    = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	reset = t0.Add(30 * time.Minute)
)

// innerCall is one invocation the fake Inner saw.
type innerCall struct {
	args     []string
	stdin    []byte
	hasStdin bool
}

func (c innerCall) line() string { return strings.Join(c.args, " ") }

// fakeInner is the gh the Runner runs: it records every call and answers from reply.
type fakeInner struct {
	mu    sync.Mutex
	calls []innerCall
	reply func(c innerCall) ([]byte, error)
}

func (f *fakeInner) Run(_ context.Context, args ...string) ([]byte, error) {
	return f.answer(innerCall{args: args})
}

func (f *fakeInner) RunStdin(_ context.Context, stdin []byte, args ...string) ([]byte, error) {
	return f.answer(innerCall{args: args, stdin: stdin, hasStdin: true})
}

func (f *fakeInner) answer(c innerCall) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	reply := f.reply
	f.mu.Unlock()
	if reply == nil {
		return nil, nil
	}
	return reply(c)
}

func (f *fakeInner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// harness is a Runner over a fake Inner and a real governor on a manual clock,
// with the spend and pause observers recorded.
type harness struct {
	t      *testing.T
	clock  *governor.ManualClock
	gov    *governor.Governor
	inner  *fakeInner
	runner *Runner
	events []governor.SpendEvent
	pauses []string // "<bucket> until <time>" per pause start or extension
}

func newHarness(t *testing.T, reply func(c innerCall) ([]byte, error)) *harness {
	t.Helper()
	h := &harness{t: t, clock: governor.NewManualClock(t0), inner: &fakeInner{reply: reply}}
	g, err := governor.New(ratefeed.GovernorConfig(config.Defaults().Settings), governor.Options{
		Clock:   h.clock,
		OnSpend: func(e governor.SpendEvent) { h.events = append(h.events, e) },
		OnPauseChange: func(b governor.Bucket, _ string, until time.Time) {
			if !until.IsZero() {
				h.pauses = append(h.pauses, fmt.Sprintf("%s until %s", b, until.Format(time.RFC3339)))
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.gov = g
	h.runner = &Runner{Inner: h.inner, Governor: g, Feed: ratefeed.New(g, h.inner, h.clock)}
	return h
}

func (h *harness) bucket(b governor.Bucket) governor.BucketState {
	h.t.Helper()
	bs, ok := h.gov.State().Bucket(b)
	if !ok {
		h.t.Fatalf("no state for %s", b)
	}
	return bs
}

// ghInclude builds `gh api --include` output the way gh prints it.
func ghInclude(status string, headers [][2]string, body string) []byte {
	var b strings.Builder
	b.WriteString("HTTP/2.0 " + status + "\n")
	for _, h := range headers {
		b.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}

func rateHeaders(resource string, limit, remaining, used int, extra ...[2]string) [][2]string {
	h := [][2]string{
		{"Content-Type", "application/json; charset=utf-8"},
		{"X-Ratelimit-Limit", fmt.Sprint(limit)},
		{"X-Ratelimit-Remaining", fmt.Sprint(remaining)},
		{"X-Ratelimit-Reset", fmt.Sprint(reset.Unix())},
		{"X-Ratelimit-Resource", resource},
		{"X-Ratelimit-Used", fmt.Sprint(used)},
	}
	return append(h, extra...)
}

// secondaryLimitText is how gh words a secondary rate limit it also calls an
// authentication failure (every HTTP 403 is one to gh).
func secondaryLimitText(args ...string) error {
	return fmt.Errorf("gh %s: exit status 1: gh: You have exceeded a secondary rate limit (HTTP 403): run `gh auth login`: %w",
		strings.Join(args, " "), github.ErrGHAuthInvalid)
}

// forbiddenText is an HTTP 403 that is NOT a rate limit.
func forbiddenText(args ...string) error {
	return fmt.Errorf("gh %s: exit status 1: gh: Resource not accessible by integration (HTTP 403): run `gh auth login`: %w",
		strings.Join(args, " "), github.ErrGHAuthInvalid)
}

// The routing table: every row admits to the stated bucket with the class and
// purpose of WithCall, books its spend (cost, purpose, group, batch size), and
// hands the caller the bytes the Provider expects.
func TestEachRowOfTheRoutingTableIsAdmittedToItsBucketAndBooked(t *testing.T) {
	const (
		graphqlPage = `{"data":{"rateLimit":{"cost":3,"remaining":3987,"resetAt":"2026-10-12T12:30:00Z"},"search":{"nodes":[]}}}`
		costOnly    = `{"data":{"rateLimit":{"cost":2},"repository":{"pullRequest":{"comments":{"nodes":[]}}}}}`
		mutation    = `{"data":{"w0":{"comment":{"id":"C_1"}}}}`
		runsBody    = `{"total_count":0,"workflow_runs":[]}`
		reviewBody  = `{"id":7,"node_id":"PRR_node","state":"PENDING"}`
		searchBody  = `{"total_count":0,"items":[]}`
		searchJSON  = `[{"number":7}]`
	)
	payload := []byte(`{"commit_id":"0123abc","body":"summary"}`)
	cases := []struct {
		name      string
		args      []string
		stdin     []byte // non-nil: the call is RunStdin
		answer    []byte
		wantRun   string // what the Inner is run with
		wantBody  string // what the caller (the Provider) gets
		bucket    governor.Bucket
		spent     int64
		remaining int64 // the bucket's remaining after the call; -1 when no reading is in force
	}{
		{
			name:   "gh api graphql with a folded rateLimit",
			args:   []string{"api", "graphql", "-F", "query=query { rateLimit { cost remaining resetAt } }", "-f", "q=is:pr"},
			answer: []byte(graphqlPage), wantRun: "api graphql -F query=query { rateLimit { cost remaining resetAt } } -f q=is:pr",
			wantBody: graphqlPage, bucket: governor.BucketGraphQL, spent: 3, remaining: 3987,
		},
		{
			name:   "gh api graphql selecting rateLimit { cost } alone books the cost and no reading",
			args:   []string{"api", "graphql", "-F", "query=query { rateLimit { cost } }"},
			answer: []byte(costOnly), wantRun: "api graphql -F query=query { rateLimit { cost } }",
			wantBody: costOnly, bucket: governor.BucketGraphQL, spent: 2, remaining: -1,
		},
		{
			name:   "a GraphQL mutation on stdin books the estimate",
			args:   []string{"api", "graphql", "--input", "-"},
			stdin:  payload,
			answer: []byte(mutation), wantRun: "api graphql --input -",
			wantBody: mutation, bucket: governor.BucketGraphQL, spent: 1, remaining: -1,
		},
		{
			name:     "gh api REST endpoint runs as gh api --include and returns the body only",
			args:     []string{"api", "repos/o/r/actions/runs", "-f", "per_page=10"},
			answer:   ghInclude("200 OK", rateHeaders("core", 5000, 4987, 13), runsBody),
			wantRun:  "api --include repos/o/r/actions/runs -f per_page=10",
			wantBody: runsBody, bucket: governor.BucketRESTCore, spent: 1, remaining: 4987,
		},
		{
			name:     "a REST write with a stdin body (create a pending review)",
			args:     []string{"api", "repos/o/r/pulls/1/reviews", "--method", "POST", "--input", "-"},
			stdin:    payload,
			answer:   ghInclude("201 Created", rateHeaders("core", 5000, 4986, 14), reviewBody),
			wantRun:  "api --include repos/o/r/pulls/1/reviews --method POST --input -",
			wantBody: reviewBody, bucket: governor.BucketRESTCore, spent: 1, remaining: 4986,
		},
		{
			name:     "gh api search/ draws on REST search",
			args:     []string{"api", "search/issues", "-f", "q=is:pr"},
			answer:   ghInclude("200 OK", rateHeaders("search", 30, 29, 1), searchBody),
			wantRun:  "api --include search/issues -f q=is:pr",
			wantBody: searchBody, bucket: governor.BucketRESTSearch, spent: 1, remaining: 29,
		},
		{
			// Admitted at the upper bound (3) but booked at the pages fetched: a
			// one-result answer is one page.
			name:   "gh search prs --limit 250 with a short answer books one search request",
			args:   []string{"search", "prs", "--json", "number", "--limit", "250", "--", "is:open"},
			answer: []byte(searchJSON), wantRun: "search prs --json number --limit 250 -- is:open",
			wantBody: searchJSON, bucket: governor.BucketRESTSearch, spent: 1, remaining: -1,
		},
		{
			name:   "gh search prs without a limit is one request",
			args:   []string{"search", "prs", "--json", "number", "--", "is:open"},
			answer: []byte(searchJSON), wantRun: "search prs --json number -- is:open",
			wantBody: searchJSON, bucket: governor.BucketRESTSearch, spent: 1, remaining: -1,
		},
		{
			name:   "gh pr view is porcelain on REST core",
			args:   []string{"pr", "view", "7", "--repo", "o/r", "--json", "number"},
			answer: []byte(`{"number":7}`), wantRun: "pr view 7 --repo o/r --json number",
			wantBody: `{"number":7}`, bucket: governor.BucketRESTCore, spent: 1, remaining: -1,
		},
		{
			name:   "gh run list is porcelain on REST core",
			args:   []string{"run", "list", "--repo", "o/r", "--json", "status"},
			answer: []byte(`[]`), wantRun: "run list --repo o/r --json status",
			wantBody: `[]`, bucket: governor.BucketRESTCore, spent: 1, remaining: -1,
		},
		{
			name:   "porcelain with a stdin body runs as given",
			args:   []string{"pr", "edit", "7", "--repo", "o/r", "--body-file", "-"},
			stdin:  []byte("new body"),
			answer: []byte(""), wantRun: "pr edit 7 --repo o/r --body-file -",
			wantBody: "", bucket: governor.BucketRESTCore, spent: 1, remaining: -1,
		},
	}
	call := ratefeed.Meta{Class: governor.ClassQuery, Purpose: governor.PurposeQuery, Group: "conversation", BatchSize: 4}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(innerCall) ([]byte, error) { return tc.answer, nil })
			ctx := WithCall(context.Background(), call)
			var (
				out []byte
				err error
			)
			if tc.stdin != nil {
				out, err = h.runner.RunStdin(ctx, tc.stdin, tc.args...)
			} else {
				out, err = h.runner.Run(ctx, tc.args...)
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tc.wantBody {
				t.Errorf("the caller got %q, want %q", out, tc.wantBody)
			}
			// The Inner ran exactly once (no re-admission loop), as gh api --include
			// for REST and as given otherwise, with the stdin only a RunStdin carries.
			if h.inner.count() != 1 {
				t.Fatalf("the Inner ran %d times, want 1", h.inner.count())
			}
			got := h.inner.calls[0]
			if got.line() != tc.wantRun || got.hasStdin != (tc.stdin != nil) || string(got.stdin) != string(tc.stdin) {
				t.Errorf("the Inner ran %q (stdin %v %q), want %q (stdin %q)", got.line(), got.hasStdin, got.stdin, tc.wantRun, tc.stdin)
			}
			// One admission, settled: the bucket's spend, with the class and the
			// purpose of WithCall; nothing left reserved; no other bucket touched.
			bs := h.bucket(tc.bucket)
			if bs.Spent != tc.spent || bs.BackgroundSpent != tc.spent || bs.SpentByPurpose[governor.PurposeQuery] != tc.spent || bs.InFlight != 0 || bs.Remaining != tc.remaining {
				t.Errorf("%s = %+v, want spent %d (as a class 3 query), remaining %d", tc.bucket, bs, tc.spent, tc.remaining)
			}
			for _, other := range governor.Buckets() {
				if other != tc.bucket {
					if o := h.bucket(other); o.Spent != 0 || o.InFlight != 0 {
						t.Errorf("%s was touched: %+v", other, o)
					}
				}
			}
			want := governor.SpendEvent{
				At: t0, Bucket: tc.bucket, Class: governor.ClassQuery, Purpose: governor.PurposeQuery,
				Group: "conversation", BatchSize: 4, Cost: tc.spent, Remaining: tc.remaining, Outcome: ratefeed.OutcomeOK,
			}
			if len(h.events) != 1 || h.events[0] != want {
				t.Errorf("spend events = %+v, want %+v", h.events, want)
			}
		})
	}
}

func TestACallWithNoStatedPurposeIsAdmittedAsInteractive(t *testing.T) {
	for _, args := range [][]string{
		{"api", "graphql", "-F", "query=x"},
		{"api", "repos/o/r"},
		{"search", "prs", "--", "x"},
		{"pr", "view", "1"},
	} {
		h := newHarness(t, func(c innerCall) ([]byte, error) {
			if c.args[0] == "api" && c.args[1] != "graphql" {
				return ghInclude("200 OK", rateHeaders("core", 5000, 4000, 1000), "{}"), nil
			}
			return []byte("{}"), nil
		})
		if _, err := h.runner.Run(context.Background(), args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if len(h.events) != 1 || h.events[0].Class != governor.ClassInteractive || h.events[0].Purpose != governor.PurposeInteractive {
			t.Errorf("%v: spend events = %+v, want one interactive call", args, h.events)
		}
	}
}

func TestWithCallAndCallFromRoundTrip(t *testing.T) {
	def := CallFrom(context.Background())
	if def.Class != governor.ClassInteractive || def.Purpose != governor.PurposeInteractive || def.Group != "" || def.BatchSize != 0 {
		t.Errorf("a context that states nothing = %+v", def)
	}
	m := ratefeed.Meta{Class: governor.ClassInvalidated, Purpose: governor.PurposeWrite, Group: "pending", BatchSize: 1}
	ctx := WithCall(context.Background(), m)
	if got := CallFrom(ctx); got != m {
		t.Errorf("CallFrom = %+v, want %+v", got, m)
	}
	// An inner statement replaces an outer one for the calls made under it.
	inner := ratefeed.Meta{Class: governor.ClassFill, Purpose: governor.PurposeFill}
	if got := CallFrom(WithCall(ctx, inner)); got != inner {
		t.Errorf("nested CallFrom = %+v, want %+v", got, inner)
	}
}

// A refusal is the governor's decision: it is returned as the *governor.Refusal
// it is, nothing runs, and nothing is booked, whichever route the call is on and
// whichever rule stopped it.
func TestARefusalRunsNothingAndBooksNothingOnEveryRoute(t *testing.T) {
	routes := []struct {
		name   string
		args   []string
		stdin  []byte
		bucket governor.Bucket
	}{
		{"gh api graphql", []string{"api", "graphql", "-F", "query=x"}, nil, governor.BucketGraphQL},
		{"a GraphQL mutation", []string{"api", "graphql", "--input", "-"}, []byte("{}"), governor.BucketGraphQL},
		{"gh api REST", []string{"api", "repos/o/r/actions/runs"}, nil, governor.BucketRESTCore},
		{"a REST write", []string{"api", "repos/o/r/pulls/1/reviews", "--method", "POST", "--input", "-"}, []byte("{}"), governor.BucketRESTCore},
		{"gh api search/", []string{"api", "search/issues", "-f", "q=x"}, nil, governor.BucketRESTSearch},
		{"gh search prs", []string{"search", "prs", "--limit", "100", "--", "x"}, nil, governor.BucketRESTSearch},
		{"porcelain", []string{"pr", "view", "1"}, nil, governor.BucketRESTCore},
	}
	causes := []struct {
		name    string
		class   governor.Class
		kind    governor.RefusalKind
		arrange func(h *harness, b governor.Bucket)
	}{
		{"the cap is spent", governor.ClassInteractive, governor.RefusedCap, func(h *harness, b governor.Bucket) {
			spend(h, b, governor.ClassInteractive, h.bucket(b).Cap)
		}},
		{"the reserve is reached", governor.ClassInteractive, governor.RefusedReserve, func(h *harness, b governor.Bucket) {
			if err := h.gov.Observe(governor.Reading{Bucket: b, Remaining: 0, ResetAt: reset}); err != nil {
				h.t.Fatal(err)
			}
		}},
		{"the background share is spent", governor.ClassKeepAlive, governor.RefusedBackgroundShare, func(h *harness, b governor.Bucket) {
			spend(h, b, governor.ClassQuery, h.bucket(b).BackgroundCap)
		}},
		{"the background is paused for a rate limit", governor.ClassKeepAlive, governor.RefusedPaused, func(h *harness, _ governor.Bucket) {
			h.gov.PauseAll(time.Minute, "secondary rate limit (HTTP 403, retry after 1m0s)")
		}},
	}
	for _, rt := range routes {
		for _, cause := range causes {
			t.Run(rt.name+" when "+cause.name, func(t *testing.T) {
				h := newHarness(t, func(innerCall) ([]byte, error) { return []byte("{}"), nil })
				cause.arrange(h, rt.bucket)
				before, eventsBefore, pausesBefore := h.bucket(rt.bucket), len(h.events), len(h.pauses)

				ctx := WithCall(context.Background(), ratefeed.Meta{Class: cause.class, Purpose: governor.PurposeRefresh})
				var (
					out []byte
					err error
				)
				if rt.stdin != nil {
					out, err = h.runner.RunStdin(ctx, rt.stdin, rt.args...)
				} else {
					out, err = h.runner.Run(ctx, rt.args...)
				}
				var refusal *governor.Refusal
				if !errors.As(err, &refusal) || refusal.Kind != cause.kind || refusal.Bucket != rt.bucket {
					t.Fatalf("err = %v, want a %s refusal of %s", err, cause.kind, rt.bucket)
				}
				var typed *ratefeed.RateLimitError
				if errors.As(err, &typed) {
					t.Errorf("a refusal was reclassified as a rate-limit answer: %v", err)
				}
				if len(out) != 0 || h.inner.count() != 0 {
					t.Errorf("out %q, the Inner ran %d times: nothing may run for a refused call", out, h.inner.count())
				}
				after := h.bucket(rt.bucket)
				if after.Spent != before.Spent || after.InFlight != 0 || len(h.events) != eventsBefore || len(h.pauses) != pausesBefore {
					t.Errorf("a refused call booked something: before %+v after %+v, %d new events, %d new pauses",
						before, after, len(h.events)-eventsBefore, len(h.pauses)-pausesBefore)
				}
				if after.Paused != before.Paused || !after.PausedUntil.Equal(before.PausedUntil) {
					t.Errorf("a refusal moved the pause: before %v until %v, after %v until %v",
						before.Paused, before.PausedUntil, after.Paused, after.PausedUntil)
				}
			})
		}
	}
}

// spend books cost on bucket as class, as a call that has already happened.
func spend(h *harness, b governor.Bucket, class governor.Class, cost int64) {
	h.t.Helper()
	if err := h.gov.Record(governor.Spend{Bucket: b, Class: class, Purpose: governor.PurposeQuery, Cost: cost}); err != nil {
		h.t.Fatal(err)
	}
}

// A rate-limit answer recognized by its text, on Run and on RunStdin, becomes a
// secondary *ratefeed.RateLimitError that does not wrap the authentication
// sentinel, and pauses the background classes for FallbackPause once per error.
func TestARateLimitAnswerPausesTheBackgroundOncePerErrorAndIsNotAnAuthFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		stdin []byte
		// reply is the Inner's answer to the call.
		reply func(c innerCall) ([]byte, error)
	}{
		{
			name: "gh api graphql on Run", args: []string{"api", "graphql", "-F", "query=x"},
			reply: func(c innerCall) ([]byte, error) { return nil, secondaryLimitText(c.args...) },
		},
		{
			name: "a GraphQL mutation on RunStdin", args: []string{"api", "graphql", "--input", "-"}, stdin: []byte("{}"),
			reply: func(c innerCall) ([]byte, error) { return nil, secondaryLimitText(c.args...) },
		},
		{
			name: "porcelain on Run", args: []string{"pr", "view", "1"},
			reply: func(c innerCall) ([]byte, error) { return nil, secondaryLimitText(c.args...) },
		},
		{
			name: "gh search prs on Run", args: []string{"search", "prs", "--", "x"},
			reply: func(c innerCall) ([]byte, error) { return nil, secondaryLimitText(c.args...) },
		},
		{
			// A REST answer whose 403 carries no Retry-After: the Feed sees an ordinary
			// error, and the text of gh's error is what says it is a rate limit.
			name: "a REST call whose 403 carries no Retry-After on Run", args: []string{"api", "repos/o/r/actions/runs"},
			reply: func(c innerCall) ([]byte, error) {
				return ghInclude("403 Forbidden", rateHeaders("core", 5000, 4000, 1000), `{"message":"You have exceeded a secondary rate limit."}`),
					secondaryLimitText(c.args...)
			},
		},
		{
			name: "a REST write whose 403 carries no Retry-After on RunStdin",
			args: []string{"api", "repos/o/r/pulls/1/reviews", "--method", "POST", "--input", "-"}, stdin: []byte("{}"),
			reply: func(c innerCall) ([]byte, error) {
				return ghInclude("403 Forbidden", rateHeaders("core", 5000, 4000, 1000), `{"message":"You have exceeded a secondary rate limit."}`),
					secondaryLimitText(c.args...)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.reply)
			h.runner.FallbackPause = 45 * time.Second
			ctx := WithCall(context.Background(), ratefeed.Meta{Class: governor.ClassInvalidated, Purpose: governor.PurposeRefresh})
			run := func(ctx context.Context) error {
				var err error
				if tc.stdin != nil {
					_, err = h.runner.RunStdin(ctx, tc.stdin, tc.args...)
				} else {
					_, err = h.runner.Run(ctx, tc.args...)
				}
				return err
			}

			err := run(ctx)
			var rl *ratefeed.RateLimitError
			if !errors.As(err, &rl) || !rl.Secondary || rl.RetryAfter != 45*time.Second {
				t.Fatalf("err = %v, want a secondary *ratefeed.RateLimitError for the fallback pause of 45s", err)
			}
			if errors.Is(err, github.ErrGHAuthInvalid) {
				t.Error("a rate-limit answer was reported as an authentication failure")
			}
			// One PauseAll for the one error: every bucket, until now + 45s.
			if len(h.pauses) != 3 {
				t.Fatalf("pause starts = %v, want one per bucket for one error", h.pauses)
			}
			for _, b := range governor.Buckets() {
				if bs := h.bucket(b); !bs.Paused || !bs.PausedUntil.Equal(t0.Add(45*time.Second)) || !strings.Contains(bs.PauseReason, "rate limit") {
					t.Errorf("%s = %+v", b, bs)
				}
			}

			// The same error passed through Classify again pauses nothing more.
			if again := Classify(h.gov, h.runner.FallbackPause, err); again != err {
				t.Errorf("Classify of an already-typed error = %v, want it untouched", again)
			}
			if len(h.pauses) != 3 {
				t.Errorf("an already-typed error paused again: %v", h.pauses)
			}

			// A second answer 10s later is a second error and extends the pause once more.
			h.clock.Advance(10 * time.Second)
			if err := run(ctx); !errors.As(err, &rl) {
				t.Fatalf("second err = %v", err)
			}
			if len(h.pauses) != 6 {
				t.Errorf("pause starts = %v, want three more for the second error", h.pauses)
			}

			// While the pause holds, a background call is refused and a class 1 or 2 call is not.
			calls := h.inner.count()
			bg := WithCall(context.Background(), ratefeed.Meta{Class: governor.ClassKeepAlive, Purpose: governor.PurposeRefresh})
			var refusal *governor.Refusal
			if err := run(bg); !errors.As(err, &refusal) || refusal.Kind != governor.RefusedPaused {
				t.Errorf("a class 4 call during the pause: %v, want a paused refusal", err)
			}
			if h.inner.count() != calls {
				t.Error("a refused background call reached gh")
			}
			interactive := WithCall(context.Background(), ratefeed.Meta{Class: governor.ClassInteractive, Purpose: governor.PurposeInteractive})
			if err := run(interactive); errors.As(err, &refusal) {
				t.Errorf("a class 1 call was refused during the pause: %v", err)
			}
			if h.inner.count() != calls+1 {
				t.Error("the class 1 call did not reach gh")
			}
		})
	}
}

// An HTTP 403 whose text is not a rate limit stays what gh called it, an
// authentication failure, and pauses nothing.
func TestAnOrdinary403StaysAnAuthenticationFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		stdin []byte
		reply func(c innerCall) ([]byte, error)
	}{
		{
			name: "GraphQL", args: []string{"api", "graphql", "-F", "query=x"},
			reply: func(c innerCall) ([]byte, error) { return nil, forbiddenText(c.args...) },
		},
		{
			name: "porcelain", args: []string{"pr", "view", "1"},
			reply: func(c innerCall) ([]byte, error) { return nil, forbiddenText(c.args...) },
		},
		{
			name: "REST", args: []string{"api", "repos/o/r"},
			reply: func(c innerCall) ([]byte, error) {
				return ghInclude("403 Forbidden", rateHeaders("core", 5000, 4000, 1000), `{"message":"Resource not accessible by integration"}`),
					forbiddenText(c.args...)
			},
		},
		{
			name: "a REST write on stdin", args: []string{"api", "repos/o/r/pulls/1/reviews", "--method", "POST", "--input", "-"}, stdin: []byte("{}"),
			reply: func(c innerCall) ([]byte, error) {
				return ghInclude("403 Forbidden", rateHeaders("core", 5000, 4000, 1000), `{"message":"Resource not accessible by integration"}`),
					forbiddenText(c.args...)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.reply)
			var (
				out []byte
				err error
			)
			if tc.stdin != nil {
				out, err = h.runner.RunStdin(context.Background(), tc.stdin, tc.args...)
			} else {
				out, err = h.runner.Run(context.Background(), tc.args...)
			}
			if !errors.Is(err, github.ErrGHAuthInvalid) {
				t.Fatalf("err = %v, want gh's authentication failure untouched", err)
			}
			var rl *ratefeed.RateLimitError
			if errors.As(err, &rl) || len(h.pauses) != 0 {
				t.Errorf("an ordinary 403 became a rate limit (%v) or paused (%v)", err, h.pauses)
			}
			if got := WireError(err); !errors.Is(got, scriptout.ErrUnauthenticated) {
				t.Errorf("WireError = %v, want unauthenticated", got)
			}
			// A REST error status still hands back its body beside the error.
			if strings.Contains(tc.name, "REST") && string(out) != `{"message":"Resource not accessible by integration"}` {
				t.Errorf("body = %q, want the error status's body beside the error", out)
			}
		})
	}
}

// The Feed pauses a REST secondary limit itself, from Retry-After; the Runner must
// neither replace its error nor pause again with the fallback.
func TestARESTSecondaryLimitWithRetryAfterIsTheFeedsAndNotPausedTwice(t *testing.T) {
	headers := rateHeaders("core", 5000, 4900, 100, [2]string{"Retry-After", "30"})
	h := newHarness(t, func(c innerCall) ([]byte, error) {
		return ghInclude("403 Forbidden", headers, `{"message":"You have exceeded a secondary rate limit."}`), secondaryLimitText(c.args...)
	})
	h.runner.FallbackPause = 90 * time.Second
	out, err := h.runner.Run(context.Background(), "api", "repos/o/r/actions/runs")
	var rl *ratefeed.RateLimitError
	if !errors.As(err, &rl) || !rl.Secondary || rl.RetryAfter != 30*time.Second || rl.StatusCode != 403 || errors.Is(err, github.ErrGHAuthInvalid) {
		t.Fatalf("err = %v, want the Feed's secondary limit for the stated 30s", err)
	}
	if !strings.Contains(string(out), "secondary rate limit") {
		t.Errorf("body = %q, want the error status's body beside the error", out)
	}
	if len(h.pauses) != 3 {
		t.Errorf("pause starts = %v, want the Feed's one per bucket and nothing more", h.pauses)
	}
	if bs := h.bucket(governor.BucketRESTCore); !bs.PausedUntil.Equal(t0.Add(30 * time.Second)) {
		t.Errorf("rest_core paused until %v, want the stated 30s and not the 90s fallback", bs.PausedUntil)
	}
}

// A call that never reached GitHub books nothing: gh refused to start without a
// credential, or the REST output held no HTTP response.
func TestACallThatNeverReachedGitHubReleasesItsReservation(t *testing.T) {
	noCredential := func(c innerCall) ([]byte, error) {
		return nil, fmt.Errorf("gh %s: no usable GitHub credential; run `gh auth login`: %w", strings.Join(c.args, " "), github.ErrGHAuthInvalid)
	}
	for _, args := range [][]string{
		{"api", "graphql", "-F", "query=x"},
		{"api", "repos/o/r"},
		{"search", "prs", "--", "x"},
		{"pr", "view", "1"},
	} {
		h := newHarness(t, noCredential)
		_, err := h.runner.Run(context.Background(), args...)
		if !errors.Is(err, github.ErrGHAuthInvalid) {
			t.Errorf("%v: err = %v", args, err)
		}
		for _, b := range governor.Buckets() {
			if bs := h.bucket(b); bs.Spent != 0 || bs.InFlight != 0 {
				t.Errorf("%v: %s = %+v, want nothing spent or reserved", args, b, bs)
			}
		}
		if len(h.events) != 0 {
			t.Errorf("%v: spend events = %+v", args, h.events)
		}
	}
}

// Failures that did reach GitHub still book the call: a request that reached it
// may have been charged.
func TestAFailureThatReachedGitHubStillBooksTheEstimate(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		bucket  governor.Bucket
		cost    int64
		outcome string
		err     error
	}{
		{[]string{"api", "graphql", "-F", "query=x"}, governor.BucketGraphQL, 1, ratefeed.OutcomeError, errors.New("gh: error connecting to api.github.com")},
		{[]string{"pr", "view", "1"}, governor.BucketRESTCore, 1, ratefeed.OutcomeError, errors.New("gh: error connecting to api.github.com")},
		{[]string{"search", "prs", "--limit", "200", "--", "x"}, governor.BucketRESTSearch, 2, ratefeed.OutcomeError, errors.New("gh: error connecting to api.github.com")},
		{[]string{"search", "prs", "--limit", "200", "--", "x"}, governor.BucketRESTSearch, 2, ratefeed.OutcomeRateLimited, secondaryLimitText("search", "prs")},
	} {
		h := newHarness(t, func(innerCall) ([]byte, error) { return []byte("partial"), tc.err })
		if _, err := h.runner.Run(context.Background(), tc.args...); err == nil {
			t.Fatalf("%v: no error", tc.args)
		}
		if bs := h.bucket(tc.bucket); bs.Spent != tc.cost || bs.InFlight != 0 {
			t.Errorf("%v: %s = %+v, want %d spent", tc.args, tc.bucket, bs, tc.cost)
		}
		if len(h.events) != 1 || h.events[0].Outcome != tc.outcome || h.events[0].Cost != tc.cost {
			t.Errorf("%v: spend events = %+v, want one %q of cost %d", tc.args, h.events, tc.outcome, tc.cost)
		}
	}
}

// The Feed MUST be built over the raw Inner. One built over the Runner itself
// would admit every REST call again from inside its own admission, forever; the
// Runner turns that wiring mistake into an error and gives the reservation back.
func TestAFeedBuiltOverTheRunnerItselfFailsInsteadOfRecursing(t *testing.T) {
	h := newHarness(t, nil)
	loop := &Runner{Inner: h.inner, Governor: h.gov}
	loop.Feed = ratefeed.New(h.gov, loop, h.clock)
	calls := map[string]func() error{
		"Run": func() error {
			_, err := loop.Run(context.Background(), "api", "repos/o/r")
			return err
		},
		"RunStdin": func() error {
			_, err := loop.RunStdin(context.Background(), []byte("{}"), "api", "repos/o/r/pulls/1/reviews", "--input", "-")
			return err
		},
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, errFeedOverRunner) {
			t.Errorf("%s: err = %v, want the Feed-over-Runner error", name, err)
		}
	}
	if h.inner.count() != 0 || len(h.events) != 0 {
		t.Errorf("gh ran %d times, %d spend events", h.inner.count(), len(h.events))
	}
	if bs := h.bucket(governor.BucketRESTCore); bs.Spent != 0 || bs.InFlight != 0 {
		t.Errorf("rest_core = %+v: the reservation must be given back", bs)
	}
}

func TestARunnerWithoutItsPartsFailsInsteadOfPanicking(t *testing.T) {
	h := newHarness(t, nil)
	for name, r := range map[string]*Runner{
		"no Inner":    {Governor: h.gov, Feed: h.runner.Feed},
		"no Governor": {Inner: h.inner, Feed: h.runner.Feed},
		"no Feed":     {Inner: h.inner, Governor: h.gov},
		"nil Runner":  nil,
	} {
		if _, err := r.Run(context.Background(), "pr", "view", "1"); err == nil {
			t.Errorf("%s: want an error", name)
		}
		if _, err := r.RunStdin(context.Background(), []byte("{}"), "pr", "edit", "1"); err == nil {
			t.Errorf("%s: want an error from RunStdin", name)
		}
	}
	if h.inner.count() != 0 {
		t.Error("gh ran for a Runner that was not whole")
	}
}

// searchOf is the JSON array a `gh search prs` prints for n results.
func searchOf(n int) string {
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprintf(`{"number":%d}`, i+1)
	}
	return "[" + strings.Join(items, ",") + "]"
}

// A `gh search prs` is admitted at its upper bound, ceil(limit / 100), and booked
// at the pages its answer shows were fetched: one for a result under 100, the
// full bound for a result that fills the limit, and never more than the bound.
func TestASearchBooksThePagesActuallyFetchedNeverMoreThanItsAdmission(t *testing.T) {
	for _, tc := range []struct {
		name    string
		limit   string
		results int
		want    int64
	}{
		{"an empty answer is one page", "1000", 0, 1},
		{"a short answer is one page", "1000", 7, 1},
		{"99 results are one page", "1000", 99, 1},
		{"a full first page needs the page that ends the listing", "1000", 100, 2},
		{"250 results are three pages", "1000", 250, 3},
		{"1000 results still book ten pages", "1000", 1000, 10},
		{"more results than the limit never book past the admission", "250", 1000, 3},
		{"a limit of 100 never books past one request", "100", 100, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(innerCall) ([]byte, error) { return []byte(searchOf(tc.results)), nil })
			if _, err := h.runner.Run(context.Background(), "search", "prs", "--json", "number", "--limit", tc.limit, "--", "is:pr"); err != nil {
				t.Fatal(err)
			}
			bs := h.bucket(governor.BucketRESTSearch)
			if bs.Spent != tc.want || bs.InFlight != 0 {
				t.Errorf("rest_search = %+v, want %d spent and nothing reserved", bs, tc.want)
			}
			if len(h.events) != 1 || h.events[0].Cost != tc.want {
				t.Errorf("spend events = %+v, want one of cost %d", h.events, tc.want)
			}
		})
	}
}

// An answer that is not a JSON array of results cannot be counted, so the
// admission estimate is what is booked.
func TestASearchWhoseAnswerCannotBeCountedBooksItsEstimate(t *testing.T) {
	h := newHarness(t, func(innerCall) ([]byte, error) { return []byte("not json"), nil })
	if _, err := h.runner.Run(context.Background(), "search", "prs", "--limit", "300", "--", "x"); err != nil {
		t.Fatal(err)
	}
	if bs := h.bucket(governor.BucketRESTSearch); bs.Spent != 3 || bs.InFlight != 0 {
		t.Errorf("rest_search = %+v, want the estimate of 3 spent", bs)
	}
}

// Admission is still at the upper bound: a limit whose bound does not fit under
// the cap is refused before gh runs, whatever the answer would have been.
func TestASearchIsStillAdmittedAtItsUpperBound(t *testing.T) {
	h := newHarness(t, func(innerCall) ([]byte, error) { return []byte("[]"), nil })
	cap := config.Defaults().Settings.RESTSearchPerMinute
	// Leave 5 of the window: below the ten pages a limit of 1000 is admitted at.
	if err := h.gov.Record(governor.Spend{Bucket: governor.BucketRESTSearch, Class: governor.ClassQuery, Purpose: governor.PurposeQuery, Cost: int64(cap) - 5}); err != nil {
		t.Fatal(err)
	}
	h.events = nil
	_, err := h.runner.Run(context.Background(), "search", "prs", "--limit", "1000", "--", "x")
	var refusal *governor.Refusal
	if !errors.As(err, &refusal) || h.inner.count() != 0 {
		t.Fatalf("err = %v after %d gh calls, want a refusal with nothing run", err, h.inner.count())
	}
}

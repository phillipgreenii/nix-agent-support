package ratefeed

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// fakeRunner is a Runner that returns canned output and records its calls.
type fakeRunner struct {
	out   []byte
	err   error
	calls [][]string
}

func (f *fakeRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	return f.out, f.err
}

func newFeed(t *testing.T, out []byte, runErr error) (*Feed, *governor.Governor, *governor.ManualClock, *fakeRunner, *[]governor.SpendEvent) {
	t.Helper()
	clk := governor.NewManualClock(t0)
	var events []governor.SpendEvent
	g, err := governor.New(GovernorConfig(config.Defaults().Settings), governor.Options{
		Clock: clk, OnSpend: func(e governor.SpendEvent) { events = append(events, e) },
	})
	if err != nil {
		t.Fatal(err)
	}
	run := &fakeRunner{out: out, err: runErr}
	return New(g, run, clk), g, clk, run, &events
}

func bucketState(t *testing.T, g *governor.Governor, b governor.Bucket) governor.BucketState {
	t.Helper()
	bs, ok := g.State().Bucket(b)
	if !ok {
		t.Fatalf("no state for %s", b)
	}
	return bs
}

func TestGovernorConfigFromTheDaemonSettings(t *testing.T) {
	cfg := config.Defaults()
	got := GovernorConfig(cfg.Settings)
	if got.GraphQL.Cap != 1500 || got.GraphQL.Reserve != 1000 || got.RESTCore.Cap != 3000 || got.RESTSearch.Cap != 20 || got.BackgroundShare != 0.9 {
		t.Errorf("defaults mapped to %+v", got)
	}
	cfg.GraphQLPointsPerHour = config.CutoverGraphQLPointsPerHour
	if err := GovernorConfig(cfg.Settings).Validate(); err != nil {
		t.Errorf("the cutover settings are not a valid governor config: %v", err)
	}
	// The three numbers the design names, through the real mapping.
	for _, tc := range []struct {
		bucket governor.Bucket
		cap    int
		bg     int64
	}{
		{governor.BucketGraphQL, 4000, 3600},
		{governor.BucketGraphQL, 1500, 1350},
		{governor.BucketRESTCore, 3000, 2700},
	} {
		s := cfg.Settings
		s.GraphQLPointsPerHour, s.RESTRequestsPerHour = 1500, 3000
		if tc.bucket == governor.BucketGraphQL {
			s.GraphQLPointsPerHour = tc.cap
		}
		g, err := governor.New(GovernorConfig(s), governor.Options{Clock: governor.NewManualClock(t0)})
		if err != nil {
			t.Fatal(err)
		}
		if bs := bucketState(t, g, tc.bucket); bs.BackgroundCap != tc.bg {
			t.Errorf("%s cap %d: background cap %d, want %d", tc.bucket, tc.cap, bs.BackgroundCap, tc.bg)
		}
	}
}

func TestRESTRunsThroughGhAPIIncludeAndFeedsTheReading(t *testing.T) {
	out := ghInclude("200 OK", rateHeaders("core", 5000, 4987, 13), `{"total_count":0,"workflow_runs":[]}`)
	f, g, _, run, events := newFeed(t, out, nil)
	resp, err := f.REST(context.Background(), RESTCall{
		Meta: Meta{Class: governor.ClassKeepAlive, Purpose: governor.PurposeRefresh, Group: "runs", BatchSize: 1},
		Args: []string{"repos/example-org/example-repo/actions/runs", "-f", "per_page=10"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(run.calls) != 1 || strings.Join(run.calls[0], " ") != "api --include repos/example-org/example-repo/actions/runs -f per_page=10" {
		t.Fatalf("gh calls = %v", run.calls)
	}
	if resp.StatusCode != 200 || string(resp.Body) != `{"total_count":0,"workflow_runs":[]}` {
		t.Errorf("response = %+v", resp)
	}
	bs := bucketState(t, g, governor.BucketRESTCore)
	if bs.Spent != 1 || bs.BackgroundSpent != 1 || bs.Remaining != 4987 || bs.Limit != 5000 || !bs.ResetAt.Equal(reset) || bs.SpentByPurpose[governor.PurposeRefresh] != 1 {
		t.Errorf("rest_core state = %+v", bs)
	}
	if bs.InFlight != 0 {
		t.Errorf("reservation left behind: %+v", bs)
	}
	want := governor.SpendEvent{
		At: t0, Bucket: governor.BucketRESTCore, Class: governor.ClassKeepAlive, Purpose: governor.PurposeRefresh,
		Group: "runs", BatchSize: 1, Cost: 1, Remaining: 4987, Outcome: OutcomeOK,
	}
	if len(*events) != 1 || (*events)[0] != want {
		t.Errorf("spend events = %+v, want %+v", *events, want)
	}
}

func TestRESTSearchDrawsOnTheSearchBucket(t *testing.T) {
	out := ghInclude("200 OK", rateHeaders("search", 30, 29, 1), `{"total_count":0,"items":[]}`)
	f, g, _, _, _ := newFeed(t, out, nil)
	if _, err := f.REST(context.Background(), RESTCall{
		Meta: Meta{Class: governor.ClassInteractive, Purpose: governor.PurposePassthrough},
		Args: []string{"search/issues", "-f", "q=is:pr"},
	}); err != nil {
		t.Fatal(err)
	}
	search := bucketState(t, g, governor.BucketRESTSearch)
	core := bucketState(t, g, governor.BucketRESTCore)
	if search.Spent != 1 || search.Remaining != 29 || search.Limit != 30 || core.Spent != 0 {
		t.Errorf("search %+v, core %+v", search, core)
	}
}

func TestASecondaryLimitPausesTheBackgroundForExactlyTheStatedTime(t *testing.T) {
	// gh prints the response and exits non-zero; its own classifier calls every
	// HTTP 403 an auth failure, which this answer must not become.
	authish := fmt.Errorf("gh api: gh: You have exceeded a secondary rate limit (HTTP 403): run `gh auth login`: %w", github.ErrGHAuthInvalid)
	headers := rateHeaders("core", 5000, 4900, 100, [2]string{"Retry-After", "60"})
	out := ghInclude("403 Forbidden", headers, `{"message":"You have exceeded a secondary rate limit."}`)
	f, g, clk, _, _ := newFeed(t, out, authish)

	var pauses []string
	g2, err := governor.New(GovernorConfig(config.Defaults().Settings), governor.Options{
		Clock: clk,
		OnPauseChange: func(b governor.Bucket, reason string, until time.Time) {
			pauses = append(pauses, fmt.Sprint(b, " ", until.IsZero()))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f = New(g2, f.gh, clk)
	g = g2

	resp, err := f.REST(context.Background(), RESTCall{Meta: Meta{Class: governor.ClassQuery, Purpose: governor.PurposeQuery}, Args: []string{"repos/o/r/actions/runs"}})
	var rl *RateLimitError
	if !errors.As(err, &rl) || !rl.Secondary || rl.RetryAfter != 60*time.Second || rl.StatusCode != 403 {
		t.Fatalf("err = %v, want a secondary *RateLimitError for 60s", err)
	}
	if errors.Is(err, github.ErrGHAuthInvalid) {
		t.Error("a secondary rate limit was reported as an authentication failure")
	}
	if resp == nil || resp.StatusCode != 403 {
		t.Errorf("response %+v was not returned beside the error", resp)
	}
	if len(pauses) != 3 {
		t.Errorf("pause starts = %v, want one per bucket", pauses)
	}

	// Background classes are held for the stated 60s on every bucket; 1 and 2 are not.
	for _, b := range governor.Buckets() {
		bs := bucketState(t, g, b)
		if !bs.Paused || !bs.PausedUntil.Equal(t0.Add(60*time.Second)) || !strings.Contains(bs.PauseReason, "secondary rate limit") {
			t.Errorf("%s state = %+v", b, bs)
		}
		if err := g.Check(governor.Request{Bucket: b, Class: governor.ClassFill, Cost: 1}); !errors.Is(err, governor.ErrRefused) {
			t.Errorf("%s: background admitted during the pause: %v", b, err)
		}
		if err := g.Check(governor.Request{Bucket: b, Class: governor.ClassInvalidated, Cost: 1}); err != nil {
			t.Errorf("%s: class 2 refused during the pause: %v", b, err)
		}
	}
	clk.Advance(60*time.Second - time.Millisecond)
	if err := g.Check(governor.Request{Bucket: governor.BucketRESTCore, Class: governor.ClassQuery, Cost: 1}); err == nil {
		t.Error("the pause ended a millisecond early")
	}
	clk.Advance(time.Millisecond)
	if err := g.Check(governor.Request{Bucket: governor.BucketRESTCore, Class: governor.ClassQuery, Cost: 1}); err != nil {
		t.Errorf("the pause outlasted the stated time: %v", err)
	}
	// The limited call itself was a request that cost one and carried a reading.
	if bs := bucketState(t, g, governor.BucketRESTCore); bs.Spent != 1 || bs.Remaining != 4900 {
		t.Errorf("rest_core after the limit = %+v", bs)
	}
}

func TestAPrimaryLimitIsNotASecondaryOne(t *testing.T) {
	out := ghInclude("403 Forbidden", rateHeaders("core", 5000, 0, 5000), `{"message":"API rate limit exceeded"}`)
	f, g, _, _, _ := newFeed(t, out, fmt.Errorf("gh api: (HTTP 403): %w", github.ErrGHAuthInvalid))
	_, err := f.REST(context.Background(), RESTCall{Meta: Meta{Class: governor.ClassInteractive, Purpose: governor.PurposeInteractive}, Args: []string{"repos/o/r"}})
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.Secondary || !rl.ResetAt.Equal(reset) || errors.Is(err, github.ErrGHAuthInvalid) {
		t.Fatalf("err = %v (%+v), want a primary *RateLimitError resetting at %v", err, rl, reset)
	}
	if bs := bucketState(t, g, governor.BucketRESTCore); bs.Paused || bs.Remaining != 0 {
		t.Errorf("a primary limit must not pause (the reading holds every class): %+v", bs)
	}
	// Nothing remains, so nothing more is admitted, whatever the class.
	for c := governor.ClassInteractive; c <= governor.ClassFill; c++ {
		err := g.Check(governor.Request{Bucket: governor.BucketRESTCore, Class: c, Cost: 1})
		var r *governor.Refusal
		if !errors.As(err, &r) || r.Kind != governor.RefusedReserve || !r.RetryAt.Equal(reset) {
			t.Errorf("class %v: %v", c, err)
		}
	}
}

func TestAnOrdinary403KeepsGhsOwnClassification(t *testing.T) {
	out := ghInclude("403 Forbidden", rateHeaders("core", 5000, 4000, 1000), `{"message":"Resource not accessible by integration"}`)
	runErr := fmt.Errorf("gh api: (HTTP 403): %w", github.ErrGHAuthInvalid)
	f, g, _, _, events := newFeed(t, out, runErr)
	resp, err := f.REST(context.Background(), RESTCall{Meta: Meta{Class: governor.ClassInteractive}, Args: []string{"repos/o/r"}})
	if !errors.Is(err, github.ErrGHAuthInvalid) || resp == nil || resp.StatusCode != 403 {
		t.Errorf("resp %+v err %v: an ordinary 403 must pass through as gh classified it", resp, err)
	}
	var rl *RateLimitError
	if errors.As(err, &rl) {
		t.Error("an ordinary 403 became a rate-limit error")
	}
	if bs := bucketState(t, g, governor.BucketRESTCore); bs.Paused || bs.Spent != 1 || (*events)[0].Outcome != OutcomeError {
		t.Errorf("state %+v events %+v", bs, *events)
	}
}

func TestACallThatNeverReachedGitHubSpendsNothing(t *testing.T) {
	f, g, _, _, events := newFeed(t, nil, errors.New("gh api repos/o/r: error connecting to api.github.com"))
	resp, err := f.REST(context.Background(), RESTCall{Meta: Meta{Class: governor.ClassQuery}, Args: []string{"repos/o/r"}})
	if err == nil || resp != nil || !strings.Contains(err.Error(), "error connecting") {
		t.Errorf("resp %+v err %v", resp, err)
	}
	if bs := bucketState(t, g, governor.BucketRESTCore); bs.Spent != 0 || bs.InFlight != 0 {
		t.Errorf("state = %+v: the reservation must be given back", bs)
	}
	if len(*events) != 0 {
		t.Errorf("events = %+v", *events)
	}
	// gh printed something that is not an HTTP response and exited 0.
	f, _, _, _, _ = newFeed(t, []byte("not a response"), nil)
	if _, err := f.REST(context.Background(), RESTCall{Meta: Meta{Class: governor.ClassQuery}, Args: []string{"repos/o/r"}}); !errors.Is(err, ErrNoResponse) {
		t.Errorf("err = %v, want ErrNoResponse", err)
	}
}

func TestRESTIsRefusedWithoutRunningGhWhenTheGovernorSaysNo(t *testing.T) {
	out := ghInclude("200 OK", rateHeaders("core", 5000, 4000, 1000), "{}")
	f, g, _, run, _ := newFeed(t, out, nil)
	if err := g.Record(governor.Spend{Bucket: governor.BucketRESTCore, Class: governor.ClassQuery, Purpose: governor.PurposeQuery, Cost: 2700}); err != nil {
		t.Fatal(err)
	}
	_, err := f.REST(context.Background(), RESTCall{Meta: Meta{Class: governor.ClassKeepAlive}, Args: []string{"repos/o/r"}})
	var r *governor.Refusal
	if !errors.As(err, &r) || r.Kind != governor.RefusedBackgroundShare || !strings.Contains(r.Reason, "rest_core background share") {
		t.Fatalf("err = %v", err)
	}
	if len(run.calls) != 0 {
		t.Errorf("gh ran %d times for a refused call", len(run.calls))
	}
	// Class 1 is still admitted up to the cap.
	if _, err := f.REST(context.Background(), RESTCall{Meta: Meta{Class: governor.ClassInteractive}, Args: []string{"repos/o/r"}}); err != nil {
		t.Errorf("class 1 refused with room left: %v", err)
	}
}

func TestSettleGraphQLBooksTheFoldedCostAndReading(t *testing.T) {
	f, g, _, _, events := newFeed(t, nil, nil)
	meta := Meta{Class: governor.ClassQuery, Purpose: governor.PurposeQuery, Group: "membership", BatchSize: 1}
	admit := func(est int64) *governor.Ticket {
		tk, err := g.Admit(governor.Request{Bucket: governor.BucketGraphQL, Class: meta.Class, Cost: est})
		if err != nil {
			t.Fatal(err)
		}
		return tk
	}

	// The response's own cost and reading replace the estimate.
	body := []byte(`{"data":{"rateLimit":{"cost":3,"remaining":1400,"resetAt":"2026-10-12T12:30:00Z"},"search":{"nodes":[]}}}`)
	if err := f.SettleGraphQL(admit(1), meta, body, nil); err != nil {
		t.Fatal(err)
	}
	bs := bucketState(t, g, governor.BucketGraphQL)
	if bs.Spent != 3 || bs.Remaining != 1400 || !bs.ResetAt.Equal(reset) || bs.InFlight != 0 {
		t.Errorf("after a folded response: %+v", bs)
	}
	// A response without the selection books the estimate, as an error.
	if err := f.SettleGraphQL(admit(2), meta, []byte(`{"data":null,"errors":[{"message":"boom"}]}`), errors.New("gh: boom")); err != nil {
		t.Fatal(err)
	}
	if bs := bucketState(t, g, governor.BucketGraphQL); bs.Spent != 5 {
		t.Errorf("spent %d, want 3 + the estimate of 2", bs.Spent)
	}
	// gh refused to run without a credential: nothing reached GitHub.
	authErr := fmt.Errorf("gh api graphql: no usable GitHub credential: %w", github.ErrGHAuthInvalid)
	if err := f.SettleGraphQL(admit(4), meta, nil, authErr); err != nil {
		t.Fatal(err)
	}
	bs = bucketState(t, g, governor.BucketGraphQL)
	if bs.Spent != 5 || bs.InFlight != 0 {
		t.Errorf("after an unauthenticated call: %+v", bs)
	}
	if len(*events) != 2 || (*events)[0].Cost != 3 || (*events)[0].Remaining != 1400 || (*events)[0].Outcome != OutcomeOK ||
		(*events)[1].Outcome != OutcomeError || (*events)[0].Group != "membership" {
		t.Errorf("events = %+v", *events)
	}
}

// The conversation page documents of internal/github select `rateLimit { cost }`
// alone. Its cost is booked; its missing remaining must not become a reading of
// zero, which would hold every class below the reserve until the window ends.
func TestSettleGraphQLWithACostOnlySelectionBooksTheCostAndKeepsTheLastReading(t *testing.T) {
	f, g, _, _, events := newFeed(t, nil, nil)
	meta := Meta{Class: governor.ClassInteractive, Purpose: governor.PurposeInteractive, Group: "conversation", BatchSize: 1}
	if err := g.Observe(governor.Reading{Bucket: governor.BucketGraphQL, Remaining: 3000, ResetAt: reset}); err != nil {
		t.Fatal(err)
	}
	tk, err := g.Admit(governor.Request{Bucket: governor.BucketGraphQL, Class: meta.Class, Cost: 1})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"data":{"rateLimit":{"cost":2},"repository":{"pullRequest":{"comments":{"nodes":[]}}}}}`)
	if err := f.SettleGraphQL(tk, meta, body, nil); err != nil {
		t.Fatal(err)
	}
	bs := bucketState(t, g, governor.BucketGraphQL)
	if bs.Spent != 2 || bs.Remaining != 2998 || !bs.ResetAt.Equal(reset) || bs.InFlight != 0 {
		t.Errorf("after a cost-only response: %+v, want spent 2 and the earlier reading (3000) less that spend", bs)
	}
	if len(*events) != 1 || (*events)[0].Cost != 2 || (*events)[0].Remaining != 2998 {
		t.Errorf("events = %+v", *events)
	}
	// Nothing is held below the reserve: the next call is admitted.
	if err := g.Check(governor.Request{Bucket: governor.BucketGraphQL, Class: governor.ClassInteractive, Cost: 1}); err != nil {
		t.Errorf("a cost-only response held the bucket: %v", err)
	}
}

func TestRateLimitErrorWithoutAVisibleStatusDoesNotClaimHTTP0(t *testing.T) {
	unseen := (&RateLimitError{Secondary: true, RetryAfter: time.Minute}).Error()
	if strings.Contains(unseen, "HTTP 0") || !strings.Contains(unseen, "secondary rate limit") || !strings.Contains(unseen, "1m0s") {
		t.Errorf("message = %q", unseen)
	}
	seen := (&RateLimitError{StatusCode: 403, Secondary: true, RetryAfter: 45 * time.Second}).Error()
	if !strings.Contains(seen, "HTTP 403") || !strings.Contains(seen, "45s") {
		t.Errorf("message = %q", seen)
	}
}

// stdinRunner is a Runner that also feeds stdin, recording what it was given.
type stdinRunner struct {
	fakeRunner
	stdin [][]byte
}

func (s *stdinRunner) RunStdin(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	s.stdin = append(s.stdin, stdin)
	return s.Run(ctx, args...)
}

func newStdinFeed(t *testing.T, out []byte, runErr error) (*Feed, *governor.Governor, *stdinRunner, *[]governor.SpendEvent) {
	t.Helper()
	clk := governor.NewManualClock(t0)
	var events []governor.SpendEvent
	g, err := governor.New(GovernorConfig(config.Defaults().Settings), governor.Options{
		Clock: clk, OnSpend: func(e governor.SpendEvent) { events = append(events, e) },
	})
	if err != nil {
		t.Fatal(err)
	}
	run := &stdinRunner{fakeRunner: fakeRunner{out: out, err: runErr}}
	return New(g, run, clk), g, run, &events
}

// A REST write with a JSON body (the create-pending-review call shape) goes
// through gh api --include with its body on stdin, and is admitted and booked as
// REST does.
func TestRESTStdinRunsAWriteThroughGhAPIIncludeAndFeedsTheReading(t *testing.T) {
	out := ghInclude("201 Created", rateHeaders("core", 5000, 4986, 14), `{"id":7,"node_id":"PRR_node","state":"PENDING"}`)
	f, g, run, events := newStdinFeed(t, out, nil)
	payload := []byte(`{"commit_id":"0123abc","body":"summary"}`)
	resp, err := f.RESTStdin(context.Background(), RESTCall{
		Meta: Meta{Class: governor.ClassInvalidated, Purpose: governor.PurposeWrite, Group: "pending", BatchSize: 1},
		Args: []string{"repos/o/r/pulls/1/reviews", "--method", "POST", "--input", "-"},
	}, payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.calls) != 1 || strings.Join(run.calls[0], " ") != "api --include repos/o/r/pulls/1/reviews --method POST --input -" {
		t.Fatalf("gh calls = %v", run.calls)
	}
	if len(run.stdin) != 1 || string(run.stdin[0]) != string(payload) {
		t.Errorf("stdin = %q, want the payload", run.stdin)
	}
	if resp.StatusCode != 201 || string(resp.Body) != `{"id":7,"node_id":"PRR_node","state":"PENDING"}` {
		t.Errorf("response = %+v", resp)
	}
	bs := bucketState(t, g, governor.BucketRESTCore)
	if bs.Spent != 1 || bs.Remaining != 4986 || bs.Limit != 5000 || bs.SpentByPurpose[governor.PurposeWrite] != 1 || bs.InFlight != 0 {
		t.Errorf("rest_core state = %+v", bs)
	}
	want := governor.SpendEvent{
		At: t0, Bucket: governor.BucketRESTCore, Class: governor.ClassInvalidated, Purpose: governor.PurposeWrite,
		Group: "pending", BatchSize: 1, Cost: 1, Remaining: 4986, Outcome: OutcomeOK,
	}
	if len(*events) != 1 || (*events)[0] != want {
		t.Errorf("spend events = %+v, want %+v", *events, want)
	}
}

func TestRESTStdinOnARunnerWithoutStdinSaysSoAndAdmitsNothing(t *testing.T) {
	out := ghInclude("200 OK", rateHeaders("core", 5000, 4000, 1000), "{}")
	f, g, _, run, events := newFeed(t, out, nil) // fakeRunner: Run only
	resp, err := f.RESTStdin(context.Background(), RESTCall{
		Meta: Meta{Class: governor.ClassInteractive}, Args: []string{"repos/o/r/pulls/1/reviews", "--input", "-"},
	}, []byte("{}"))
	if !errors.Is(err, ErrNoStdinRunner) || resp != nil {
		t.Fatalf("resp %+v err %v, want ErrNoStdinRunner", resp, err)
	}
	if !strings.Contains(err.Error(), "StdinRunner") {
		t.Errorf("the error does not name the missing interface: %v", err)
	}
	if len(run.calls) != 0 || len(*events) != 0 {
		t.Errorf("gh ran %d times, %d spend events", len(run.calls), len(*events))
	}
	if bs := bucketState(t, g, governor.BucketRESTCore); bs.Spent != 0 || bs.InFlight != 0 {
		t.Errorf("rest_core = %+v: nothing may be reserved for a call that cannot run", bs)
	}
}

func TestRESTStdinPausesTheBackgroundOnASecondaryLimitLikeREST(t *testing.T) {
	headers := rateHeaders("core", 5000, 4900, 100, [2]string{"Retry-After", "30"})
	out := ghInclude("403 Forbidden", headers, `{"message":"You have exceeded a secondary rate limit."}`)
	authish := fmt.Errorf("gh api: (HTTP 403): %w", github.ErrGHAuthInvalid)
	f, g, _, _ := newStdinFeed(t, out, authish)
	resp, err := f.RESTStdin(context.Background(), RESTCall{
		Meta: Meta{Class: governor.ClassInvalidated, Purpose: governor.PurposeWrite},
		Args: []string{"repos/o/r/pulls/1/reviews", "--method", "POST", "--input", "-"},
	}, []byte("{}"))
	var rl *RateLimitError
	if !errors.As(err, &rl) || !rl.Secondary || rl.RetryAfter != 30*time.Second || errors.Is(err, github.ErrGHAuthInvalid) {
		t.Fatalf("err = %v, want a secondary *RateLimitError for 30s that is not an auth failure", err)
	}
	if resp == nil || resp.StatusCode != 403 {
		t.Errorf("response %+v was not returned beside the error", resp)
	}
	for _, b := range governor.Buckets() {
		if bs := bucketState(t, g, b); !bs.Paused || !bs.PausedUntil.Equal(t0.Add(30*time.Second)) {
			t.Errorf("%s state = %+v", b, bs)
		}
	}
}

// A GraphQL read retried by github.Provider's own bounded retry takes the
// governor's check before the retry, so a retry cannot spend past the limit.
func TestRetryGuardStopsAGraphQLRetryTheGovernorWouldRefuse(t *testing.T) {
	f, g, _, _, _ := newFeed(t, nil, nil)
	reading := governor.Reading{Bucket: governor.BucketGraphQL, Remaining: 1000, ResetAt: reset}
	if err := g.Observe(reading); err != nil {
		t.Fatal(err)
	}

	page := []byte(`{"data":{"rateLimit":{"cost":1,"remaining":4000,"resetAt":"2026-10-12T12:30:00Z"},"search":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}`)
	transient := errors.New("gh api graphql: error connecting to api.github.com")
	newProvider := func(r *sequenceRunner) *github.Provider {
		p := github.NewWithRunner(r).WithRetryPolicy(github.RetryPolicy{
			MaxAttempts: 2, Backoff: func(int) time.Duration { return 0 },
			Sleep: func(context.Context, time.Duration) error { return nil },
		})
		p.SetRetryGuard(f.RetryGuard(governor.ClassQuery, governor.BucketGraphQL, 1))
		return p
	}

	// At the reserve the guard refuses, and the second attempt never runs.
	denied := &sequenceRunner{results: []result{{err: transient}, {out: page}}}
	_, _, err := newProvider(denied).SearchPRsEnrichedGated(context.Background(), "author:@me", nil)
	var refusal *governor.Refusal
	if !errors.As(err, &refusal) || refusal.Kind != governor.RefusedReserve || denied.calls != 1 {
		t.Fatalf("err = %v after %d calls, want a reserve refusal after exactly 1", err, denied.calls)
	}

	// With room the same retry goes ahead.
	if err := g.Observe(governor.Reading{Bucket: governor.BucketGraphQL, Remaining: 4000, ResetAt: reset}); err != nil {
		t.Fatal(err)
	}
	allowed := &sequenceRunner{results: []result{{err: transient}, {out: page}}}
	if _, _, err := newProvider(allowed).SearchPRsEnrichedGated(context.Background(), "author:@me", nil); err != nil || allowed.calls != 2 {
		t.Fatalf("err = %v after %d calls, want success on the retry", err, allowed.calls)
	}
}

type result struct {
	out []byte
	err error
}

// sequenceRunner answers each call with the next canned result (the github
// package's own ghRunner shape).
type sequenceRunner struct {
	results []result
	calls   int
}

func (s *sequenceRunner) Run(_ context.Context, _ ...string) ([]byte, error) {
	r := s.results[min(s.calls, len(s.results)-1)]
	s.calls++
	return r.out, r.err
}

func (s *sequenceRunner) RunStdin(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	return s.Run(ctx, args...)
}

// fixedToken is a github.TokenSource that always answers with one fake token.
type fixedToken struct{}

func (fixedToken) Token(context.Context) (string, error) { return "fixture-token", nil }

// TestRESTThroughTheRealGateway runs Feed.REST over the real token-protected gh
// gateway (github.CLI) with a stand-in gh executable, so the argument list
// (`api --include ...`), the configured host and the stdout of an error status
// are exercised through the actual exec path.
func TestRESTThroughTheRealGateway(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs /bin/sh")
	}
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	script := filepath.Join(dir, "gh-standin")
	body := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" > '" + argv + "'\n" +
		"printf 'host=%s token=%s\\n' \"$GH_HOST\" \"$GH_TOKEN\" >> '" + argv + "'\n" +
		"printf 'HTTP/2.0 403 Forbidden\\n'\n" +
		"printf 'Content-Type: application/json\\r\\n'\n" +
		"printf 'Retry-After: 45\\r\\n'\n" +
		"printf 'X-Ratelimit-Limit: 5000\\r\\nX-Ratelimit-Remaining: 4500\\r\\nX-Ratelimit-Reset: " + fmt.Sprint(reset.Unix()) + "\\r\\n'\n" +
		"printf 'X-Ratelimit-Resource: core\\r\\n\\r\\n'\n" +
		"printf '%s' '{\"message\":\"You have exceeded a secondary rate limit.\"}'\n" +
		"printf 'gh: You have exceeded a secondary rate limit. (HTTP 403)\\n' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cli := github.NewCLIWithOptions(github.Options{Binary: script, Host: "ghe.example.com", TokenSource: fixedToken{}})
	clk := governor.NewManualClock(t0)
	g, err := governor.New(GovernorConfig(config.Defaults().Settings), governor.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	f := New(g, cli, clk)
	resp, err := f.REST(context.Background(), RESTCall{Meta: Meta{Class: governor.ClassQuery, Purpose: governor.PurposeQuery}, Args: []string{"repos/o/r/actions/runs", "-f", "per_page=5"}})
	var rl *RateLimitError
	if !errors.As(err, &rl) || !rl.Secondary || rl.RetryAfter != 45*time.Second {
		t.Fatalf("err = %v, want a secondary limit of 45s", err)
	}
	if resp == nil || resp.StatusCode != 403 || !strings.Contains(string(resp.Body), "secondary rate limit") {
		t.Errorf("resp = %+v", resp)
	}
	got, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	if want := "api --include repos/o/r/actions/runs -f per_page=5\nhost=ghe.example.com token=fixture-token\n"; string(got) != want {
		t.Errorf("gh saw %q, want %q", got, want)
	}
	if bs := bucketState(t, g, governor.BucketRESTCore); !bs.Paused || !bs.PausedUntil.Equal(t0.Add(45*time.Second)) || bs.Remaining != 4500 || bs.Spent != 1 {
		t.Errorf("rest_core = %+v", bs)
	}
}

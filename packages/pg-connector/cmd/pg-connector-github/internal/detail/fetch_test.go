package detail

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

// countingGH is a fake gh runner that records every call.
type countingGH struct {
	calls  [][]string
	answer func(args []string) ([]byte, error)
}

func (g *countingGH) Run(_ context.Context, args ...string) ([]byte, error) {
	g.calls = append(g.calls, args)
	return g.answer(args)
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sleeps records the backoff delays instead of waiting.
type sleeps struct{ delays []time.Duration }

func (s *sleeps) sleep(_ context.Context, d time.Duration) error {
	s.delays = append(s.delays, d)
	return nil
}

func newFetcher(t *testing.T, gh *countingGH) (*Fetcher, *governor.Governor, *sleeps) {
	t.Helper()
	clk := governor.NewManualClock(t0)
	gov, err := governor.New(ratefeed.GovernorConfig(config.Defaults().Settings), governor.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	sl := &sleeps{}
	policy := github.DefaultRetryPolicy()
	policy.Sleep = sl.sleep
	return &Fetcher{GH: gh, Governor: gov, Feed: ratefeed.New(gov, gh, clk), Retry: &policy}, gov, sl
}

var background = ratefeed.Meta{Class: governor.ClassKeepAlive, Purpose: governor.PurposeRefresh}

func idsOf(args []string) []string {
	var ids []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-f" && strings.HasPrefix(args[i+1], "ids[]=") {
			ids = append(ids, strings.TrimPrefix(args[i+1], "ids[]="))
		}
	}
	return ids
}

func targets(n int) []groups.Target {
	out := make([]groups.Target, n)
	for i := range out {
		out[i] = groups.Target{NodeID: fmt.Sprintf("PR_%d", i), Stamps: groups.Stamps{Head: fmt.Sprintf("head-%d", i), Base: "base-1"}}
	}
	return out
}

// answerFor builds a successful response with a CLEAN node for every id.
func answerFor(ids []string) []byte {
	var nodes []string
	for _, id := range ids {
		nodes = append(nodes, fmt.Sprintf(`{"id":%q,"mergeStateStatus":"CLEAN","merged":false,"additions":1,"deletions":2,"changedFiles":3,`+
			`"reviewRequests":{"nodes":[{"requestedReviewer":{"login":"user-%s"}}]}}`, id, id))
	}
	return []byte(`{"data":{"rateLimit":{"cost":1,"remaining":4900,"resetAt":"2026-10-12T10:00:00Z"},"nodes":[` + strings.Join(nodes, ",") + `]}}`)
}

func spent(t *testing.T, gov *governor.Governor) int64 {
	t.Helper()
	bs, _ := gov.State().Bucket(governor.BucketGraphQL)
	return bs.Spent
}

func TestFetcher_Identity(t *testing.T) {
	f := &Fetcher{}
	if f.Group() != groups.Detail || f.Group() != "detail" {
		t.Errorf("Group = %q", f.Group())
	}
	for size, want := range map[int]int{0: 25, -3: 25, 1: 1, 10: 10, 50: 50, 51: 50, 500: 50} {
		f.BatchSize = size
		if got := f.MaxBatch(); got != want {
			t.Errorf("BatchSize %d: MaxBatch = %d, want %d", size, got, want)
		}
	}
	req := f.Spend(governor.ClassInteractive, 40)
	if req.Bucket != governor.BucketGraphQL || req.Class != governor.ClassInteractive || req.Cost != 1 {
		t.Errorf("Spend = %+v", req)
	}
}

func TestFetch_DecodesTheFieldSet(t *testing.T) {
	gh := &countingGH{answer: func([]string) ([]byte, error) { return fixture(t, "nodes_two.json"), nil }}
	f, gov, _ := newFetcher(t, gh)
	ts := []groups.Target{{NodeID: "PR_a", Stamps: groups.Stamps{Head: "h-a", Base: "b-a"}}, {NodeID: "PR_b", Stamps: groups.Stamps{Head: "h-b", Base: "b-b"}}}
	got := f.Fetch(context.Background(), background, ts)
	if len(gh.calls) != 1 || len(got) != 2 {
		t.Fatalf("calls %d outcomes %d, want 1 and 2", len(gh.calls), len(got))
	}
	a, err := Decode(got[0].Content)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Err != nil || a.Merged || a.MergeStateStatus != "BLOCKED" || a.Additions != 12 || a.Deletions != 3 || a.ChangedFiles != 2 ||
		strings.Join(a.ReviewRequests, ",") != "phillipgreenii,widget-reviewers,review-bot" {
		t.Errorf("detail a = %+v (err %v)", a, got[0].Err)
	}
	b, err := Decode(got[1].Content)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Merged || b.MergeStateStatus != "UNKNOWN" || len(b.ReviewRequests) != 0 || !strings.Contains(string(got[1].Content), `"reviewRequests":[]`) {
		t.Errorf("detail b = %+v content %s", b, got[1].Content)
	}
	for i, o := range got {
		if o.Stamps != ts[i].Stamps {
			t.Errorf("outcome %d stamps = %+v, want %+v", i, o.Stamps, ts[i].Stamps)
		}
		d, _ := Decode(o.Content)
		if o.Fingerprint != Fingerprint(d) {
			t.Errorf("outcome %d fingerprint %s", i, o.Fingerprint)
		}
	}
	if got[0].Cost+got[1].Cost != 1 {
		t.Errorf("costs %d + %d, want 1 in all", got[0].Cost, got[1].Cost)
	}
	if bs, _ := gov.State().Bucket(governor.BucketGraphQL); bs.Spent != 1 || bs.Remaining != 4900 {
		t.Errorf("governor saw spent %d remaining %d, want 1 and 4900", bs.Spent, bs.Remaining)
	}
	if !strings.Contains(strings.Join(gh.calls[0], " "), "api graphql") {
		t.Errorf("args = %v", gh.calls[0])
	}
}

func TestFetch_OneCallFor25Targets(t *testing.T) {
	gh := &countingGH{answer: func(args []string) ([]byte, error) { return answerFor(idsOf(args)), nil }}
	f, gov, _ := newFetcher(t, gh)
	got := f.Fetch(context.Background(), background, targets(25))
	if len(gh.calls) != 1 || len(idsOf(gh.calls[0])) != 25 || len(got) != 25 {
		t.Fatalf("calls %d, ids %d, outcomes %d; want 1, 25, 25", len(gh.calls), len(idsOf(gh.calls[0])), len(got))
	}
	if spent(t, gov) != 1 {
		t.Errorf("spent %d points, want 1", spent(t, gov))
	}
}

func TestFetch_SplitsIntoBatchesKeepingOrderAndBookingEveryCall(t *testing.T) {
	gh := &countingGH{answer: func(args []string) ([]byte, error) { return answerFor(idsOf(args)), nil }}
	f, gov, _ := newFetcher(t, gh)
	for _, n := range []int{0, 1, 25, 26, 60, 75} {
		gh.calls = nil
		before := spent(t, gov)
		ts := targets(n)
		got := f.Fetch(context.Background(), background, ts)
		wantCalls := (n + 24) / 25
		if len(gh.calls) != wantCalls || len(got) != n {
			t.Errorf("n=%d: %d calls, %d outcomes, want %d and %d", n, len(gh.calls), len(got), wantCalls, n)
		}
		if booked := spent(t, gov) - before; booked != int64(wantCalls) {
			t.Errorf("n=%d: booked %d points, want %d (one per call)", n, booked, wantCalls)
		}
		var seen int
		var cost int64
		for _, c := range gh.calls {
			if k := len(idsOf(c)); k > 25 {
				t.Errorf("n=%d: a call carries %d ids", n, k)
			}
			seen += len(idsOf(c))
		}
		for i, o := range got {
			cost += o.Cost
			d, err := Decode(o.Content)
			if o.Err != nil || err != nil || len(d.ReviewRequests) != 1 || d.ReviewRequests[0] != "user-"+ts[i].NodeID {
				t.Fatalf("n=%d: outcome %d = %+v (%v): order must be kept", n, i, d, err)
			}
			if o.Stamps != ts[i].Stamps {
				t.Errorf("n=%d: outcome %d stamps %+v", n, i, o.Stamps)
			}
		}
		if seen != n || cost != int64(wantCalls) {
			t.Errorf("n=%d: sent %d ids, outcome costs sum to %d, want %d and %d", n, seen, cost, n, wantCalls)
		}
	}
}

func TestFetch_BatchSizeLowersTheBatch(t *testing.T) {
	gh := &countingGH{answer: func(args []string) ([]byte, error) { return answerFor(idsOf(args)), nil }}
	f, _, _ := newFetcher(t, gh)
	f.BatchSize = 10
	f.Fetch(context.Background(), background, targets(25))
	if len(gh.calls) != 3 {
		t.Errorf("%d calls, want 3 (10, 10, 5)", len(gh.calls))
	}
}

func TestFetch_StampsAreThoseBeforeTheCall(t *testing.T) {
	gh := &countingGH{answer: func(args []string) ([]byte, error) { return nil, errors.New("gh api graphql: exit status 1") }}
	f, _, _ := newFetcher(t, gh)
	ts := targets(3)
	for i, o := range f.Fetch(context.Background(), background, ts) {
		if o.Stamps != ts[i].Stamps || o.Err == nil {
			t.Errorf("outcome %d = %+v", i, o)
		}
	}
}

func is502(args []string) bool { return len(idsOf(args)) >= 2 }

func TestFetch_FallsBackPerPRAfter502And504(t *testing.T) {
	for _, status := range []string{"502", "504"} {
		t.Run(status, func(t *testing.T) {
			gh := &countingGH{answer: func(args []string) ([]byte, error) {
				if is502(args) {
					return nil, errors.New("gh api graphql: exit status 1: gh: HTTP " + status)
				}
				return answerFor(idsOf(args)), nil
			}}
			f, gov, sl := newFetcher(t, gh)
			ts := targets(5)
			got := f.Fetch(context.Background(), background, ts)
			if len(gh.calls) != 1+5 {
				t.Fatalf("%d calls, want 1 batch + 5 per-PR", len(gh.calls))
			}
			if len(idsOf(gh.calls[0])) != 5 {
				t.Errorf("first call carried %d ids", len(idsOf(gh.calls[0])))
			}
			for i, c := range gh.calls[1:] {
				if ids := idsOf(c); len(ids) != 1 || ids[0] != ts[i].NodeID {
					t.Errorf("fallback call %d ids = %v", i, ids)
				}
			}
			for i, o := range got {
				d, err := Decode(o.Content)
				if o.Err != nil || err != nil || d.ReviewRequests[0] != "user-"+ts[i].NodeID || o.Stamps != ts[i].Stamps {
					t.Errorf("outcome %d = %+v %+v (%v)", i, o, d, err)
				}
			}
			if len(sl.delays) != 0 {
				t.Errorf("the batch was retried before falling back: %v", sl.delays)
			}
			// Every call was admitted: the failed batch call reserved a point too.
			if spent(t, gov) != 6 {
				t.Errorf("spent %d points, want 6 (one per call)", spent(t, gov))
			}
		})
	}
}

func TestFetch_SingleIDGatewayErrorIsThatTargetsAlone(t *testing.T) {
	gh := &countingGH{answer: func(args []string) ([]byte, error) {
		if ids := idsOf(args); is502(args) || ids[0] == "PR_1" {
			return nil, errors.New("gh api graphql: exit status 1: gh: HTTP 502")
		}
		return answerFor(idsOf(args)), nil
	}}
	f, _, _ := newFetcher(t, gh)
	got := f.Fetch(context.Background(), background, targets(3))
	if got[0].Err != nil || got[2].Err != nil || got[1].Err == nil || !strings.Contains(got[1].Err.Error(), "HTTP 502") {
		t.Fatalf("errors = %v, %v, %v; want only the middle target to fail", got[0].Err, got[1].Err, got[2].Err)
	}
	if got[1].Content != nil {
		t.Error("a failed target carries content")
	}
}

func TestFetch_OtherBatchErrorsFailEveryTargetWithoutFallback(t *testing.T) {
	for name, runErr := range map[string]error{
		"forbidden":  errors.New("gh api graphql: exit status 1: gh: HTTP 403"),
		"rate limit": &ratefeed.RateLimitError{StatusCode: 403, Secondary: true, RetryAfter: time.Minute},
	} {
		t.Run(name, func(t *testing.T) {
			gh := &countingGH{answer: func([]string) ([]byte, error) { return nil, runErr }}
			f, _, sl := newFetcher(t, gh)
			got := f.Fetch(context.Background(), background, targets(4))
			if len(gh.calls) != 1 || len(sl.delays) != 0 {
				t.Errorf("%d calls, %d sleeps; want one call, no retry, no fallback", len(gh.calls), len(sl.delays))
			}
			for i, o := range got {
				if o.Err != runErr {
					t.Errorf("outcome %d error = %v, want the error unchanged", i, o.Err)
				}
			}
		})
	}
}

func TestFetch_BadAnswersAreErrorsAndNotRetried(t *testing.T) {
	for name, answer := range map[string]func([]string) ([]byte, error){
		"not json":         func([]string) ([]byte, error) { return []byte("nope"), nil },
		"wrong node count": func([]string) ([]byte, error) { return []byte(`{"data":{"nodes":[]}}`), nil },
		"other gql error": func([]string) ([]byte, error) {
			return []byte(`{"data":{"nodes":[null]},"errors":[{"type":"FORBIDDEN"}]}`), errors.New("gh api graphql: exit status 1")
		},
	} {
		t.Run(name, func(t *testing.T) {
			gh := &countingGH{answer: answer}
			f, _, _ := newFetcher(t, gh)
			got := f.Fetch(context.Background(), background, targets(1))
			if got[0].Err == nil || errors.Is(got[0].Err, groups.ErrGone) || len(gh.calls) != 1 {
				t.Errorf("err %v after %d calls", got[0].Err, len(gh.calls))
			}
		})
	}
}

func TestFetch_UnresolvedNodesAreGoneForThatTargetOnly(t *testing.T) {
	gh := &countingGH{answer: func([]string) ([]byte, error) {
		return fixture(t, "nodes_gone.json"), errors.New("gh api graphql: exit status 1")
	}}
	f, _, _ := newFetcher(t, gh)
	ts := []groups.Target{{NodeID: "PR_deleted"}, {NodeID: "PR_live"}, {NodeID: "ISSUE_1"}}
	got := f.Fetch(context.Background(), background, ts)
	if len(gh.calls) != 1 {
		t.Errorf("%d calls, want 1", len(gh.calls))
	}
	if !errors.Is(got[0].Err, groups.ErrGone) || !errors.Is(got[2].Err, groups.ErrGone) {
		t.Errorf("errors = %v, %v; want ErrGone for the null node and the non-PR node", got[0].Err, got[2].Err)
	}
	if got[1].Err != nil || got[1].Content == nil {
		t.Errorf("the live node = %+v", got[1])
	}
}

func TestFetch_RetriesATransientFailure(t *testing.T) {
	n := 0
	gh := &countingGH{answer: func(args []string) ([]byte, error) {
		n++
		if n == 1 {
			return nil, errors.New("gh api graphql: exit status 1: gh: HTTP 503")
		}
		return answerFor(idsOf(args)), nil
	}}
	f, gov, sl := newFetcher(t, gh)
	got := f.Fetch(context.Background(), background, targets(2))
	if got[0].Err != nil || got[1].Err != nil || len(gh.calls) != 2 {
		t.Fatalf("errors %v %v after %d calls", got[0].Err, got[1].Err, len(gh.calls))
	}
	if spent(t, gov) != 2 {
		t.Errorf("spent %d points, want 2 admissions", spent(t, gov))
	}
	if len(sl.delays) != 1 || sl.delays[0] != 500*time.Millisecond {
		t.Errorf("backoff = %v, want [500ms]", sl.delays)
	}
}

func TestFetch_GivesUpAfterThreeAttempts(t *testing.T) {
	gh := &countingGH{answer: func([]string) ([]byte, error) {
		return nil, errors.New("gh api graphql: exit status 1: gh: HTTP 503")
	}}
	f, gov, sl := newFetcher(t, gh)
	got := f.Fetch(context.Background(), background, targets(2))
	if len(gh.calls) != 3 || spent(t, gov) != 3 {
		t.Errorf("%d calls, %d points; want 3 each", len(gh.calls), spent(t, gov))
	}
	if len(sl.delays) != 2 || sl.delays[0] != 500*time.Millisecond || sl.delays[1] != time.Second {
		t.Errorf("backoff = %v, want [500ms 1s]", sl.delays)
	}
	for i, o := range got {
		if o.Err == nil || !strings.Contains(o.Err.Error(), "HTTP 503") {
			t.Errorf("outcome %d error = %v", i, o.Err)
		}
	}
}

func TestFetch_SingleIDGatewayErrorIsRetried(t *testing.T) {
	gh := &countingGH{answer: func([]string) ([]byte, error) {
		return nil, errors.New("gh api graphql: exit status 1: gh: HTTP 504")
	}}
	f, _, _ := newFetcher(t, gh)
	got := f.Fetch(context.Background(), background, targets(1))
	if got[0].Err == nil || len(gh.calls) != 3 {
		t.Errorf("err %v after %d calls, want an error after 3 attempts", got[0].Err, len(gh.calls))
	}
}

func TestFetch_MultiIDGatewayErrorIsNotRetriedAsABatch(t *testing.T) {
	gh := &countingGH{answer: func(args []string) ([]byte, error) {
		if is502(args) {
			return nil, errors.New("gh api graphql: exit status 1: gh: HTTP 504")
		}
		return answerFor(idsOf(args)), nil
	}}
	f, _, sl := newFetcher(t, gh)
	f.Fetch(context.Background(), background, targets(2))
	if len(gh.calls) != 3 || len(sl.delays) != 0 {
		t.Errorf("%d calls, %d sleeps; want 1 batch + 2 per-PR and no backoff", len(gh.calls), len(sl.delays))
	}
}

func TestFetch_CancelledContextStopsTheRetrying(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	gh := &countingGH{answer: func([]string) ([]byte, error) {
		cancel()
		return nil, errors.New("gh api graphql: exit status 1: gh: HTTP 503")
	}}
	f, _, sl := newFetcher(t, gh)
	got := f.Fetch(ctx, background, targets(1))
	if len(gh.calls) != 1 || len(sl.delays) != 0 || got[0].Err == nil {
		t.Errorf("%d calls, %d sleeps, err %v; want one attempt and an error", len(gh.calls), len(sl.delays), got[0].Err)
	}

	// A context that ends while waiting out the backoff stops the retrying too.
	ctx2, cancel2 := context.WithCancel(context.Background())
	gh2 := &countingGH{answer: func([]string) ([]byte, error) {
		return nil, errors.New("gh api graphql: exit status 1: gh: HTTP 503")
	}}
	f2, _, _ := newFetcher(t, gh2)
	policy := github.DefaultRetryPolicy()
	policy.Sleep = func(context.Context, time.Duration) error { cancel2(); return ctx2.Err() }
	f2.Retry = &policy
	got = f2.Fetch(ctx2, background, targets(1))
	if len(gh2.calls) != 1 || got[0].Err == nil {
		t.Errorf("%d calls, err %v; want one attempt and an error", len(gh2.calls), got[0].Err)
	}
}

func TestFetch_NoRetryWindowLeftStopsTheRetrying(t *testing.T) {
	gh := &countingGH{answer: func([]string) ([]byte, error) {
		return nil, errors.New("gh api graphql: exit status 1: gh: HTTP 503")
	}}
	f, _, sl := newFetcher(t, gh)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second) // under the 5s window
	defer cancel()
	f.Fetch(ctx, background, targets(1))
	if len(gh.calls) != 1 || len(sl.delays) != 0 {
		t.Errorf("%d calls, %d sleeps; want one attempt", len(gh.calls), len(sl.delays))
	}
}

func TestFetch_GovernorRefusalMakesNoCall(t *testing.T) {
	gh := &countingGH{answer: func([]string) ([]byte, error) { return nil, errors.New("must not run") }}
	f, gov, _ := newFetcher(t, gh)
	gov.PauseAll(time.Minute, "test pause")
	got := f.Fetch(context.Background(), background, targets(3))
	if len(gh.calls) != 0 {
		t.Errorf("%d calls, want none", len(gh.calls))
	}
	for i, o := range got {
		var r *governor.Refusal
		if !errors.As(o.Err, &r) || !errors.Is(o.Err, governor.ErrRefused) {
			t.Errorf("outcome %d error = %v, want a *governor.Refusal", i, o.Err)
		}
	}
}

func TestFetch_RefusalPartWayThroughTheFallbackKeepsEarlierResults(t *testing.T) {
	var gov *governor.Governor
	gh := &countingGH{}
	gh.answer = func(args []string) ([]byte, error) {
		ids := idsOf(args)
		if is502(args) {
			return nil, errors.New("gh api graphql: exit status 1: gh: HTTP 502")
		}
		if ids[0] == "PR_1" {
			gov.PauseAll(time.Minute, "test pause") // the next admission is refused
		}
		return answerFor(ids), nil
	}
	f, g, _ := newFetcher(t, gh)
	gov = g
	got := f.Fetch(context.Background(), background, targets(5))
	if len(gh.calls) != 1+2 {
		t.Errorf("%d calls, want the batch and two per-PR calls", len(gh.calls))
	}
	for i := 0; i < 2; i++ {
		if got[i].Err != nil || got[i].Content == nil {
			t.Errorf("outcome %d = %+v, want its result kept", i, got[i])
		}
	}
	for i := 2; i < 5; i++ {
		var r *governor.Refusal
		if !errors.As(got[i].Err, &r) || got[i].Content != nil {
			t.Errorf("outcome %d = %+v, want the refusal", i, got[i])
		}
	}
}

// The document is the detail field set. Changing it moves what a call costs and
// how reliably a batch of MaxBatch ids answers, so it is pinned.
func TestDocument_Pinned(t *testing.T) {
	const want = "query($ids: [ID!]!) { rateLimit { cost remaining resetAt } nodes(ids: $ids) { ... on PullRequest { " +
		"id mergeStateStatus merged additions deletions changedFiles reviewRequests(first: 100) { nodes { requestedReviewer { " +
		"... on User { login } ... on Bot { login } ... on Mannequin { login } ... on Team { slug } } } } } } }"
	if got := strings.Join(strings.Fields(document), " "); got != want {
		t.Fatalf("document changed. The 1-point cost and the batch ceiling were measured for the pinned document only; "+
			"re-measure (rateLimit(dryRun: true) and a batch at MaxBatch ids), then update MaxBatch and this pin together.\n got: %s\nwant: %s", got, want)
	}
	if MaxBatch != 50 || DefaultBatch != 25 {
		t.Errorf("MaxBatch %d DefaultBatch %d, want 50 and 25", MaxBatch, DefaultBatch)
	}
}

package passthrough

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/metered"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var t0 = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

// fakeInner is the gh the metered Runner runs: it records every invocation and
// answers from reply (or from the canned golden scenario).
type fakeInner struct {
	mu    sync.Mutex
	calls []string
	reply func(args []string) (string, error)
}

func (f *fakeInner) Run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, strings.Join(args, " "))
	reply := f.reply
	f.mu.Unlock()
	out, err := reply(args)
	return []byte(out), err
}

func (f *fakeInner) RunStdin(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	return f.Run(ctx, args...)
}

func (f *fakeInner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// cannedReply answers the golden scenario, plus the conversation pages of the
// one commenter candidate (pr 16) when withComments is set.
func cannedReply(withComments bool) func(args []string) (string, error) {
	return func(args []string) (string, error) {
		line := strings.Join(args, " ")
		if withComments {
			switch {
			case len(args) >= 2 && args[0] == "search" && strings.Contains(line, "commenter:"):
				return "[" + prJSON(16, "Chatty", "teammate", "2026-09-02T00:00:00Z", "") + "]", nil
			case len(args) >= 2 && args[0] == "api" && args[1] == "graphql" && strings.Contains(line, "reviewThreads(first"):
				return `{"data":{"rateLimit":{"cost":1},"repository":{"pullRequest":{"reviewThreads":{"totalCount":0,` +
					`"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}}`, nil
			case len(args) >= 2 && args[0] == "api" && args[1] == "graphql" && strings.Contains(line, "pullRequest"):
				return `{"data":{"rateLimit":{"cost":1},"repository":{"pullRequest":{"comments":{"totalCount":1,` +
					`"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"id":"IC_1","author":{"login":"phillipgreenii"},` +
					`"authorAssociation":"OWNER","body":"hi","createdAt":"2026-09-11T09:00:00Z","updatedAt":"2026-09-11T09:00:00Z"}]}}}}}`, nil
			}
		}
		out, ok := cannedGH(args)
		if !ok {
			return "", fmt.Errorf("unexpected gh call: %s", line)
		}
		return out, nil
	}
}

// harness is the real metered Provider over a fake Inner and a real governor on
// a manual clock, with every booked spend recorded.
type harness struct {
	t      *testing.T
	clock  *governor.ManualClock
	gov    *governor.Governor
	inner  *fakeInner
	mu     sync.Mutex
	events []governor.SpendEvent
	gh     GitHub
}

func newHarness(t *testing.T, searchPerMinute int, reply func(args []string) (string, error)) *harness {
	t.Helper()
	h := &harness{t: t, clock: governor.NewManualClock(t0), inner: &fakeInner{reply: reply}}
	s := config.Defaults().Settings
	s.RESTSearchPerMinute = searchPerMinute
	g, err := governor.New(ratefeed.GovernorConfig(s), governor.Options{
		Clock: h.clock,
		OnSpend: func(e governor.SpendEvent) {
			h.mu.Lock()
			h.events = append(h.events, e)
			h.mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.gov = g
	h.gh = metered.NewProvider(&metered.Runner{Inner: h.inner, Governor: g, Feed: ratefeed.New(g, h.inner, h.clock)})
	return h
}

// booked sums the cost of the booked spends per bucket, and fails the test for any
// spend that is not class 1 with purpose passthrough.
func (h *harness) booked() map[governor.Bucket]int64 {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[governor.Bucket]int64{}
	for _, e := range h.events {
		if e.Class != governor.ClassInteractive || e.Purpose != governor.PurposePassthrough {
			h.t.Errorf("spend %+v is not class 1 with purpose passthrough", e)
		}
		out[e.Bucket] += e.Cost
	}
	return out
}

func (h *harness) handlers() map[string]scriptout.OpHandler { return Handlers(Deps{GH: h.gh}) }

const activityArgs = `{"since":"2026-09-01T00:00:00Z","before":"2026-10-01T00:00:00Z"}`

// search books the REST search bucket: one request for --limit 100.
func TestMetering_SearchBooksOneRESTSearchRequest(t *testing.T) {
	h := newHarness(t, 20, cannedReply(false))
	if _, err := h.handlers()[OpSearch].Handle(context.Background(), json.RawMessage(`{"query":"repo:o/r is:pr is:open"}`)); err != nil {
		t.Fatal(err)
	}
	got := h.booked()
	if len(got) != 1 || got[governor.BucketRESTSearch] != 1 || h.inner.count() != 1 {
		t.Fatalf("booked %v over %d gh calls, want exactly 1 REST search request in 1 call", got, h.inner.count())
	}
	if !strings.Contains(h.inner.calls[0], "--limit 100") {
		t.Errorf("call = %q, want --limit 100", h.inner.calls[0])
	}
}

// list_activity books ceil(1000 / 100) = 10 search requests for each of its five
// capped queries, plus the viewer read (GraphQL) and the per-PR reads (the merged
// PR, the reviewed PR's reviews, the commenter candidate's two conversation pages).
func TestMetering_ListActivityBooksEveryOriginCallAsClassOnePassthrough(t *testing.T) {
	h := newHarness(t, 100, cannedReply(true))
	if _, err := h.handlers()[OpListActivity].Handle(context.Background(), json.RawMessage(activityArgs)); err != nil {
		t.Fatal(err)
	}
	got := h.booked()
	const queries, perQuery = 5, (1000 + 99) / 100
	if got[governor.BucketRESTSearch] != queries*perQuery {
		t.Errorf("rest_search = %d requests, want %d queries x %d", got[governor.BucketRESTSearch], queries, perQuery)
	}
	// Viewer (1 point) and the commenter candidate's two conversation pages (1 point each).
	if got[governor.BucketGraphQL] != 3 {
		t.Errorf("graphql = %d points, want 3 (viewer + 2 conversation pages)", got[governor.BucketGraphQL])
	}
	// The merged PR read and the reviewed PR's reviews, one REST core request each.
	if got[governor.BucketRESTCore] != 2 {
		t.Errorf("rest_core = %d requests, want 2 (merged PR + reviews)", got[governor.BucketRESTCore])
	}
	if h.inner.count() != 5+1+2+2 {
		t.Errorf("%d gh calls: %q", h.inner.count(), h.inner.calls)
	}
}

// A governor refusal makes the op unavailable naming the cap, and nothing runs.
func TestMetering_RefusalIsUnavailableNamingTheCapAndRunsNothing(t *testing.T) {
	for _, op := range []struct{ name, args string }{
		{OpSearch, `{"query":"is:open"}`},
		{OpListActivity, activityArgs},
	} {
		t.Run(op.name, func(t *testing.T) {
			h := newHarness(t, 20, cannedReply(false))
			// Another caller has spent the whole window of the search bucket.
			if err := h.gov.Record(governor.Spend{Bucket: governor.BucketRESTSearch, Class: governor.ClassQuery, Purpose: governor.PurposeQuery, Cost: 20}); err != nil {
				t.Fatal(err)
			}
			h.events = nil
			_, err := h.handlers()[op.name].Handle(context.Background(), json.RawMessage(op.args))
			if !errors.Is(err, scriptout.ErrUnavailable) || !strings.Contains(err.Error(), "cap") {
				t.Fatalf("err = %v, want unavailable naming the cap", err)
			}
			// Nothing past the refusal ran: search runs nothing at all; list_activity
			// ran only its viewer read, ahead of the first search.
			want := map[string]int{OpSearch: 0, OpListActivity: 1}[op.name]
			if h.inner.count() != want {
				t.Errorf("gh ran %d times after a refusal, want %d: %q", h.inner.count(), want, h.inner.calls)
			}
			if got := h.booked()[governor.BucketRESTSearch]; got != 0 {
				t.Errorf("a refused op booked %d search requests", got)
			}
		})
	}
}

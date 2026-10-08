package github

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// instrumentedOp runs fn as op of an instrumented dispatch table, the way the
// serve loop does, and returns the one event it logged plus that event
// serialized as the log line (bead pg2-ir8bs).
func instrumentedOp(t *testing.T, op string, fn func(ctx context.Context) error) (eventlog.Event, map[string]any) {
	t.Helper()
	sink := &costSink{}
	table := scriptout.DispatchTable{op: scriptout.OpHandler{
		SchemaVersion: 1,
		Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
			return nil, fn(ctx)
		},
	}}
	wrapped := eventlog.Instrument(table, sink, "test", time.Now)
	_, _ = wrapped[op].Handle(context.Background(), nil)
	if len(sink.events) != 1 {
		t.Fatalf("expected one event, got %d", len(sink.events))
	}
	line, err := eventlog.Line(sink.events[0])
	if err != nil {
		t.Fatalf("Line: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatalf("decode log line: %v", err)
	}
	return sink.events[0], m
}

func requireCost(t *testing.T, ev eventlog.Event, line map[string]any, want int) {
	t.Helper()
	if ev.GraphQLCost == nil || *ev.GraphQLCost != want {
		t.Fatalf("GraphQLCost = %v, want %d", derefInt(ev.GraphQLCost), want)
	}
	if line["graphql_cost"] != float64(want) {
		t.Errorf("serialized graphql_cost = %v, want %d", line["graphql_cost"], want)
	}
}

// costPage is one connection page whose own rateLimit.cost is cost.
func costPage(cost int, field string, hasNext bool, cursor string) []byte {
	// A page that promises another one must carry a node to page from.
	nodes := ""
	if hasNext {
		nodes = "{}"
	}
	return []byte(fmt.Sprintf(`{"data":{"rateLimit":{"cost":%d},"repository":{"pullRequest":{%q:{"totalCount":0,"pageInfo":{"hasNextPage":%t,"endCursor":%q},"nodes":[%s]}}}}}`,
		cost, field, hasNext, cursor, nodes))
}

// TestGetFiles_LogsCostSummedAcrossPages: the files row carries the sum of
// every page's own rateLimit.cost.
func TestGetFiles_LogsCostSummedAcrossPages(t *testing.T) {
	p := NewWithRunner(&sequencedGH{responses: [][]byte{
		costPage(2, "files", true, "C1"),
		costPage(3, "files", false, ""),
	}})
	ev, line := instrumentedOp(t, "files", func(ctx context.Context) error {
		_, err := p.GetFiles(ctx, "o/r", 1)
		return err
	})
	requireCost(t, ev, line, 5)
}

func TestGetCommits_LogsCostSummedAcrossPages(t *testing.T) {
	p := NewWithRunner(&sequencedGH{responses: [][]byte{
		costPage(1, "commits", true, "C1"),
		costPage(1, "commits", false, ""),
	}})
	ev, line := instrumentedOp(t, "commits", func(ctx context.Context) error {
		_, err := p.GetCommits(ctx, "o/r", 1)
		return err
	})
	requireCost(t, ev, line, 2)
}

// TestShowReads_LogCostOfEveryGraphQLDocument: the review-context reads show
// makes (review threads, issue comments, reviews) each add their own cost.
func TestShowReads_LogCostOfEveryGraphQLDocument(t *testing.T) {
	p := NewWithRunner(&sequencedGH{responses: [][]byte{
		costPage(1, "reviewThreads", false, ""),
		costPage(1, "comments", false, ""),
		costPage(2, "reviews", false, ""),
	}})
	ev, line := instrumentedOp(t, "show", func(ctx context.Context) error {
		if _, err := p.ListCommentsReport(ctx, "o/r", 1); err != nil {
			return err
		}
		_, err := p.ListReviewsReport(ctx, "o/r", 1)
		return err
	})
	requireCost(t, ev, line, 4)
}

// A document that reports no rateLimit adds nothing, and the row still carries
// an explicit 0 rather than omitting the field.
func TestFiles_NoRateLimitInResponseLogsZeroCost(t *testing.T) {
	gh := newFakeGH()
	gh.responses["api graphql"] = []byte(`{"data":{"repository":{"pullRequest":{"files":{"totalCount":0,"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}}`)
	p := NewWithRunner(gh)
	ev, line := instrumentedOp(t, "files", func(ctx context.Context) error {
		_, err := p.GetFiles(ctx, "o/r", 1)
		return err
	})
	requireCost(t, ev, line, 0)
}

// The queries this connector sends for show/files/commits all select the cost;
// a document that stops selecting it would silently log 0.
func TestPerPRReadDocuments_SelectRateLimitCost(t *testing.T) {
	for name, q := range map[string]string{
		"files":         prFilesPageQuery,
		"commits":       prCommitsPageQuery,
		"reviews":       reviewsPageQuery,
		"threads":       reviewThreadsPageQuery,
		"threadComment": threadCommentsPageQuery,
		"issueComments": issueCommentsPageQuery,
	} {
		if !regexp.MustCompile(`rateLimit \{[^}]*\bcost\b`).MatchString(q) {
			t.Errorf("%s document does not select rateLimit { cost ... }", name)
		}
	}
}

func derefInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

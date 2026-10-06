package github

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The cheap PR list's field set: what the batched search selects, what it
// costs, and the pin that ties the selection to schema.PRListFields.

// pullRequestFragmentFields returns the top-level field names selected inside
// searchBatchedQuery's `... on PullRequest { ... }` fragment: every identifier
// at brace depth 1, skipping arguments in parentheses and the bodies of nested
// selections.
func pullRequestFragmentFields(t *testing.T) []string {
	t.Helper()
	const marker = "... on PullRequest {"
	start := strings.Index(searchBatchedQuery, marker)
	if start < 0 {
		t.Fatalf("searchBatchedQuery has no %q fragment", marker)
	}
	body := searchBatchedQuery[start+len(marker):]
	depth, parens := 1, 0
	var fields []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			if depth == 1 && parens == 0 {
				fields = append(fields, cur.String())
			}
			cur.Reset()
		}
	}
	for _, r := range body {
		switch {
		case r == '(':
			flush()
			parens++
		case r == ')':
			flush()
			parens--
		case r == '{':
			flush()
			depth++
		case r == '}':
			flush()
			depth--
			if depth == 0 {
				return fields
			}
		case r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9':
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	t.Fatal("searchBatchedQuery's PullRequest fragment never closes")
	return nil
}

// selectionToSchemaFields maps each field the batched search selects to the
// schema.PR JSON names it fills. A selected field with no entry here fails
// TestPRListFields_MatchBatchedSelection, so a new selection cannot ship
// without naming the schema fields it feeds (and, through them, the list
// fingerprint).
var selectionToSchemaFields = map[string][]string{
	"id":             {"node_id"},
	"number":         {"id", "number"},
	"title":          {"title"},
	"url":            {"url"},
	"state":          {"state"},
	"body":           {"body"},
	"isDraft":        {"draft"},
	"updatedAt":      {"updated_at"},
	"reviewDecision": {"review_decision"},
	"mergeable":      {"mergeable"},
	"author":         {"author"},
	"repository":     {"repo"},
	"labels":         {"labels", "label_count"},
	"comments":       {"comment_count"},
	"reviews":        {"review_count"},
	"reviewThreads":  {"review_thread_count"},
	"headRefOid":     {"head_sha"},
	"commits":        {"checks_rollup"},
}

// backendStampedListFields are the schema.PR fields every list entity carries
// regardless of the search selection: the read's own as-of time and staleness.
var backendStampedListFields = []string{"as_of", "stale"}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// TestPRListFields_MatchBatchedSelection fails when schema.PRListFields and the
// batched search selection disagree, in either direction.
func TestPRListFields_MatchBatchedSelection(t *testing.T) {
	want := map[string]bool{}
	for _, f := range backendStampedListFields {
		want[f] = true
	}
	for _, sel := range pullRequestFragmentFields(t) {
		names, ok := selectionToSchemaFields[sel]
		if !ok {
			t.Errorf("the batched search selects %q, which selectionToSchemaFields does not map to a schema.PR field; "+
				"add the mapping and schema.PRListFields together", sel)
			continue
		}
		for _, n := range names {
			want[n] = true
		}
	}
	for sel := range selectionToSchemaFields {
		found := false
		for _, f := range pullRequestFragmentFields(t) {
			found = found || f == sel
		}
		if !found {
			t.Errorf("selectionToSchemaFields maps %q, but the batched search no longer selects it", sel)
		}
	}

	got := map[string]bool{}
	for _, f := range schema.PRListFields {
		if got[f] {
			t.Errorf("schema.PRListFields lists %q twice", f)
		}
		got[f] = true
	}
	var wantList, gotList []string
	for f := range want {
		wantList = append(wantList, f)
	}
	for f := range got {
		gotList = append(gotList, f)
	}
	if strings.Join(sorted(wantList), ",") != strings.Join(sorted(gotList), ",") {
		t.Fatalf("schema.PRListFields disagrees with the batched search selection:\n  selection implies: %v\n  PRListFields:      %v",
			sorted(wantList), sorted(gotList))
	}
}

// TestSearchBatchedQuery_CheapListFieldSet pins what the list query requests
// and, as important, what it must not: nothing that adds points per page or
// makes the search time out.
func TestSearchBatchedQuery_CheapListFieldSet(t *testing.T) {
	gh := &sequencedGH{responses: [][]byte{[]byte(`{"data":{"search":{"pageInfo":{"hasNextPage":false},"nodes":[]}}}`)}}
	if _, err := NewWithRunner(gh).SearchPRsEnriched(context.Background(), "is:open"); err != nil {
		t.Fatalf("SearchPRsEnriched: %v", err)
	}
	sent := strings.Join(gh.calls[0], " ")

	for _, want := range []string{
		"reviewThreads { totalCount }",
		"labels(first: 20) { totalCount nodes { name } }",
		"rateLimit { cost remaining resetAt }",
	} {
		if !strings.Contains(sent, want) {
			t.Errorf("the batched search request must contain %q: %v", want, gh.calls[0])
		}
	}
	for _, banned := range []string{"reviewRequests", "mergeStateStatus", "contexts", "checkRuns", "checkSuites"} {
		if strings.Contains(sent, banned) {
			t.Errorf("the batched search request must not contain %q: %v", banned, gh.calls[0])
		}
	}
	// The only CI selection is the existing statusCheckRollup { state }.
	if n := strings.Count(sent, "statusCheckRollup"); n != 1 {
		t.Fatalf("expected exactly one statusCheckRollup selection, got %d", n)
	}
	if !regexp.MustCompile(`statusCheckRollup\s*\{\s*state\s*\}`).MatchString(sent) {
		t.Errorf("statusCheckRollup must select only { state }: %v", gh.calls[0])
	}
}

const sampleBatchedPageWithCounts = `{
  "data": {
    "rateLimit": {"cost": 2, "remaining": 4990, "resetAt": "2026-10-06T13:00:00Z"},
    "search": {
      "pageInfo": {"hasNextPage": false, "endCursor": ""},
      "nodes": [
        {
          "id": "PR_kwDOSynthetic9", "number": 9, "title": "T", "url": "https://example.invalid/o/r/pull/9",
          "state": "OPEN", "body": "b", "isDraft": true, "updatedAt": "2026-10-05T10:00:00Z",
          "reviewDecision": "APPROVED", "mergeable": "MERGEABLE",
          "author": {"login": "octocat"}, "repository": {"nameWithOwner": "o/r"},
          "labels": {"totalCount": 31, "nodes": [{"name": "bug"}]},
          "comments": {"totalCount": 4}, "reviews": {"totalCount": 2}, "reviewThreads": {"totalCount": 6},
          "headRefOid": "cafe", "commits": {"nodes": [{"commit": {"statusCheckRollup": {"state": "SUCCESS"}}}]}
        }
      ]
    }
  }
}`

func TestSearchPRsEnriched_CarriesReviewThreadAndLabelTotals(t *testing.T) {
	gh := &sequencedGH{responses: [][]byte{[]byte(sampleBatchedPageWithCounts)}}
	prs, err := NewWithRunner(gh).SearchPRsEnriched(context.Background(), "is:open")
	if err != nil {
		t.Fatalf("SearchPRsEnriched: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %+v", prs)
	}
	if prs[0].ReviewThreadCount != 6 {
		t.Errorf("ReviewThreadCount = %d, want 6", prs[0].ReviewThreadCount)
	}
	// LabelCount is the connection's own total (31), not the one name returned.
	if prs[0].LabelCount != 31 || len(prs[0].Labels) != 1 {
		t.Errorf("LabelCount = %d, Labels = %v, want 31 and one name", prs[0].LabelCount, prs[0].Labels)
	}
}

// costSink collects the events an instrumented handler writes.
type costSink struct {
	mu     sync.Mutex
	events []eventlog.Event
}

func (c *costSink) Write(ev eventlog.Event) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
}

// instrumentedList runs fn as the "list" op of an instrumented dispatch table
// and returns the one event it logged, serialized the way the log line is.
func instrumentedList(t *testing.T, fn func(ctx context.Context) error) (eventlog.Event, map[string]any) {
	t.Helper()
	sink := &costSink{}
	table := scriptout.DispatchTable{"list": scriptout.OpHandler{
		SchemaVersion: 1,
		Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
			return nil, fn(ctx)
		},
	}}
	wrapped := eventlog.Instrument(table, sink, "test", time.Now)
	_, _ = wrapped["list"].Handle(context.Background(), nil)
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

// TestSearchPRsEnriched_LogsCostSummedAcrossPages shows the list row carries the
// sum of every search request's own cost.
func TestSearchPRsEnriched_LogsCostSummedAcrossPages(t *testing.T) {
	page1 := `{"data":{"rateLimit":{"cost":2,"remaining":4000,"resetAt":"x"},"search":{"pageInfo":{"hasNextPage":true,"endCursor":"C1"},"nodes":[
	  {"number":1,"url":"u","state":"OPEN","repository":{"nameWithOwner":"o/r"}}]}}}`
	page2 := `{"data":{"rateLimit":{"cost":2,"remaining":3998,"resetAt":"x"},"search":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[
	  {"number":2,"url":"u","state":"OPEN","repository":{"nameWithOwner":"o/r"}}]}}}`
	p := NewWithRunner(&sequencedGH{responses: [][]byte{[]byte(page1), []byte(page2)}})

	ev, line := instrumentedList(t, func(ctx context.Context) error {
		_, err := p.SearchPRsEnriched(ctx, "is:open")
		return err
	})
	if ev.GraphQLCost == nil || *ev.GraphQLCost != 4 {
		t.Fatalf("GraphQLCost = %v, want 4 (2 + 2 over two pages)", ev.GraphQLCost)
	}
	if line["graphql_cost"] != float64(4) {
		t.Errorf("serialized graphql_cost = %v, want 4", line["graphql_cost"])
	}
}

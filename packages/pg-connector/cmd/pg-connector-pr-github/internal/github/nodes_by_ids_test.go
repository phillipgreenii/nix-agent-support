package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The batched fetch by node id (bead pg2-12yur): the document and its 74-id
// chunk size, chunking, and the failure modes a chunk can have.

// pinnedNodesByIDsQuery is the whole by-ids document, whitespace normalized,
// with %s where the ids go. The 74-id chunk size was measured for this field
// set (spec: "refresh by ids", 1 point for 1 to 74 ids, 2 for 75 to 100), so
// any change to the document can move that boundary.
const pinnedNodesByIDsQuery = `query { rateLimit { cost remaining resetAt } nodes(ids: [%s]) { ... on PullRequest { id number title url state body isDraft updatedAt reviewDecision mergeable author { login } repository { nameWithOwner } labels(first: 20) { totalCount nodes { name } } comments { totalCount } reviews { totalCount } reviewThreads { totalCount } headRefOid commits(last: 1) { nodes { commit { statusCheckRollup { state } } } } } } }`

func TestNodesBatchedChunkSize_Is74(t *testing.T) {
	if nodesBatchedChunkSize != 74 {
		t.Fatalf("nodesBatchedChunkSize = %d, want 74: the cost step from 1 to 2 points sits at 75 ids for this field set; re-measure with rateLimit(dryRun: true) before changing it", nodesBatchedChunkSize)
	}
}

func TestNodesByIDsQuery_PinnedFieldSet(t *testing.T) {
	got := strings.Join(strings.Fields(nodesByIDsQuery), " ")
	if got != pinnedNodesByIDsQuery {
		t.Fatalf("nodesByIDsQuery changed. The 74-id chunk size was measured for the pinned document only; "+
			"re-measure the cost boundary (rateLimit(dryRun: true) at 74 and 75 ids), then update nodesBatchedChunkSize and pinnedNodesByIDsQuery together.\n got: %s\nwant: %s", got, pinnedNodesByIDsQuery)
	}
}

// The by-ids document and the batched search document MUST select the same
// PullRequest fragment, so the summary a refresher fetches by id is the summary
// the list returns, field for field.
func TestNodesByIDsQuery_SharesTheSearchFragment(t *testing.T) {
	frag := strings.Join(strings.Fields(pullRequestSummaryFragment), " ")
	for name, doc := range map[string]string{"search": searchBatchedQuery, "nodes": nodesByIDsQuery} {
		if !strings.Contains(strings.Join(strings.Fields(doc), " "), frag) {
			t.Errorf("the %s document does not embed pullRequestSummaryFragment", name)
		}
	}
}

// bodyAndErrGH answers every call with the given stdout AND error, which is how
// the real runner reports a failed gh run: it still hands back what gh printed.
type bodyAndErrGH struct {
	body []byte
	err  error
}

func (g bodyAndErrGH) Run(context.Context, ...string) ([]byte, error) { return g.body, g.err }

func (g bodyAndErrGH) RunStdin(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	return g.Run(ctx, args...)
}

func nodeJSON(id string, number int) string {
	return fmt.Sprintf(`{"id":%q,"number":%d,"title":"t%d","url":"u","state":"OPEN","repository":{"nameWithOwner":"o/r"},"headRefOid":"sha%d","updatedAt":"2026-10-01T00:00:00Z"}`, id, number, number, number)
}

func nodesResponse(cost, remaining int, nodes ...string) []byte {
	return []byte(fmt.Sprintf(`{"data":{"rateLimit":{"cost":%d,"remaining":%d,"resetAt":"x"},"nodes":[%s]}}`, cost, remaining, strings.Join(nodes, ",")))
}

func idsList(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("PR_%d", i+1)
	}
	return out
}

func TestSummariesByNodeIDs_ParsesSummariesKeyedByNodeID(t *testing.T) {
	gh := &sequencedGH{responses: [][]byte{nodesResponse(1, 4000, nodeJSON("PR_1", 1), nodeJSON("PR_2", 2))}}
	got, err := NewWithRunner(gh).SummariesByNodeIDs(context.Background(), []string{"PR_1", "PR_2"}, nil)
	if err != nil {
		t.Fatalf("SummariesByNodeIDs: %v", err)
	}
	if len(got) != 2 || got["PR_1"].Number != 1 || got["PR_2"].HeadSHA != "sha2" || got["PR_1"].Repo != "o/r" || got["PR_1"].NodeID != "PR_1" {
		t.Fatalf("got %+v", got)
	}
	if len(gh.calls) != 1 {
		t.Fatalf("gh calls = %d, want 1", len(gh.calls))
	}
	if doc := strings.Join(gh.calls[0], " "); !strings.Contains(doc, `nodes(ids: ["PR_1", "PR_2"])`) {
		t.Fatalf("gh args = %v, want the ids written into the document", gh.calls[0])
	}
}

// 150 ids are three documents of 74, 74 and 2, in order: never one document
// over the 74-id boundary, which would cost 2 points.
func TestSummariesByNodeIDs_ChunksAtTheBoundary(t *testing.T) {
	ids := idsList(150)
	respond := func(from, to int) []byte {
		var nodes []string
		for i := from; i < to; i++ {
			nodes = append(nodes, nodeJSON(ids[i], i+1))
		}
		return nodesResponse(1, 4000, nodes...)
	}
	gh := &sequencedGH{responses: [][]byte{respond(0, 74), respond(74, 148), respond(148, 150)}}
	got, err := NewWithRunner(gh).SummariesByNodeIDs(context.Background(), ids, nil)
	if err != nil {
		t.Fatalf("SummariesByNodeIDs: %v", err)
	}
	if len(got) != 150 {
		t.Fatalf("got %d summaries, want 150", len(got))
	}
	if len(gh.calls) != 3 {
		t.Fatalf("gh calls = %d, want 3 (74 + 74 + 2)", len(gh.calls))
	}
	for i, want := range []int{74, 74, 2} {
		doc := strings.Join(gh.calls[i], " ")
		if n := strings.Count(doc, `"PR_`); n != want {
			t.Errorf("document %d names %d ids, want %d", i, n, want)
		}
	}
}

func TestSummariesByNodeIDs_DuplicateIDsAreRequestedOnce(t *testing.T) {
	gh := &sequencedGH{responses: [][]byte{nodesResponse(1, 4000, nodeJSON("PR_1", 1))}}
	if _, err := NewWithRunner(gh).SummariesByNodeIDs(context.Background(), []string{"PR_1", "PR_1"}, nil); err != nil {
		t.Fatalf("SummariesByNodeIDs: %v", err)
	}
	if doc := strings.Join(gh.calls[0], " "); strings.Count(doc, `"PR_1"`) != 1 {
		t.Fatalf("document names PR_1 more than once: %v", gh.calls[0])
	}
}

func TestSummariesByNodeIDs_AnInvalidIDNeverReachesTheDocument(t *testing.T) {
	gh := &sequencedGH{}
	_, err := NewWithRunner(gh).SummariesByNodeIDs(context.Background(), []string{`PR_a"]) { x }`}, nil)
	if err == nil || len(gh.calls) != 0 {
		t.Fatalf("err = %v, gh calls = %d, want an error and no gh call", err, len(gh.calls))
	}
}

// A null node, and a node that is not a pull request, are absent from the map
// and are not a failure.
func TestSummariesByNodeIDs_NullAndNonPullRequestNodesAreAbsent(t *testing.T) {
	gh := &sequencedGH{responses: [][]byte{nodesResponse(1, 4000, nodeJSON("PR_1", 1), "null", "{}")}}
	got, err := NewWithRunner(gh).SummariesByNodeIDs(context.Background(), []string{"PR_1", "PR_2", "I_3"}, nil)
	if err != nil {
		t.Fatalf("SummariesByNodeIDs: %v", err)
	}
	if len(got) != 1 || got["PR_1"].Number != 1 {
		t.Fatalf("got %+v, want only PR_1", got)
	}
}

// gh exits non-zero for a deleted node but still prints the response; a response
// whose only errors are per-node lookup failures is a partial answer, not a
// failure.
func TestSummariesByNodeIDs_APerNodeLookupErrorIsAPartialAnswer(t *testing.T) {
	body := []byte(`{"data":{"rateLimit":{"cost":1,"remaining":4000,"resetAt":"x"},"nodes":[` + nodeJSON("PR_1", 1) + `,null]},` +
		`"errors":[{"type":"NOT_FOUND","path":["nodes",1],"message":"Could not resolve to a node with the global id of 'PR_2'"}]}`)
	gh := bodyAndErrGH{body: body, err: errors.New("gh api graphql: exit status 1: Could not resolve to a node")}
	got, err := NewWithRunner(gh).SummariesByNodeIDs(context.Background(), []string{"PR_1", "PR_2"}, nil)
	if err != nil {
		t.Fatalf("SummariesByNodeIDs: %v, want the partial answer honoured", err)
	}
	if len(got) != 1 || got["PR_1"].Number != 1 {
		t.Fatalf("got %+v, want only PR_1", got)
	}
}

// Any other failure fails the whole call and returns NO result, even when an
// earlier chunk succeeded.
func TestSummariesByNodeIDs_AFailedChunkFailsTheCallWithNoPartialResult(t *testing.T) {
	ids := idsList(80)
	var first []string
	for i := 0; i < 74; i++ {
		first = append(first, nodeJSON(ids[i], i+1))
	}
	boom := errors.New("gh api graphql: exit status 1: HTTP 502")
	gh := &sequencedGH{responses: [][]byte{nodesResponse(1, 4000, first...), nil}, errs: []error{nil, boom}}
	got, err := NewWithRunner(gh).SummariesByNodeIDs(context.Background(), ids, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the chunk's failure", err)
	}
	if got != nil {
		t.Fatalf("got %d summaries, want none from a failed call", len(got))
	}
}

func TestSummariesByNodeIDs_ErrorsOtherThanPerNodeLookupFailAndAreNotMistakenForOne(t *testing.T) {
	cases := map[string]string{
		"rate limited":               `{"data":{"nodes":[null]},"errors":[{"type":"RATE_LIMITED","path":["nodes",0],"message":"API rate limit exceeded"}]}`,
		"not found outside nodes":    `{"data":{"nodes":[null]},"errors":[{"type":"NOT_FOUND","path":["viewer"],"message":"x"}]}`,
		"lookup plus a real failure": `{"data":{"nodes":[null]},"errors":[{"type":"NOT_FOUND","path":["nodes",0],"message":"Could not resolve to a node"},{"type":"RATE_LIMITED","message":"slow down"}]}`,
	}
	for name, body := range cases {
		for _, runErr := range []error{nil, errors.New("exit status 1")} {
			t.Run(fmt.Sprintf("%s runErr=%v", name, runErr != nil), func(t *testing.T) {
				gh := bodyAndErrGH{body: []byte(body), err: runErr}
				got, err := NewWithRunner(gh).SummariesByNodeIDs(context.Background(), []string{"PR_1"}, nil)
				if err == nil || got != nil {
					t.Fatalf("got %+v, %v, want a failure and no result", got, err)
				}
			})
		}
	}
}

func TestSummariesByNodeIDs_MisalignedOrUnparseableAnswerIsAFailure(t *testing.T) {
	for name, body := range map[string][]byte{
		"fewer nodes than ids": nodesResponse(1, 4000, nodeJSON("PR_1", 1)),
		"not json":             []byte(`<html>`),
		"empty":                nil,
	} {
		t.Run(name, func(t *testing.T) {
			gh := &sequencedGH{responses: [][]byte{body}}
			got, err := NewWithRunner(gh).SummariesByNodeIDs(context.Background(), []string{"PR_1", "PR_2"}, nil)
			if err == nil || got != nil {
				t.Fatalf("got %+v, %v, want a failure", got, err)
			}
		})
	}
}

// The gate sees each document's own reading before that document's results are
// used or the next is requested; a refusal returns no result and stops.
func TestSummariesByNodeIDs_GateJudgesEveryChunkAndRefusalStops(t *testing.T) {
	ids := idsList(80)
	var first, second []string
	for i := 0; i < 74; i++ {
		first = append(first, nodeJSON(ids[i], i+1))
	}
	for i := 74; i < 80; i++ {
		second = append(second, nodeJSON(ids[i], i+1))
	}
	gh := &sequencedGH{responses: [][]byte{nodesResponse(1, 4000, first...), nodesResponse(1, 10, second...)}}
	var readings []int
	refuse := errors.New("below reserve")
	got, err := NewWithRunner(gh).SummariesByNodeIDs(context.Background(), ids, func(rl RateLimit) error {
		readings = append(readings, rl.Remaining)
		if rl.Remaining < 100 {
			return refuse
		}
		return nil
	})
	if !errors.Is(err, refuse) || got != nil {
		t.Fatalf("got %d summaries, err %v, want the gate's refusal and no result", len(got), err)
	}
	if len(readings) != 2 || readings[0] != 4000 || readings[1] != 10 {
		t.Fatalf("readings = %v, want one per document, in order", readings)
	}

	// A response with no reading is a failure when a gate is set (fail closed).
	noReading := []byte(`{"data":{"nodes":[` + nodeJSON("PR_1", 1) + `]}}`)
	gh = &sequencedGH{responses: [][]byte{noReading}}
	if _, err := NewWithRunner(gh).SummariesByNodeIDs(context.Background(), []string{"PR_1"}, func(RateLimit) error { return nil }); err == nil {
		t.Fatal("a response without a rateLimit reading must fail closed when gated")
	}
}

// The ids-only list's search asks gh for each result's node id and carries it
// on the PR, so the refresher has a node id for every member it reads.
func TestSearchPRs_RequestsAndCarriesTheNodeID(t *testing.T) {
	gh := &sequencedGH{responses: [][]byte{[]byte(`[{"id":"PR_kwDOSynthetic1","number":1,"url":"u","state":"OPEN","repository":{"nameWithOwner":"o/r"}}]`)}}
	prs, err := NewWithRunner(gh).SearchPRs(context.Background(), "is:open")
	if err != nil {
		t.Fatalf("SearchPRs: %v", err)
	}
	if len(prs) != 1 || prs[0].NodeID != "PR_kwDOSynthetic1" {
		t.Fatalf("prs = %+v, want the node id carried", prs)
	}
	joined := strings.Join(gh.calls[0], " ")
	if !strings.Contains(joined, "--json id,") {
		t.Fatalf("gh args = %q, want --json to request id", joined)
	}
}

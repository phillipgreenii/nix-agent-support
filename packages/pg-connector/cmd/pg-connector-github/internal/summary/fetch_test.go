package summary

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

func newFetcher(t *testing.T, gh *countingGH) (*Fetcher, *governor.Governor) {
	t.Helper()
	clk := governor.NewManualClock(t0)
	gov, err := governor.New(ratefeed.GovernorConfig(config.Defaults().Settings), governor.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	return &Fetcher{GH: gh, Governor: gov, Feed: ratefeed.New(gov, gh, clk)}, gov
}

var background = ratefeed.Meta{Class: governor.ClassKeepAlive, Purpose: governor.PurposeRefresh}

// idsOf returns the node ids a call carries (its `ids[]=` flags).
func idsOf(args []string) []string {
	var ids []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-f" && strings.HasPrefix(args[i+1], "ids[]=") {
			ids = append(ids, strings.TrimPrefix(args[i+1], "ids[]="))
		}
	}
	return ids
}

func TestFetch_DecodesTheFieldSet(t *testing.T) {
	gh := &countingGH{answer: func([]string) ([]byte, error) { return fixture(t, "nodes_two.json"), nil }}
	f, gov := newFetcher(t, gh)
	got, cost, err := f.Fetch(context.Background(), background, []string{"PR_a", "PR_b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(gh.calls) != 1 || cost != 1 || len(got) != 2 {
		t.Fatalf("calls %d cost %d summaries %d, want 1 1 2", len(gh.calls), cost, len(got))
	}
	a := got[0]
	if a.NodeID != "PR_a" || a.Ref() != "Acme-Org/Widget#3" || a.Title != "Add the widget" || a.State != "OPEN" ||
		a.Mergeable != "MERGEABLE" || a.Author != "phillipgreenii" || a.CommentCount != 4 || a.ReviewCount != 1 ||
		a.ReviewThreadCount != 2 || a.LabelCount != 2 || a.HeadRefOid != "head-1" || a.HeadRefName != "feature" ||
		a.BaseRefName != "main" || a.BaseRefOid != "base-1" || a.ChecksState != "SUCCESS" || a.UpdatedAt != "2026-10-12T08:00:00Z" {
		t.Errorf("summary a = %+v", a)
	}
	b := got[1]
	if b.Author != "" || b.ChecksState != "" || !b.IsDraft || b.Mergeable != "UNKNOWN" || len(b.Labels) != 0 {
		t.Errorf("summary b = %+v", b)
	}
	if bs, _ := gov.State().Bucket(governor.BucketGraphQL); bs.Spent != 1 || bs.Remaining != 4900 {
		t.Errorf("governor saw spent %d remaining %d, want 1 and 4900", bs.Spent, bs.Remaining)
	}
	if !strings.Contains(strings.Join(gh.calls[0], " "), "api graphql") {
		t.Errorf("args = %v", gh.calls[0])
	}
}

func TestFetch_NeverMoreThan74IdsPerCall(t *testing.T) {
	gh := &countingGH{answer: func(args []string) ([]byte, error) {
		var nodes []string
		for _, id := range idsOf(args) {
			nodes = append(nodes, fmt.Sprintf(`{"id":%q,"number":1,"repository":{"nameWithOwner":"o/r"}}`, id))
		}
		return []byte(`{"data":{"rateLimit":{"cost":1,"remaining":4900,"resetAt":"2026-10-12T10:00:00Z"},"nodes":[` + strings.Join(nodes, ",") + `]}}`), nil
	}}
	f, _ := newFetcher(t, gh)
	for _, n := range []int{1, 74, 75, 148, 149} {
		gh.calls = nil
		ids := make([]string, n)
		for i := range ids {
			ids[i] = fmt.Sprintf("PR_%d", i)
		}
		got, cost, err := f.Fetch(context.Background(), background, ids)
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		wantCalls := (n + 73) / 74
		if len(gh.calls) != wantCalls || cost != int64(wantCalls) {
			t.Errorf("n=%d: %d calls, cost %d, want %d each", n, len(gh.calls), cost, wantCalls)
		}
		seen := 0
		for _, c := range gh.calls {
			if k := len(idsOf(c)); k > 74 {
				t.Errorf("n=%d: a call carries %d ids", n, k)
			}
			seen += len(idsOf(c))
		}
		if seen != n || len(got) != n {
			t.Errorf("n=%d: sent %d ids, returned %d summaries", n, seen, len(got))
		}
		for i, s := range got {
			if s.NodeID != ids[i] {
				t.Fatalf("n=%d: summary %d is %s, want %s (order must be kept)", n, i, s.NodeID, ids[i])
			}
		}
	}
}

func TestFetch_ConfiguredBatchSizeLowersButNeverRaisesTheLimit(t *testing.T) {
	f := &Fetcher{BatchSize: 10}
	if f.batchSize() != 10 {
		t.Errorf("batchSize = %d, want 10", f.batchSize())
	}
	f.BatchSize = 500
	if f.batchSize() != 74 {
		t.Errorf("batchSize = %d, want it clamped to 74", f.batchSize())
	}
	f.BatchSize = 0
	if f.batchSize() != config.DefaultBatchSize || config.DefaultBatchSize != 74 {
		t.Errorf("default batchSize = %d", f.batchSize())
	}
}

func TestFetch_MissingNodesAreGoneNotErrors(t *testing.T) {
	gh := &countingGH{answer: func([]string) ([]byte, error) {
		return fixture(t, "nodes_gone.json"), errors.New("gh api graphql: exit status 1")
	}}
	f, _ := newFetcher(t, gh)
	got, _, err := f.Fetch(context.Background(), background, []string{"PR_deleted", "ISSUE_1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].Gone || got[0].NodeID != "PR_deleted" || !got[1].Gone || got[1].NodeID != "ISSUE_1" {
		t.Errorf("summaries = %+v", got)
	}
}

func TestFetch_Failures(t *testing.T) {
	boom := errors.New("gh api graphql: exit status 1")
	for name, answer := range map[string]func([]string) ([]byte, error){
		"gh failed": func([]string) ([]byte, error) { return nil, boom },
		"other gql error": func([]string) ([]byte, error) {
			return []byte(`{"data":{"nodes":[null]},"errors":[{"type":"FORBIDDEN"}]}`), boom
		},
		"not json":         func([]string) ([]byte, error) { return []byte("nope"), nil },
		"wrong node count": func([]string) ([]byte, error) { return []byte(`{"data":{"nodes":[]}}`), nil },
	} {
		f, _ := newFetcher(t, &countingGH{answer: answer})
		if _, _, err := f.Fetch(context.Background(), background, []string{"PR_a"}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestFetch_StopsAtAFailingBatchKeepingEarlierResults(t *testing.T) {
	n := 0
	gh := &countingGH{answer: func(args []string) ([]byte, error) {
		n++
		if n == 2 {
			return nil, errors.New("gh api graphql: exit status 1")
		}
		var nodes []string
		for _, id := range idsOf(args) {
			nodes = append(nodes, fmt.Sprintf(`{"id":%q,"number":1,"repository":{"nameWithOwner":"o/r"}}`, id))
		}
		return []byte(`{"data":{"rateLimit":{"cost":1,"remaining":4900,"resetAt":"2026-10-12T10:00:00Z"},"nodes":[` + strings.Join(nodes, ",") + `]}}`), nil
	}}
	f, _ := newFetcher(t, gh)
	ids := make([]string, 80)
	for i := range ids {
		ids[i] = fmt.Sprintf("PR_%d", i)
	}
	got, cost, err := f.Fetch(context.Background(), background, ids)
	if err == nil || len(got) != 74 || cost != 1 {
		t.Errorf("err %v, %d summaries, cost %d; want an error, the first 74 and cost 1", err, len(got), cost)
	}
}

func TestFetch_GovernorRefusalMakesNoCall(t *testing.T) {
	gh := &countingGH{answer: func([]string) ([]byte, error) { return nil, errors.New("must not run") }}
	f, gov := newFetcher(t, gh)
	gov.PauseAll(time.Minute, "test pause")
	_, _, err := f.Fetch(context.Background(), background, []string{"PR_a"})
	if !errors.Is(err, governor.ErrRefused) || len(gh.calls) != 0 {
		t.Errorf("err = %v with %d calls, want a refusal and no call", err, len(gh.calls))
	}
}

// The document is the batched search field set plus three fields. Changing it
// moves the points boundary behind MaxBatch, so it is pinned.
func TestDocument_Pinned(t *testing.T) {
	if !strings.Contains(document, github.BatchedPRFields) {
		t.Fatal("the document must carry the batched search field set verbatim")
	}
	for _, f := range []string{"headRefName", "baseRefName", "baseRefOid", "nodes(ids: $ids)", "rateLimit { cost remaining resetAt }"} {
		if !strings.Contains(document, f) {
			t.Errorf("document lacks %q", f)
		}
	}
	const want = "query($ids: [ID!]!) { rateLimit { cost remaining resetAt } nodes(ids: $ids) { ... on PullRequest { " +
		"id number title url state body isDraft updatedAt reviewDecision mergeable author { login } " +
		"repository { nameWithOwner } labels(first: 20) { totalCount nodes { name } } comments { totalCount } " +
		"reviews { totalCount } reviewThreads { totalCount } headRefOid " +
		"commits(last: 1) { nodes { commit { statusCheckRollup { state } } } } headRefName baseRefName baseRefOid } } }"
	if got := strings.Join(strings.Fields(document), " "); got != want {
		t.Fatalf("document changed. The 74-id boundary was measured for the pinned document only; re-measure "+
			"(rateLimit(dryRun: true) at 74 and 75 ids), then update MaxBatch and this pin together.\n got: %s\nwant: %s", got, want)
	}
	if MaxBatch != 74 {
		t.Errorf("MaxBatch = %d, want 74", MaxBatch)
	}
}

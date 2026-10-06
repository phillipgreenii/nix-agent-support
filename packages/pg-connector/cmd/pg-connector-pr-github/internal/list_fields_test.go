package internal

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// A change to either of the cheap list's two extra totals changes the list
// fingerprint, because the fingerprint hashes the whole summary entity.
func TestListFingerprint_MovesWithReviewThreadAndLabelTotals(t *testing.T) {
	base := api.PR{Repo: "owner/repo", Number: 1, Title: "T", State: "open", ReviewThreadCount: 1, LabelCount: 1}
	same := base
	moreThreads := base
	moreThreads.ReviewThreadCount = 2
	moreLabels := base
	moreLabels.LabelCount = 2

	hash := func(p api.PR) string { return canonicalSummaryHash(t, listOnePR(t, p)) }
	if hash(base) != hash(same) {
		t.Fatal("control: identical input hashed differently")
	}
	if hash(base) == hash(moreThreads) {
		t.Error("review_thread_count 1 -> 2 did not change the fingerprint")
	}
	if hash(base) == hash(moreLabels) {
		t.Error("label_count 1 -> 2 did not change the fingerprint")
	}
}

// batchedRunner answers the rate-limit read and one fully populated batched
// search page, standing in for `gh`.
type batchedRunner struct{}

const fullyPopulatedPage = `{"data":{"rateLimit":{"cost":2,"remaining":4000,"resetAt":"2026-10-06T13:00:00Z"},"search":{
  "pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{
  "id":"PR_kwDOSynthetic1","number":1,"title":"T","url":"https://example.invalid/o/r/pull/1","state":"OPEN",
  "body":"b","isDraft":true,"updatedAt":"2026-10-05T10:00:00Z","reviewDecision":"APPROVED","mergeable":"MERGEABLE",
  "author":{"login":"octocat"},"repository":{"nameWithOwner":"o/r"},
  "labels":{"totalCount":2,"nodes":[{"name":"bug"},{"name":"p1"}]},
  "comments":{"totalCount":3},"reviews":{"totalCount":4},"reviewThreads":{"totalCount":5},
  "headRefOid":"cafe","commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS"}}}]}}]}}}`

func (batchedRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	if strings.Contains(strings.Join(args, " "), "search(query") {
		return []byte(fullyPopulatedPage), nil
	}
	return []byte(`{"data":{"rateLimit":{"remaining":4000,"resetAt":"2026-10-06T13:00:00Z"}}}`), nil
}

func (r batchedRunner) RunStdin(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	return r.Run(ctx, args...)
}

// TestList_PopulatesExactlyPRListFields runs the real GitHub layer over a fully
// populated search page and compares what the list entity carries against
// schema.PRListFields. stale is always emitted (false is its value), so it is
// checked for presence, not for being non-zero.
func TestList_PopulatesExactlyPRListFields(t *testing.T) {
	b := New(github.NewWithRunner(batchedRunner{}))
	got, err := b.List(context.Background(), []string{"is:open"}, false, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Entities) != 1 {
		t.Fatalf("Entities = %+v, want one", got.Entities)
	}
	raw, err := json.Marshal(got.Entities[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}

	var populated []string
	for k, v := range m {
		switch x := v.(type) {
		case nil:
			continue
		case string:
			if x == "" {
				continue
			}
		case float64:
			if x == 0 {
				continue
			}
		case bool:
			if !x && k != "stale" {
				continue
			}
		case []any:
			if len(x) == 0 {
				continue
			}
		}
		populated = append(populated, k)
	}
	sort.Strings(populated)
	want := append([]string(nil), schema.PRListFields...)
	sort.Strings(want)
	if strings.Join(populated, ",") != strings.Join(want, ",") {
		t.Fatalf("the list entity populates %v, but schema.PRListFields is %v", populated, want)
	}
	if m["review_thread_count"] != float64(5) || m["label_count"] != float64(2) {
		t.Errorf("review_thread_count/label_count = %v/%v, want 5/2", m["review_thread_count"], m["label_count"])
	}
}

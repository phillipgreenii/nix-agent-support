package gather

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The PR adapter reads the pending-review state through pg-connector only:
// `pr review pending <id>` for the record; the retired escalation fact is no
// longer read. These tests drive it with a fake pg-connector.

const (
	reviewPRShow    = `{"protocolVersion":1,"schemaVersion":4,"result":{"id":"PR1","repo":"myorg/repo","number":42,"state":"open","merged":false,"head_sha":"sha-AAA","as_of":"2026-09-16T00:00:00Z"}}`
	reviewPRFiles   = `{"protocolVersion":1,"schemaVersion":4,"result":{"id":"PR1","files":[]}}`
	reviewPRCommits = `{"protocolVersion":1,"schemaVersion":4,"result":{"id":"PR1","commits":[]}}`
	reviewCI        = `{"runs":[],"sources":[]}`
	reviewNoBeads   = `{"entities":[],"present_ids":[],"sources":[]}`

	pendingNone    = `{"protocolVersion":1,"schemaVersion":4,"result":{"pending":false,"head_sha":"sha-AAA","as_of":"2026-09-16T00:00:00Z"}}`
	pendingCurrent = `{"protocolVersion":1,"schemaVersion":4,"result":{"pending":true,"head_sha":"sha-AAA","as_of":"2026-09-16T00:00:00Z","review":{"review_id":"PRR_1","commit_sha":"sha-AAA","stale":false}}}`
	pendingStale   = `{"protocolVersion":1,"schemaVersion":4,"result":{"pending":true,"head_sha":"sha-AAA","as_of":"2026-09-16T00:00:00Z","review":{"review_id":"PRR_1","commit_sha":"sha-OLD","stale":true}}}`
	pendingError   = `{"protocolVersion":1,"error":{"code":"unavailable","message":"review_pending: detection_failed: more than one pending review"}}`
)

// reviewBehavior assembles the fake pg-connector script for one PR hydration.
func reviewBehavior(pending string, pendingExit int) string {
	return strings.Join([]string{
		"pr show=0:" + reviewPRShow,
		"pr files=0:" + reviewPRFiles,
		"pr commits=0:" + reviewPRCommits,
		"ci list=0:" + reviewCI,
		"issue list=0:" + reviewNoBeads,
		"pr review=" + itoa(pendingExit) + ":" + pending,
	}, "|")
}

func gatherReview(t *testing.T, behavior string) (f Facts, calls []string) {
	t.Helper()
	rec := entityFactory(t, behavior)
	res, err := NewGatherer(testConfig(""), nil).EntityGatherers()["pr"].GatherEntity(context.Background(), fixturePRKey, ChangeChanged)
	if err != nil {
		t.Fatalf("GatherEntity: %v", err)
	}
	if res.Degraded != "" {
		t.Fatalf("a review lookup must never degrade the hydration, got %q", res.Degraded)
	}
	if err := json.Unmarshal(res.Payload, &f); err != nil {
		t.Fatal(err)
	}
	return f, readCalls(t, rec)
}

func TestPRAdapter_PendingReviewFiveStates(t *testing.T) {
	cases := []struct {
		name        string
		behavior    string
		wantPending string // "" means an Error is expected
		wantErrHas  string
	}{
		{"none", reviewBehavior(pendingNone, 0), `"pending":false`, ""},
		{"current", reviewBehavior(pendingCurrent, 0), `"stale":false`, ""},
		{"stale", reviewBehavior(pendingStale, 0), `"stale":true`, ""},
		{"lookup-failed", reviewBehavior(pendingError, 1), "", "detection_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := gatherReview(t, tc.behavior)
			if f.ReviewPending == nil {
				t.Fatal("no review_pending fact")
			}
			if tc.wantPending != "" {
				if f.ReviewPending.Error != "" || !strings.Contains(string(f.ReviewPending.Result), tc.wantPending) {
					t.Errorf("review_pending = %+v, want result containing %s", f.ReviewPending, tc.wantPending)
				}
			} else if len(f.ReviewPending.Result) != 0 || !strings.Contains(f.ReviewPending.Error, tc.wantErrHas) {
				t.Errorf("review_pending = %+v, want an error containing %q and no result", f.ReviewPending, tc.wantErrHas)
			}
		})
	}
}

func TestPRAdapter_PendingReviewUsesOnlyTheConnectorVerbs(t *testing.T) {
	_, calls := gatherReview(t, reviewBehavior(pendingNone, 0))
	var sawPending bool
	for _, c := range calls {
		if c == "pr review pending "+fixturePRKey {
			sawPending = true
		}
		if strings.Contains(c, "pending-review-escalations") {
			t.Errorf("the retired escalation query was made: %q", c)
		}
		// Read-only: no submit, no issue write.
		if strings.HasPrefix(c, "pr review submit") || strings.HasPrefix(c, "issue create") ||
			strings.HasPrefix(c, "issue update") || strings.HasPrefix(c, "issue comment") || strings.HasPrefix(c, "issue close") {
			t.Errorf("display hydration made a state change: %q", c)
		}
	}
	if !sawPending {
		t.Errorf("calls %v: want `pr review pending`", calls)
	}
}

func TestPRAdapter_PendingReviewNotFoundIsAFailureNotNone(t *testing.T) {
	f, _ := gatherReview(t, reviewBehavior(`{"protocolVersion":1,"error":{"code":"not_found","message":"gone"}}`, 4))
	if f.ReviewPending == nil || len(f.ReviewPending.Result) != 0 || !strings.Contains(f.ReviewPending.Error, "not_found") {
		t.Errorf("review_pending = %+v", f.ReviewPending)
	}
}

func TestPRAdapter_PendingReviewMalformedResultIsAFailure(t *testing.T) {
	f, _ := gatherReview(t, reviewBehavior(`{"protocolVersion":1,"result":{"head_sha":"x"}}`, 0))
	if f.ReviewPending == nil || len(f.ReviewPending.Result) != 0 || f.ReviewPending.Error == "" {
		t.Errorf("review_pending = %+v, want a failure for a result without a pending flag", f.ReviewPending)
	}
}

func TestPRAdapter_RemovedReadCarriesNoReviewState(t *testing.T) {
	rec := entityFactory(t, "pr show=0:"+strings.Replace(reviewPRShow, `"state":"open"`, `"state":"closed"`, 1))
	res, err := NewGatherer(testConfig(""), nil).EntityGatherers()["pr"].GatherEntity(context.Background(), fixturePRKey, ChangeRemoved)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(res.Payload), "review_pending") {
		t.Errorf("removed re-read carries review state: %s", res.Payload)
	}
	if calls := readCalls(t, rec); len(calls) != 1 {
		t.Errorf("calls %v, want only pr show", calls)
	}
}

func TestLegacyGatherNeverReadsReviewState(t *testing.T) {
	// Gather is the legacy sync/run path and stays unchanged: it MUST NOT call
	// the pending-review lookup, so a fake that fails on it still passes.
	rec := entityFactory(t, reviewBehavior(pendingError, 1))
	f, err := NewGatherer(testConfig(""), nil).Gather(context.Background(), "pr", fixturePRKey, ChangeChanged)
	if err != nil {
		t.Fatal(err)
	}
	if f.ReviewPending != nil {
		t.Errorf("legacy Gather populated the review state: %+v", f)
	}
	for _, c := range readCalls(t, rec) {
		if strings.HasPrefix(c, "pr review") {
			t.Errorf("legacy Gather called %q", c)
		}
	}
}

// A stored facts record written while the escalation fact still existed
// carries review_escalations (and digest_state). It decodes without error,
// the pending-review half survives, and re-encoding drops the retired keys.
func TestFactsToleratesRetiredEscalationKeys(t *testing.T) {
	stored := `{"review_pending":{"result":{"pending":false,"head_sha":"sha-AAA"}},` +
		`"review_escalations":{"open":[{"id":"esc-1","kind":"pr","head":"sha-AAA"}],"error":"x"},"digest_state":"unmarked"}`
	var f Facts
	if err := json.Unmarshal([]byte(stored), &f); err != nil {
		t.Fatalf("a stored record with retired keys must decode: %v", err)
	}
	if f.ReviewPending == nil || !strings.Contains(string(f.ReviewPending.Result), `"pending":false`) {
		t.Errorf("review_pending lost: %+v", f.ReviewPending)
	}
	out, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "review_escalations") || strings.Contains(string(out), "digest_state") {
		t.Errorf("retired keys survive re-encoding: %s", out)
	}
}

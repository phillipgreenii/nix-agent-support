package gather

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The PR adapter reads the pending-review state through pg-connector only:
// `pr review pending <id>` for the record and the named escalation query for
// the open escalation beads. These tests drive it with a fake pg-connector.

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

func escalationList(entities ...string) string {
	return `{"entities":[` + strings.Join(entities, ",") + `],"present_ids":[],"sources":[]}`
}

func escalationBead(id, key, extra string) string {
	md := `"review_escalation_key":"` + key + `"`
	if extra != "" {
		md += "," + extra
	}
	return `{"id":"` + id + `","title":"escalation","labels":["human","pending-review-escalation"],"metadata":{` + md + `}}`
}

// reviewBehavior assembles the fake pg-connector script for one PR hydration.
func reviewBehavior(pending string, pendingExit int, issueList string, issueExit int) string {
	return strings.Join([]string{
		"pr show=0:" + reviewPRShow,
		"pr files=0:" + reviewPRFiles,
		"pr commits=0:" + reviewPRCommits,
		"ci list=0:" + reviewCI,
		"issue list=" + itoa(issueExit) + ":" + issueList,
		"pr review=" + itoa(pendingExit) + ":" + pending,
	}, "|")
}

func gatherReview(t *testing.T, behavior string) (Facts, []string) {
	t.Helper()
	f, calls, degraded := gatherReviewRaw(t, behavior)
	if degraded != "" {
		t.Fatalf("a review lookup must never degrade the hydration, got %q", degraded)
	}
	return f, calls
}

// gatherReviewRaw is gatherReview without the not-degraded assertion, for a
// test whose single fake `issue list` also fails the work-beads read.
func gatherReviewRaw(t *testing.T, behavior string) (f Facts, calls []string, degraded string) {
	t.Helper()
	rec := entityFactory(t, behavior)
	res, err := NewGatherer(testConfig(""), nil).EntityGatherers()["pr"].GatherEntity(context.Background(), fixturePRKey, ChangeChanged)
	if err != nil {
		t.Fatalf("GatherEntity: %v", err)
	}
	if err := json.Unmarshal(res.Payload, &f); err != nil {
		t.Fatal(err)
	}
	return f, readCalls(t, rec), res.Degraded
}

func TestPRAdapter_PendingReviewFiveStates(t *testing.T) {
	perPR := escalationBead("esc-1", "pr:"+fixturePRKey, `"review_escalation_head":"sha-AAA"`)
	cases := []struct {
		name        string
		behavior    string
		wantPending string // "" means an Error is expected
		wantErrHas  string
		wantEsc     []string
	}{
		{"none", reviewBehavior(pendingNone, 0, reviewNoBeads, 0), `"pending":false`, "", nil},
		{"current", reviewBehavior(pendingCurrent, 0, reviewNoBeads, 0), `"stale":false`, "", nil},
		{"stale", reviewBehavior(pendingStale, 0, reviewNoBeads, 0), `"stale":true`, "", nil},
		{"stale-with-open-escalation", reviewBehavior(pendingStale, 0, escalationList(perPR), 0), `"stale":true`, "", []string{"esc-1"}},
		{"lookup-failed", reviewBehavior(pendingError, 1, reviewNoBeads, 0), "", "detection_failed", nil},
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
			if f.ReviewEscalations == nil || f.ReviewEscalations.Error != "" {
				t.Fatalf("review_escalations = %+v", f.ReviewEscalations)
			}
			var ids []string
			for _, e := range f.ReviewEscalations.Open {
				ids = append(ids, e.ID)
			}
			if strings.Join(ids, ",") != strings.Join(tc.wantEsc, ",") {
				t.Errorf("escalation ids = %v, want %v", ids, tc.wantEsc)
			}
		})
	}
}

func TestPRAdapter_PendingReviewUsesOnlyTheConnectorVerbs(t *testing.T) {
	_, calls := gatherReview(t, reviewBehavior(pendingNone, 0, reviewNoBeads, 0))
	var sawPending, sawQuery bool
	for _, c := range calls {
		if c == "pr review pending "+fixturePRKey {
			sawPending = true
		}
		if strings.HasPrefix(c, "issue list --query "+ReviewEscalationQuery) {
			sawQuery = true
		}
		// Read-only: no submit, no issue write.
		if strings.HasPrefix(c, "pr review submit") || strings.HasPrefix(c, "issue create") ||
			strings.HasPrefix(c, "issue update") || strings.HasPrefix(c, "issue comment") || strings.HasPrefix(c, "issue close") {
			t.Errorf("display hydration made a state change: %q", c)
		}
	}
	if !sawPending || !sawQuery {
		t.Errorf("calls %v: want `pr review pending` and the escalation query", calls)
	}
}

func TestPRAdapter_PendingReviewNotFoundIsAFailureNotNone(t *testing.T) {
	f, _ := gatherReview(t, reviewBehavior(`{"protocolVersion":1,"error":{"code":"not_found","message":"gone"}}`, 4, reviewNoBeads, 0))
	if f.ReviewPending == nil || len(f.ReviewPending.Result) != 0 || !strings.Contains(f.ReviewPending.Error, "not_found") {
		t.Errorf("review_pending = %+v", f.ReviewPending)
	}
}

func TestPRAdapter_PendingReviewMalformedResultIsAFailure(t *testing.T) {
	f, _ := gatherReview(t, reviewBehavior(`{"protocolVersion":1,"result":{"head_sha":"x"}}`, 0, reviewNoBeads, 0))
	if f.ReviewPending == nil || len(f.ReviewPending.Result) != 0 || f.ReviewPending.Error == "" {
		t.Errorf("review_pending = %+v, want a failure for a result without a pending flag", f.ReviewPending)
	}
}

func TestPRAdapter_EscalationQueryFailureIsUnknownNotNone(t *testing.T) {
	// A degraded fan-out (exit 2) could be missing the covering bead: failure.
	for _, exit := range []int{1, 2, 3} {
		f, _, _ := gatherReviewRaw(t, reviewBehavior(pendingStale, 0, `{"error":{"code":"query_not_recognized","message":"no such query"}}`, exit))
		if f.ReviewEscalations == nil || f.ReviewEscalations.Error == "" || len(f.ReviewEscalations.Open) != 0 {
			t.Errorf("exit %d: review_escalations = %+v, want an error", exit, f.ReviewEscalations)
		}
	}
}

func TestPRAdapter_EscalationMatching(t *testing.T) {
	cases := []struct {
		name string
		list string
		want []string
	}{
		{"other PR", escalationList(escalationBead("e1", "pr:myorg/repo#7", "")), nil},
		{"case-insensitive repo", escalationList(escalationBead("e2", "pr:MyOrg/Repo#42", "")), []string{"e2"}},
		{"rollup naming the PR", escalationList(escalationBead("e3", "rollup:delete_refused", `"review_escalation_prs":"myorg/repo#7;myorg/repo#42"`)), []string{"e3"}},
		{"rollup not naming the PR", escalationList(escalationBead("e4", "rollup:delete_refused", `"review_escalation_prs":"myorg/repo#7"`)), nil},
		{"no key", escalationList(`{"id":"e5","metadata":{}}`), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := gatherReview(t, reviewBehavior(pendingStale, 0, tc.list, 0))
			var ids []string
			for _, e := range f.ReviewEscalations.Open {
				ids = append(ids, e.ID)
			}
			if strings.Join(ids, ",") != strings.Join(tc.want, ",") {
				t.Errorf("ids = %v, want %v", ids, tc.want)
			}
		})
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
	rec := entityFactory(t, reviewBehavior(pendingError, 1, reviewNoBeads, 0))
	f, err := NewGatherer(testConfig(""), nil).Gather(context.Background(), "pr", fixturePRKey, ChangeChanged)
	if err != nil {
		t.Fatal(err)
	}
	if f.ReviewPending != nil || f.ReviewEscalations != nil {
		t.Errorf("legacy Gather populated the review state: %+v", f)
	}
	for _, c := range readCalls(t, rec) {
		if strings.HasPrefix(c, "pr review") {
			t.Errorf("legacy Gather called %q", c)
		}
	}
}

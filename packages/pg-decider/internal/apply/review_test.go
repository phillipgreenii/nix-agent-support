package apply

// Tests of the review head refresh (ruling 16): the live re-read that precedes
// the update of an unclaimed review item, and the abandonment of the write when
// a claim landed between the listing and the update. Every helper here is
// prefixed "reviewRefresh" because sibling packets add test files to this
// package.

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
)

const reviewRefreshID = "rv-2"

func reviewRefreshAction() action.Action {
	return action.Action{
		Op: action.OpUpdate, Kind: "review-pr", Target: str(reviewRefreshID), Rule: "review.head-advanced",
		Fields: action.Fields{
			Title: "review-pr: acme/widgets#42 @ 9f3c1e2",
			Metadata: map[string]string{
				"head_sha": "9f3c1e2", "dedup_key": "pr:acme/widgets#42:review-pr:9f3c1e2",
			},
		},
		Facts: map[string]any{"transition": "refresh-head", "head_sha": "9f3c1e2"},
	}
}

func reviewRefreshLive(state, assignee string) string {
	return fmt.Sprintf(`{"result":{"id":%q,"state":%q,"assignee":%q,"served_from":"origin","stale":false,"as_of":"2026-10-10T00:00:00Z"}}`,
		reviewRefreshID, state, assignee)
}

func reviewRefreshShow(stdout string) respRule {
	return respRule{Match: "pg-connector issue show " + reviewRefreshID, Stdout: stdout}
}

// An item still open and unassigned is updated: head_sha, key and title.
func TestReviewRefreshWritesAnUnclaimedItemAfterALiveRead(t *testing.T) {
	d := newDouble(t, reviewRefreshShow(reviewRefreshLive("open", "")))
	r := focusRun(t, d, nil, nil, nil, reviewRefreshAction())
	if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeApplied}) || r.ExitCode != 0 {
		t.Fatalf("outcomes %v exit %d (events %+v)", outcomes(r), r.ExitCode, r.Events)
	}
	shows := focusLines(d, "pg-connector issue show")
	if len(shows) != 1 || !strings.Contains(shows[0], "--fresh") {
		t.Fatalf("the live read must be a fresh show, got %q", shows)
	}
	ups := focusLines(d, "pg-connector issue update")
	if len(ups) != 1 {
		t.Fatalf("updates = %q", ups)
	}
	for _, want := range []string{
		"issue update " + reviewRefreshID,
		"--metadata head_sha=9f3c1e2",
		"--metadata dedup_key=pr:acme/widgets#42:review-pr:9f3c1e2",
		"--title review-pr: acme/widgets#42 @ 9f3c1e2",
	} {
		if !strings.Contains(ups[0], want) {
			t.Errorf("update %q lacks %q", ups[0], want)
		}
	}
	if strings.Contains(ups[0], "--status") {
		t.Errorf("a head refresh does not touch the status: %q", ups[0])
	}
}

// A claim that landed between the listing and the update leaves the item
// unchanged: nothing is written, the outcome is skipped-stale and not a failure.
func TestReviewRefreshIsAbandonedWhenAClaimLanded(t *testing.T) {
	for name, live := range map[string]string{
		"in progress with an assignee": reviewRefreshLive("in_progress", "worker-1"),
		"assigned but still open":      reviewRefreshLive("open", "worker-1"),
		"in progress":                  reviewRefreshLive("in_progress", ""),
		"deferred":                     reviewRefreshLive("deferred", ""),
		"blocked":                      reviewRefreshLive("blocked", ""),
		"closed":                       reviewRefreshLive("closed", ""),
	} {
		t.Run(name, func(t *testing.T) {
			d := newDouble(t, reviewRefreshShow(live))
			r := focusRun(t, d, nil, nil, nil, reviewRefreshAction())
			if got := focusUpdates(d); len(got) != 0 {
				t.Fatalf("an abandoned write must write no update and no comment, got %q", got)
			}
			if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeSkippedStale}) {
				t.Fatalf("outcomes %v, want skipped-stale", outcomes(r))
			}
			if r.ExitCode != 0 {
				t.Errorf("a skipped-stale outcome is not a failure, exit %d", r.ExitCode)
			}
		})
	}
}

// A live read that cannot be trusted fails closed: nothing is written.
func TestReviewRefreshWithAnUntrustedLiveReadWritesNothing(t *testing.T) {
	for name, show := range map[string]respRule{
		"error":       {Match: "pg-connector issue show " + reviewRefreshID, Stdout: `{"error":{"code":"unavailable","message":"down"}}`, Exit: 1},
		"not origin":  reviewRefreshShow(strings.Replace(reviewRefreshLive("open", ""), `"origin"`, `"cache"`, 1)),
		"stale":       reviewRefreshShow(strings.Replace(reviewRefreshLive("open", ""), `"stale":false`, `"stale":true`, 1)),
		"undecodable": reviewRefreshShow(`not json`),
	} {
		t.Run(name, func(t *testing.T) {
			d := newDouble(t, show)
			r := focusRun(t, d, nil, nil, nil, reviewRefreshAction())
			if got := focusUpdates(d); len(got) != 0 {
				t.Fatalf("wrote %q", got)
			}
			if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeSkippedStale}) {
				t.Fatalf("outcomes %v", outcomes(r))
			}
		})
	}
}

// A stale refresh breaks the force-review consumption that depends on it: the
// flag stays set, so the next run (which then sees the claim) decides again.
func TestReviewRefreshAbandonedDoesNotConsumeTheForceFlag(t *testing.T) {
	d := newDouble(t, reviewRefreshShow(reviewRefreshLive("in_progress", "worker-1")))
	key := "decider.pr-decider.force_review_consumed"
	val := "9f3c1e2"
	consume := action.Action{Op: action.OpAnnotate, Target: &key, Fields: action.Fields{Value: &val}, Rule: "review.head-advanced", RequiresPrior: true}
	r := focusRun(t, d, nil, nil, nil, reviewRefreshAction(), consume)
	if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeSkippedStale, OutcomeSkippedDependency}) {
		t.Fatalf("outcomes %v", outcomes(r))
	}
	for _, l := range focusLines(d, "pg-desk") {
		if strings.Contains(l, "annotate") {
			t.Errorf("the flag was consumed: %q", l)
		}
	}
}

// Only the refresh is re-read: another update of a review item (or a refresh of
// another kind) is written as before, without a live read.
func TestReviewRefreshReadsLiveOnlyForTheRefreshTransition(t *testing.T) {
	plain := reviewRefreshAction()
	plain.Facts = map[string]any{"head_sha": "9f3c1e2"}
	d := newDouble(t)
	r := focusRun(t, d, nil, nil, nil, plain)
	if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeApplied}) {
		t.Fatalf("outcomes %v", outcomes(r))
	}
	if got := focusLines(d, "pg-connector issue show"); len(got) != 0 {
		t.Errorf("an update without the refresh transition made a live read: %q", got)
	}
}

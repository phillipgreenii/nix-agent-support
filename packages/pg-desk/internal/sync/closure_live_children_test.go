package sync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Bead pg2-ubvmh: closing the anchor of a closed/merged PR MUST close every
// open child, including ones the ledger and Facts.WorkBeads never saw. The
// `--change removed` gather (removedFacts) returns no work-beads at all, and
// the configured work-beads query lists only merge-request beads plus
// process-feedback:/review-pr: tasks, so a legacy child created by the old
// pg-pr daemon was invisible to the cascade and bd >= 1.3.1 refused to close
// the anchor ("cannot close X: 1 open child issue(s); close children first").
// handleClosure therefore reads each closed bead's live children itself
// (`pg-connector issue children <id>`).

// removedMergedFacts is exactly what gather.Gather returns for a merged PR on
// a --change removed re-read: pr show state and RemovedState, NO WorkBeads.
func removedMergedFacts() gather.Facts {
	return gather.Facts{PRShow: prFixture(map[string]any{"state": "closed", "merged": true}), HeadSHA: fixtureHeadSHA, RemovedState: "merged"}
}

func callsOfVerb(t *testing.T, recordFile, verb string) []callRecord {
	t.Helper()
	var out []callRecord
	for _, r := range readCallRecords(t, recordFile) {
		if r.verb() == verb {
			out = append(out, r)
		}
	}
	return out
}

// TestSync_ConfirmedClosure_ClosesUntrackedChildFromRemovedFacts is the bug's
// reproduction: a ChangeRemoved closure whose facts carry NO work-beads, with
// an open legacy child of the anchor that is in neither the ledger nor the
// facts. The child must close, before the anchor, and the run must succeed.
func TestSync_ConfirmedClosure_ClosesUntrackedChildFromRemovedFacts(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)
	anchorID, cycleID, reviewID := seedExistingAnchorCycleAndReview(t, s)
	t.Setenv("GO_HELPER_PARENT_OF", strings.Join([]string{
		cycleID + ":" + anchorID, reviewID + ":" + anchorID, "bd-legacy:" + anchorID,
	}, ","))

	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, removedMergedFacts(), interpFor("mine", nil)); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	order := closedTransitions(t, recordFile)
	if len(order) != 4 || order[len(order)-1] != anchorID {
		t.Fatalf("close order = %v, want cycle, review and the untracked child closed, then the anchor %s last", order, anchorID)
	}
	seen := map[string]int{}
	for _, id := range order {
		seen[id]++
	}
	if seen["bd-legacy"] != 1 {
		t.Fatalf("untracked legacy child closed %d times, want 1: %v", seen["bd-legacy"], order)
	}
	if anchor, _, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor); anchor.LastSyncedContentHash != closedSentinel {
		t.Fatalf("anchor ledger row not marked closed: %+v", anchor)
	}

	// The live read is pinned like every other issue call.
	children := callsOfVerb(t, recordFile, "issue children")
	if len(children) == 0 {
		t.Fatal("handleClosure never read the anchor's live children")
	}
	for _, c := range children {
		if !strings.Contains(strings.Join(c.Args, " "), "--backend beads") || c.PGConnectorBeadsDir != "/configured/beads" {
			t.Errorf("issue children call %v (beads dir %q) is not pinned to the agent tracker backend and beads dir", c.Args, c.PGConnectorBeadsDir)
		}
	}
}

// TestSync_ConfirmedClosure_ClosesUntrackedDescendantsDepthFirst: untracked
// children have untracked children of their own (a legacy child with its own
// open child); every level closes before its parent. Also covers an untracked
// child of a ledger-tracked cycle bead.
func TestSync_ConfirmedClosure_ClosesUntrackedDescendantsDepthFirst(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)
	anchorID, cycleID, reviewID := seedExistingAnchorCycleAndReview(t, s)
	t.Setenv("GO_HELPER_PARENT_OF", strings.Join([]string{
		cycleID + ":" + anchorID, reviewID + ":" + anchorID, "bd-legacy:" + anchorID,
		"bd-legacy-kid:bd-legacy", "bd-cycle-legacy-kid:" + cycleID,
	}, ","))

	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, removedMergedFacts(), interpFor("mine", nil)); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	order := closedTransitions(t, recordFile)
	at := map[string]int{}
	for i, id := range order {
		if _, dup := at[id]; dup {
			t.Fatalf("%s closed more than once: %v", id, order)
		}
		at[id] = i
	}
	if len(order) != 6 || order[len(order)-1] != anchorID {
		t.Fatalf("close order = %v, want 6 closes with the anchor %s last", order, anchorID)
	}
	for _, edge := range [][2]string{{"bd-legacy-kid", "bd-legacy"}, {"bd-cycle-legacy-kid", cycleID}} {
		if at[edge[0]] > at[edge[1]] {
			t.Errorf("%s must close before its parent %s: %v", edge[0], edge[1], order)
		}
	}
}

// TestSync_ConfirmedClosure_LiveChildrenMergeWithFactsWithoutDoubleClose: a
// child present both in Facts.WorkBeads and in the live read closes once.
func TestSync_ConfirmedClosure_LiveChildrenMergeWithFactsWithoutDoubleClose(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)
	anchorID, cycleID, reviewID := seedExistingAnchorCycleAndReview(t, s)
	t.Setenv("GO_HELPER_PARENT_OF", strings.Join([]string{
		cycleID + ":" + anchorID, reviewID + ":" + anchorID, "bd-human-1:" + anchorID, "bd-legacy:" + anchorID,
	}, ","))
	facts := removedMergedFacts()
	facts.WorkBeads = workBeadsFixture(
		map[string]any{"id": "bd-human-1", "title": "Human: unblock", "state": "open", "parent": anchorID},
	)

	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, facts, interpFor("mine", nil)); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	closed := map[string]int{}
	for _, id := range closedTransitions(t, recordFile) {
		closed[id]++
	}
	for _, id := range []string{anchorID, cycleID, reviewID, "bd-human-1", "bd-legacy"} {
		if closed[id] != 1 {
			t.Errorf("%s closed %d times, want 1: %v", id, closed[id], closed)
		}
	}
}

// TestSync_ConfirmedClosure_LiveReadNeverClosesForeignOrClosedBeads: only an
// open direct child of the node being closed is closed. A live-read entry that
// names a different parent, or is already closed, is left alone.
func TestSync_ConfirmedClosure_LiveReadNeverClosesForeignOrClosedBeads(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)
	anchorID, _, _ := seedExistingAnchorCycleAndReview(t, s)
	t.Setenv("GO_HELPER_CHILDREN_RAW", `{"children":[`+
		`{"id":"bd-legacy","title":"legacy","state":"open","parent":"`+anchorID+`"},`+
		`{"id":"bd-no-parent-field","title":"unset parent","state":"open"},`+
		`{"id":"bd-foreign","title":"other anchor's","state":"open","parent":"bd-some-other-anchor"},`+
		`{"id":"bd-closed","title":"done","state":"closed","parent":"`+anchorID+`"},`+
		`{"id":"`+anchorID+`","title":"self","state":"open","parent":"`+anchorID+`"}]}`)

	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, removedMergedFacts(), interpFor("mine", nil)); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	closed := map[string]int{}
	for _, id := range closedTransitions(t, recordFile) {
		closed[id]++
	}
	for _, id := range []string{"bd-legacy", "bd-no-parent-field"} {
		if closed[id] != 1 {
			t.Errorf("%s closed %d times, want 1: %v", id, closed[id], closed)
		}
	}
	for _, id := range []string{"bd-foreign", "bd-closed"} {
		if closed[id] != 0 {
			t.Errorf("%s must not be closed: %v", id, closed)
		}
	}
	if closed[anchorID] != 1 {
		t.Errorf("anchor closed %d times, want exactly 1 (a self-listing must not close it early): %v", closed[anchorID], closed)
	}
}

// TestSync_ConfirmedClosure_LiveReadFailureFailsClosed: an indeterminate live
// read (here "unavailable") fails the run and touches nothing, rather than
// closing the anchor on a partial view of its children.
func TestSync_ConfirmedClosure_LiveReadFailureFailsClosed(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)
	anchorID, _, _ := seedExistingAnchorCycleAndReview(t, s)
	t.Setenv("GO_HELPER_CHILDREN_FAIL_CODE", "unavailable")

	err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, removedMergedFacts(), interpFor("mine", nil))
	if err == nil || !strings.Contains(err.Error(), "children") {
		t.Fatalf("Sync error = %v, want the failed children read", err)
	}
	var ce *ConnectorError
	if !errors.As(err, &ce) || ce.Code != "unavailable" {
		t.Fatalf("error chain should carry the connector error, got %#v", err)
	}
	for _, id := range closedTransitions(t, recordFile) {
		t.Errorf("%s closed despite an indeterminate children read", id)
	}
	for _, r := range readCallRecords(t, recordFile) {
		if len(r.Args) > 2 && r.Args[2] == anchorID {
			t.Fatalf("anchor written despite an indeterminate children read: %v", r.Args)
		}
	}
	if anchor, _, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor); anchor.LastSyncedContentHash == closedSentinel {
		t.Fatal("anchor ledger row marked closed")
	}
}

// TestSync_ConfirmedClosure_BackendWithoutChildrenOpFallsBackToFacts: a
// backend that does not implement the optional children op (unknown_op) or
// does not know the bead (not_found) still closes via Facts.WorkBeads, the
// behavior before the live read existed.
func TestSync_ConfirmedClosure_BackendWithoutChildrenOpFallsBackToFacts(t *testing.T) {
	for _, code := range []string{"unknown_op", "not_found"} {
		t.Run(code, func(t *testing.T) {
			s := newTestSyncer(t, ModeApply)
			recordFile := withFactory(t)
			anchorID, cycleID, reviewID := seedExistingAnchorCycleAndReview(t, s)
			t.Setenv("GO_HELPER_CHILDREN_FAIL_CODE", code)
			facts := removedMergedFacts()
			facts.WorkBeads = workBeadsFixture(
				map[string]any{"id": "bd-human-1", "title": "Human: unblock", "state": "open", "parent": anchorID},
			)

			if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, facts, interpFor("mine", nil)); err != nil {
				t.Fatalf("Sync: %v", err)
			}
			order := closedTransitions(t, recordFile)
			if len(order) != 4 || order[len(order)-1] != anchorID {
				t.Fatalf("close order = %v, want cycle %s, review %s, bd-human-1 then the anchor last", order, cycleID, reviewID)
			}
		})
	}
}

// TestSync_PlanMode_ClosureDoesNotReadLiveChildren: plan mode makes no
// connector call at all (the live read is only needed to close beads).
func TestSync_PlanMode_ClosureDoesNotReadLiveChildren(t *testing.T) {
	s := newTestSyncer(t, ModePlan)
	recordFile := withFactory(t)
	seedExistingAnchorCycleAndReview(t, s)

	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, removedMergedFacts(), interpFor("mine", nil)); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if calls := readCallRecords(t, recordFile); len(calls) != 0 {
		t.Fatalf("plan mode made connector calls: %+v", calls)
	}
}

// TestSync_ConfirmedClosure_OpenChildRefusalIsNonTransientAndNotRetried is the
// defect-2 end to end: when bd still refuses the anchor close because of an
// open child the run could not close (here the live read reports no children
// while bd's refusal fires, as when a child appears between the read and the
// close), the failure Sync returns classifies as non-transient, so the retry
// state records "non-transient" at the FIRST failure with no retry scheduled
// instead of burning the whole backoff budget.
func TestSync_ConfirmedClosure_OpenChildRefusalIsNonTransientAndNotRetried(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	withFactory(t)
	anchorID, cycleID, reviewID := seedExistingAnchorCycleAndReview(t, s)
	t.Setenv("GO_HELPER_PARENT_OF", strings.Join([]string{cycleID + ":" + anchorID, reviewID + ":" + anchorID, "bd-late:" + anchorID}, ","))
	// The live read is blind to bd-late; bd's refusal is not.
	t.Setenv("GO_HELPER_CHILDREN_RAW", `{"children":[]}`)

	err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, removedMergedFacts(), interpFor("mine", nil))
	if err == nil || !strings.Contains(err.Error(), "open child issue") {
		t.Fatalf("Sync error = %v, want bd's open-child refusal", err)
	}
	var ce *ConnectorError
	if !errors.As(err, &ce) || ce.Code != "unavailable" {
		t.Fatalf("the refusal arrives wrapped as connector code unavailable, got %#v", err)
	}
	if got := Classify(err); got != ClassNonTransient {
		t.Fatalf("Classify = %q, want non-transient", got)
	}
	policy := RetryPolicy{MaxRetries: 10, InitialBackoff: time.Minute, MaxBackoff: 30 * time.Minute}
	next := policy.NextState(store.SyncRetry{}, err, time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC))
	if next.State != store.SyncRetryNonTransient || next.NextRetryAt != "" || next.Attempts != 1 {
		t.Fatalf("retry state = %+v, want non-transient after the first attempt with no retry scheduled", next)
	}
	if RetryDue(next, true, time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("a non-transient row must never be due for an automatic retry")
	}
}

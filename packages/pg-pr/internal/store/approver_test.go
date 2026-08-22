package store

import (
	"context"
	"testing"
)

// Two distinct approvers on the same head are two distinct, distinguishable
// rows — the defect pr_revision.others_approved (a single OR'd boolean)
// could not represent.
func TestSetApproval_TwoApproversAreDistinctRows(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)

	if err := db.SetApproval(ctx, prID, "me", "h1", "approved", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("SetApproval(me): %v", err)
	}
	if err := db.SetApproval(ctx, prID, "teammate", "h1", "approved", "2026-01-01T01:00:00Z"); err != nil {
		t.Fatalf("SetApproval(teammate): %v", err)
	}

	approvals, err := db.ListApprovals(ctx, prID)
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(approvals) != 2 {
		t.Fatalf("want 2 distinct approver rows, got %d: %+v", len(approvals), approvals)
	}
	byApprover := map[string]Approval{}
	for _, a := range approvals {
		byApprover[a.Approver] = a
	}
	me, ok := byApprover["me"]
	if !ok || me.HeadSHA != "h1" || me.State != "approved" {
		t.Errorf("me row wrong: ok=%v %+v", ok, me)
	}
	teammate, ok := byApprover["teammate"]
	if !ok || teammate.HeadSHA != "h1" || teammate.State != "approved" {
		t.Errorf("teammate row wrong: ok=%v %+v", ok, teammate)
	}
	if me.ID == teammate.ID {
		t.Errorf("two distinct approvers must not share a row id")
	}
}

// The same approver re-approving a LATER head UPDATES the existing row
// (UNIQUE(pr_id, approver)) rather than appending a duplicate.
func TestSetApproval_SameApproverLaterHeadUpdatesInPlace(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)

	if err := db.SetApproval(ctx, prID, "teammate", "h1", "approved", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("first SetApproval: %v", err)
	}
	first, err := db.GetApproval(ctx, prID, "teammate")
	if err != nil || first == nil {
		t.Fatalf("GetApproval after first observation: err=%v got=%+v", err, first)
	}

	if err := db.SetApproval(ctx, prID, "teammate", "h2", "approved", "2026-01-02T00:00:00Z"); err != nil {
		t.Fatalf("second SetApproval: %v", err)
	}

	approvals, err := db.ListApprovals(ctx, prID)
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("re-approval at a later head must UPDATE, not duplicate; got %d rows: %+v", len(approvals), approvals)
	}
	if approvals[0].ID != first.ID {
		t.Errorf("re-approval should reuse the same row id; got %d, want %d", approvals[0].ID, first.ID)
	}
	if approvals[0].HeadSHA != "h2" {
		t.Errorf("HeadSHA = %q, want h2 (the latest observation)", approvals[0].HeadSHA)
	}
	if approvals[0].ObservedAt != "2026-01-02T00:00:00Z" {
		t.Errorf("ObservedAt = %q, want the latest timestamp", approvals[0].ObservedAt)
	}
}

// Per-approver staleness relative to the CURRENT head is computable: "A
// approved head N, B has not re-approved head N+1" is representable and
// IsStale reports it correctly for each approver independently.
func TestApproval_PerApproverStaleness(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)

	// Both approve head N.
	if err := db.SetApproval(ctx, prID, "alice", "hN", "approved", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("SetApproval alice: %v", err)
	}
	if err := db.SetApproval(ctx, prID, "bob", "hN", "approved", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("SetApproval bob: %v", err)
	}

	// The PR advances to head N+1; only bob re-approves it.
	const currentHead = "hN+1"
	if err := db.SetApproval(ctx, prID, "bob", currentHead, "approved", "2026-01-02T00:00:00Z"); err != nil {
		t.Fatalf("SetApproval bob re-approve: %v", err)
	}

	alice, err := db.GetApproval(ctx, prID, "alice")
	if err != nil || alice == nil {
		t.Fatalf("GetApproval alice: err=%v got=%+v", err, alice)
	}
	bob, err := db.GetApproval(ctx, prID, "bob")
	if err != nil || bob == nil {
		t.Fatalf("GetApproval bob: err=%v got=%+v", err, bob)
	}

	if !alice.IsStale(currentHead) {
		t.Errorf("alice approved %q, current head is %q; must be reported stale", alice.HeadSHA, currentHead)
	}
	if bob.IsStale(currentHead) {
		t.Errorf("bob re-approved the current head %q; must NOT be reported stale", currentHead)
	}

	// A fully caught-up approver at an EMPTY currentHeadSHA is never reported
	// stale (nothing to compare against).
	if alice.IsStale("") {
		t.Errorf("an empty currentHeadSHA must never be reported stale")
	}
}

// GetApproval on an approver never recorded for the PR returns nil, not an
// error.
func TestGetApproval_NoRowReturnsNil(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)

	a, err := db.GetApproval(ctx, prID, "nobody")
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if a != nil {
		t.Errorf("want nil for an approver never recorded, got %+v", a)
	}
}

// ListApprovals on a PR with no recorded approvals returns an empty slice, not
// an error.
func TestListApprovals_NoneRecorded(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)

	approvals, err := db.ListApprovals(ctx, prID)
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(approvals) != 0 {
		t.Errorf("want 0 approvals, got %d: %+v", len(approvals), approvals)
	}
}

// A teammate's non-approved states (changes-requested, commented) are
// representable too — pr_approval is not limited to "approved" the way
// others_approved was.
func TestSetApproval_NonApprovedStates(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)

	if err := db.SetApproval(ctx, prID, "teammate", "h1", "changes-requested", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("SetApproval changes-requested: %v", err)
	}
	got, err := db.GetApproval(ctx, prID, "teammate")
	if err != nil || got == nil {
		t.Fatalf("GetApproval: err=%v got=%+v", err, got)
	}
	if got.State != "changes-requested" {
		t.Errorf("State = %q, want changes-requested", got.State)
	}
}

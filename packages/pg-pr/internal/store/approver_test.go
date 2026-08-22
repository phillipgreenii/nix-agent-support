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

// The three readings a per-approver record must keep distinguishable —
// FRESH, STALE and ABSENT — are all distinguishable for the same approver
// across scenarios. A two-way boolean (the old others_approved) collapses the
// first two, which is the defect pg2-4dz88.1.7 fixes: an approver who DID
// approve but whose approval no longer stands must never read as one who never
// approved (INV-APPROVAL-3).
//
// Both routes to STALE are covered, because they are independent: the host
// DISMISSED the review (head-independent — asserted here AT the current head,
// where a head comparison alone reports nothing), and the approver simply has
// not reviewed the current head.
func TestApproval_FreshStaleAbsentAreDistinguishable(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)

	const currentHead = "h2"

	tests := []struct {
		name      string
		approver  string
		seed      func(t *testing.T)
		wantRow   bool
		wantStale bool
	}{
		{
			name:     "fresh: approved the current head",
			approver: "fresh-approver",
			seed: func(t *testing.T) {
				t.Helper()
				if err := db.SetApproval(ctx, prID, "fresh-approver", currentHead, "approved", "2026-01-02T00:00:00Z"); err != nil {
					t.Fatalf("SetApproval: %v", err)
				}
			},
			wantRow:   true,
			wantStale: false,
		},
		{
			name:     "stale by dismissal: dismissed AT the current head",
			approver: "dismissed-approver",
			seed: func(t *testing.T) {
				t.Helper()
				if err := db.SetDismissedApproval(ctx, prID, "dismissed-approver", currentHead, "2026-01-02T01:00:00Z"); err != nil {
					t.Fatalf("SetDismissedApproval: %v", err)
				}
			},
			wantRow:   true,
			wantStale: true,
		},
		{
			name:     "stale by head: approved an earlier head only",
			approver: "behind-approver",
			seed: func(t *testing.T) {
				t.Helper()
				if err := db.SetApproval(ctx, prID, "behind-approver", "h1", "approved", "2026-01-01T00:00:00Z"); err != nil {
					t.Fatalf("SetApproval: %v", err)
				}
			},
			wantRow:   true,
			wantStale: true,
		},
		{
			name:     "absent: never recorded on this PR",
			approver: "never-reviewed",
			seed:     nil, // nothing observed for this approver
			wantRow:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.seed != nil {
				tc.seed(t)
			}
			got, err := db.GetApproval(ctx, prID, tc.approver)
			if err != nil {
				t.Fatalf("GetApproval: %v", err)
			}
			if !tc.wantRow {
				if got != nil {
					t.Fatalf("absent must stay absent (no fabricated row), got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("want a recorded row for %q, got none — an approval that DID happen must never read as absent", tc.approver)
			}
			// Every row here records an approval, stale or not: state must not
			// be repurposed to encode the staleness.
			if got.State != "approved" {
				t.Errorf("State = %q, want approved (staleness is not encoded in state)", got.State)
			}
			if got.IsStale(currentHead) != tc.wantStale {
				t.Errorf("IsStale(%q) = %v, want %v (row=%+v)", currentHead, got.IsStale(currentHead), tc.wantStale, *got)
			}
		})
	}

	// All three recorded approvers are still individually addressable — the
	// stale ones were not folded into, or dropped in favour of, the fresh one.
	approvals, err := db.ListApprovals(ctx, prID)
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(approvals) != 3 {
		t.Fatalf("want 3 rows (fresh + 2 stale), got %d: %+v", len(approvals), approvals)
	}
}

// A dismissed approval is head-INDEPENDENTLY stale: the host can dismiss a
// review without the head moving, so an empty currentHeadSHA — which never
// reports a non-dismissed approval stale — must still report a dismissed one
// stale.
func TestApproval_DismissedIsStaleEvenWithoutAHeadToCompare(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)

	if err := db.SetDismissedApproval(ctx, prID, "teammate", "h1", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("SetDismissedApproval: %v", err)
	}
	got, err := db.GetApproval(ctx, prID, "teammate")
	if err != nil || got == nil {
		t.Fatalf("GetApproval: err=%v got=%+v", err, got)
	}
	if !got.Dismissed {
		t.Errorf("Dismissed = false, want true (the dismissal fact must round-trip)")
	}
	if !got.IsStale("") {
		t.Errorf("a dismissed approval must report stale even with no head to compare against")
	}
	if !got.IsStale("h1") {
		t.Errorf("a dismissed approval must report stale even AT the head it was observed on")
	}
}

// Dismissal is not permanent: the same approver re-approving replaces the row
// and CLEARS the dismissal, so a dismiss-then-reapprove sequence ends FRESH.
// The reverse sequence ends STALE. Both are the upsert's last-observation-wins
// semantics, asserted in both orders because getting one right by accident is
// easy.
func TestSetApproval_DismissalRoundTripsBothOrders(t *testing.T) {
	ctx := context.Background()

	const currentHead = "h2"

	tests := []struct {
		name          string
		apply         func(t *testing.T, db *DB, prID int64)
		wantDismissed bool
		wantHeadSHA   string
		wantStale     bool
	}{
		{
			name: "dismiss then reapprove at a newer head → fresh",
			apply: func(t *testing.T, db *DB, prID int64) {
				t.Helper()
				if err := db.SetDismissedApproval(ctx, prID, "alice", "h1", "2026-01-01T00:00:00Z"); err != nil {
					t.Fatalf("SetDismissedApproval: %v", err)
				}
				if err := db.SetApproval(ctx, prID, "alice", currentHead, "approved", "2026-01-02T00:00:00Z"); err != nil {
					t.Fatalf("SetApproval: %v", err)
				}
			},
			wantDismissed: false,
			wantHeadSHA:   currentHead,
			wantStale:     false,
		},
		{
			name: "reapprove then dismiss → stale",
			apply: func(t *testing.T, db *DB, prID int64) {
				t.Helper()
				if err := db.SetApproval(ctx, prID, "alice", currentHead, "approved", "2026-01-01T00:00:00Z"); err != nil {
					t.Fatalf("SetApproval: %v", err)
				}
				if err := db.SetDismissedApproval(ctx, prID, "alice", currentHead, "2026-01-02T00:00:00Z"); err != nil {
					t.Fatalf("SetDismissedApproval: %v", err)
				}
			},
			wantDismissed: true,
			wantHeadSHA:   currentHead,
			wantStale:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := OpenForTest(t)
			prID := seedPR(t, db)
			tc.apply(t, db, prID)

			approvals, err := db.ListApprovals(ctx, prID)
			if err != nil {
				t.Fatalf("ListApprovals: %v", err)
			}
			if len(approvals) != 1 {
				t.Fatalf("the same approver must occupy ONE row; got %d: %+v", len(approvals), approvals)
			}
			got := approvals[0]
			if got.Dismissed != tc.wantDismissed {
				t.Errorf("Dismissed = %v, want %v (row=%+v)", got.Dismissed, tc.wantDismissed, got)
			}
			if got.HeadSHA != tc.wantHeadSHA {
				t.Errorf("HeadSHA = %q, want %q", got.HeadSHA, tc.wantHeadSHA)
			}
			if got.State != "approved" {
				t.Errorf("State = %q, want approved", got.State)
			}
			if got.IsStale(currentHead) != tc.wantStale {
				t.Errorf("IsStale(%q) = %v, want %v (row=%+v)", currentHead, got.IsStale(currentHead), tc.wantStale, got)
			}
		})
	}
}

// Staleness compares head SHAs for DIFFERENCE, not for order. A head SHA
// carries no ordering meaning — a force-push can move the head to a SHA that
// sorts either side of the old one — so an approval recorded at a head that
// sorts AFTER the current head is exactly as stale as one that sorts before it.
func TestApproval_StalenessComparesDifferenceNotOrder(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)

	if err := db.SetApproval(ctx, prID, "alice", "zzz-reviewed-head", "approved", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("SetApproval: %v", err)
	}
	got, err := db.GetApproval(ctx, prID, "alice")
	if err != nil || got == nil {
		t.Fatalf("GetApproval: err=%v got=%+v", err, got)
	}
	if !got.IsStale("aaa-current-head") {
		t.Errorf("an approval whose head sorts AFTER the current head must still read stale (row=%+v)", *got)
	}
}

// Every pr_approval accessor REPORTS a store failure rather than returning a
// silent success: with the store closed underneath them, each entry point must
// return an error.
func TestApprovalAccessors_ReportStoreErrors(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := db.SetApproval(ctx, prID, "alice", "h1", "approved", "t1"); err == nil {
		t.Error("SetApproval on a closed store returned nil, want an error")
	}
	if err := db.SetDismissedApproval(ctx, prID, "alice", "h1", "t1"); err == nil {
		t.Error("SetDismissedApproval on a closed store returned nil, want an error")
	}
	if _, err := db.GetApproval(ctx, prID, "alice"); err == nil {
		t.Error("GetApproval on a closed store returned nil, want an error")
	}
	if _, err := db.ListApprovals(ctx, prID); err == nil {
		t.Error("ListApprovals on a closed store returned nil, want an error")
	}
}

// SQLite's dynamic typing lets a non-integer value sit in the dismissed
// column, which cannot be scanned into the Dismissed bool. ListApprovals MUST
// surface that as an error instead of quietly returning a short list, which a
// caller would read as "this approver never approved".
func TestListApprovals_UnscannableRowSurfacesAsError(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)

	if _, err := db.sql.Exec(`INSERT INTO pr_approval (pr_id, approver, state, head_sha, observed_at, dismissed)
		VALUES (?,'alice','approved','h1','t1','not-an-integer')`, prID); err != nil {
		t.Fatalf("seed unscannable row: %v", err)
	}

	got, err := db.ListApprovals(ctx, prID)
	if err == nil {
		t.Errorf("ListApprovals returned nil error for an unscannable row; got %+v", got)
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

// A teammate's CHANGES_REQUESTED record round-trips through SetApproval and
// is queryable per approver, and is distinguishable both from that SAME
// approver's own EARLIER APPROVED state (the state changed, not just the
// head) and from a STALE reading (IsStale is false against the head it was
// most recently observed at). pg2-4dz88.1.8: sync now writes
// changes-requested here too, alongside the existing approved writes.
func TestApproval_ChangesRequestedDistinctFromApprovedAndStale(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)

	// The teammate approves head h1...
	if err := db.SetApproval(ctx, prID, "teammate", "h1", "approved", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("SetApproval approved: %v", err)
	}
	approvedFirst, err := db.GetApproval(ctx, prID, "teammate")
	if err != nil || approvedFirst == nil || approvedFirst.State != "approved" {
		t.Fatalf("GetApproval after approved observation: err=%v got=%+v", err, approvedFirst)
	}

	// ...then requests changes on a later head h2 (UNIQUE(pr_id, approver)
	// means this UPDATES the same row rather than appending a new one).
	if err := db.SetApproval(ctx, prID, "teammate", "h2", "changes-requested", "2026-01-02T00:00:00Z"); err != nil {
		t.Fatalf("SetApproval changes-requested: %v", err)
	}

	got, err := db.GetApproval(ctx, prID, "teammate")
	if err != nil || got == nil {
		t.Fatalf("GetApproval: err=%v got=%+v", err, got)
	}
	if got.ID != approvedFirst.ID {
		t.Errorf("re-observation should reuse the same row id; got %d, want %d", got.ID, approvedFirst.ID)
	}
	if got.State != "changes-requested" {
		t.Errorf("State = %q, want \"changes-requested\" (must not still read as the earlier approved state)", got.State)
	}
	if got.HeadSHA != "h2" {
		t.Errorf("HeadSHA = %q, want h2 (the later observation replaces, not appends)", got.HeadSHA)
	}

	// Distinguishable from STALE: at the head it was most recently observed
	// at, it must not be reported stale.
	if got.IsStale("h2") {
		t.Errorf("changes-requested at the current head must not be reported stale")
	}
	// But relative to a HEAD BEYOND h2, it IS stale — same staleness
	// semantics as an approved row, just carrying a different state.
	if !got.IsStale("h3") {
		t.Errorf("changes-requested observed at h2 must be reported stale once the PR advances to h3")
	}
}

package store

import (
	"context"
	"testing"
)

// TestReconcileStaleness verifies that ci-failure rows whose subject_sha does
// not match the PR head are marked superseded, and on-head rows are untouched.
func TestReconcileStaleness(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	prID, _ := db.UpsertPR(ctx, PullRequest{
		Repo: "o/r", Number: 1, Ownership: "mine", State: "open", HeadSHA: "sha-head",
	})

	// Row on current head — should stay as-is.
	onHeadID, err := db.UpsertFeedback(ctx, Feedback{
		PRID:        prID,
		Kind:        "ci-failure",
		Fingerprint: "fp-on",
		SubjectSHA:  "sha-head",
	})
	if err != nil {
		t.Fatalf("UpsertFeedback on-head: %v", err)
	}

	// Row on an older SHA — should become superseded.
	offHeadID, err := db.UpsertFeedback(ctx, Feedback{
		PRID:        prID,
		Kind:        "ci-failure",
		Fingerprint: "fp-off",
		SubjectSHA:  "sha-old",
	})
	if err != nil {
		t.Fatalf("UpsertFeedback off-head: %v", err)
	}

	if err := db.ReconcileStaleness(ctx, prID, "sha-head"); err != nil {
		t.Fatalf("ReconcileStaleness: %v", err)
	}

	onHead, _ := db.GetFeedback(ctx, onHeadID)
	if onHead.Status == "superseded" {
		t.Errorf("on-head row was unexpectedly superseded")
	}

	offHead, _ := db.GetFeedback(ctx, offHeadID)
	if offHead.Status != "superseded" {
		t.Errorf("off-head row status = %q, want superseded", offHead.Status)
	}
}

// TestListFeedbackFilters verifies that ListFilter{Kind} and
// ListFilter{ActiveOnly} narrow results correctly.
func TestListFeedbackFilters(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	prID, _ := db.UpsertPR(ctx, PullRequest{
		Repo: "o/r", Number: 1, Ownership: "mine", State: "open",
	})

	// Insert two different kinds.
	_, _ = db.UpsertFeedback(ctx, Feedback{PRID: prID, Kind: "pr-comments", Fingerprint: "fp-a"})
	_, _ = db.UpsertFeedback(ctx, Feedback{PRID: prID, Kind: "ci-failure", Fingerprint: "fp-b", SubjectSHA: "sha1"})

	// Insert one that is superseded.
	supID, _ := db.UpsertFeedback(ctx, Feedback{PRID: prID, Kind: "ci-failure", Fingerprint: "fp-c", SubjectSHA: "sha-old"})
	if err := db.ReconcileStaleness(ctx, prID, "sha1"); err != nil {
		t.Fatalf("ReconcileStaleness: %v", err)
	}
	// Confirm it's superseded.
	got, _ := db.GetFeedback(ctx, supID)
	if got.Status != "superseded" {
		t.Fatalf("precondition: fp-c status = %q, want superseded", got.Status)
	}

	// Filter by kind.
	byKind, err := db.ListFeedback(ctx, prID, ListFilter{Kind: "pr-comments"})
	if err != nil {
		t.Fatalf("ListFeedback by kind: %v", err)
	}
	if len(byKind) != 1 || byKind[0].Kind != "pr-comments" {
		t.Errorf("ListFeedback(Kind=pr-comments) = %v, want 1 pr-comments row", byKind)
	}

	// ActiveOnly should exclude the superseded row.
	active, err := db.ListFeedback(ctx, prID, ListFilter{ActiveOnly: true})
	if err != nil {
		t.Fatalf("ListFeedback active: %v", err)
	}
	for _, f := range active {
		if f.Status == "superseded" {
			t.Errorf("ActiveOnly returned superseded row: %+v", f)
		}
	}
	// Expect 2 active rows (fp-a and fp-b).
	if len(active) != 2 {
		t.Errorf("ActiveOnly count = %d, want 2", len(active))
	}
}

// TestPopulatedNullableRoundTrip verifies that all nullable tail fields of a
// ci-failure row survive an UpsertFeedback → GetFeedback cycle.
func TestPopulatedNullableRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	prID, _ := db.UpsertPR(ctx, PullRequest{
		Repo: "o/r", Number: 1, Ownership: "mine", State: "open",
	})

	in := Feedback{
		PRID:        prID,
		Kind:        "ci-failure",
		Fingerprint: "fp-rt",
		RunID:       "run-42",
		CheckName:   "lint",
		Conclusion:  "failure",
		Related:     true,
		RetryCount:  3,
		Link:        "https://ci.example.com/run/42",
		SubjectSHA:  "deadbeef",
	}
	id, err := db.UpsertFeedback(ctx, in)
	if err != nil {
		t.Fatalf("UpsertFeedback: %v", err)
	}

	got, err := db.GetFeedback(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("GetFeedback: %v / nil=%v", err, got == nil)
	}

	if got.RunID != in.RunID {
		t.Errorf("RunID = %q, want %q", got.RunID, in.RunID)
	}
	if got.CheckName != in.CheckName {
		t.Errorf("CheckName = %q, want %q", got.CheckName, in.CheckName)
	}
	if got.Conclusion != in.Conclusion {
		t.Errorf("Conclusion = %q, want %q", got.Conclusion, in.Conclusion)
	}
	if got.Related != in.Related {
		t.Errorf("Related = %v, want %v", got.Related, in.Related)
	}
	if got.RetryCount != in.RetryCount {
		t.Errorf("RetryCount = %d, want %d", got.RetryCount, in.RetryCount)
	}
	if got.Link != in.Link {
		t.Errorf("Link = %q, want %q", got.Link, in.Link)
	}
	if got.SubjectSHA != in.SubjectSHA {
		t.Errorf("SubjectSHA = %q, want %q", got.SubjectSHA, in.SubjectSHA)
	}
}

package store

import (
	"context"
	"testing"
)

// The merge-loop gate: a PR has blocking feedback while any actionable row
// (ci-failure or self-review) is unresolved — status NOT IN
// (dispositioned, resolved, superseded). Dispositioning clears the block.
// This mirrors how ci-failure feedback already gates auto-merge; self-review
// (pg2-4c5i.34, Q1 — always block) MUST gate the same way.
func TestHasBlockingFeedback_SelfReviewGatesUntilDispositioned(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	prID, _ := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 1, Ownership: "mine", State: "open", HeadSHA: "h1"})

	// No feedback yet → not blocked.
	blocked, err := db.HasBlockingFeedback(ctx, prID)
	if err != nil {
		t.Fatalf("HasBlockingFeedback: %v", err)
	}
	if blocked {
		t.Fatal("a PR with no feedback must not be blocked")
	}

	// A new self-review finding → blocked (gates like an unresolved ci-failure).
	srID, err := db.UpsertFeedback(ctx, Feedback{
		PRID: prID, Kind: "self-review", Fingerprint: "fp-sr", Body: "fix this", IsOurs: true, AuthorKind: "agent", SubjectSHA: "h1",
	})
	if err != nil {
		t.Fatalf("UpsertFeedback self-review: %v", err)
	}
	blocked, err = db.HasBlockingFeedback(ctx, prID)
	if err != nil {
		t.Fatalf("HasBlockingFeedback: %v", err)
	}
	if !blocked {
		t.Fatal("an unresolved self-review finding MUST block the merge loop")
	}

	// Dispositioning it clears the block.
	if err := db.SetDisposition(ctx, srID, "will-fix", "bead created", ""); err != nil {
		t.Fatalf("SetDisposition: %v", err)
	}
	blocked, err = db.HasBlockingFeedback(ctx, prID)
	if err != nil {
		t.Fatalf("HasBlockingFeedback: %v", err)
	}
	if blocked {
		t.Fatal("a dispositioned self-review finding MUST NOT block the merge loop")
	}
}

// A new ci-failure blocks and a new self-review blocks identically — proving the
// self-review kind is treated exactly like ci-failure by the gate.
func TestHasBlockingFeedback_CIFailureAndSelfReviewAreEquivalent(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	prID, _ := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 2, Ownership: "mine", State: "open", HeadSHA: "h1"})

	ciID, _ := db.UpsertFeedback(ctx, Feedback{PRID: prID, Kind: "ci-failure", Fingerprint: "fp-ci", SubjectSHA: "h1", CheckName: "build", Conclusion: "failure"})
	if b, _ := db.HasBlockingFeedback(ctx, prID); !b {
		t.Fatal("unresolved ci-failure must block")
	}
	// Dispositioning the ci-failure clears the block (no self-review present).
	if err := db.SetDisposition(ctx, ciID, "wont-fix", "flaky", ""); err != nil {
		t.Fatalf("SetDisposition ci: %v", err)
	}
	if b, _ := db.HasBlockingFeedback(ctx, prID); b {
		t.Fatal("dispositioned ci-failure alone must not block")
	}

	// Now add a self-review row: it must re-block on its own.
	_, _ = db.UpsertFeedback(ctx, Feedback{PRID: prID, Kind: "self-review", Fingerprint: "fp-sr2", Body: "x", IsOurs: true, AuthorKind: "agent", SubjectSHA: "h1"})
	if b, _ := db.HasBlockingFeedback(ctx, prID); !b {
		t.Fatal("an unresolved self-review must block even when ci-failure is dispositioned")
	}
}

// A superseded self-review row (e.g. stale head) does not block — matching the
// ci-failure supersede semantics.
func TestHasBlockingFeedback_SupersededSelfReviewDoesNotBlock(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	prID, _ := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 3, Ownership: "mine", State: "open", HeadSHA: "h2"})

	srID, _ := db.UpsertFeedback(ctx, Feedback{PRID: prID, Kind: "self-review", Fingerprint: "fp-old", Body: "old", IsOurs: true, AuthorKind: "agent", SubjectSHA: "h1"})
	// Force it superseded directly (simulating a resolution path).
	if _, err := db.sql.ExecContext(ctx, "UPDATE feedback SET status='superseded' WHERE id=?", srID); err != nil {
		t.Fatalf("supersede: %v", err)
	}
	if b, _ := db.HasBlockingFeedback(ctx, prID); b {
		t.Fatal("a superseded self-review must not block")
	}
}

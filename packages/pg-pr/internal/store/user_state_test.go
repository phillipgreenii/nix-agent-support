package store

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// TestUserState_RoundTrip covers SetHidden (with a reason) and SetWIP,
// reading both back through GetPR.
func TestUserState_RoundTrip(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)

	if _, err := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 1, Ownership: "mine", State: "open"}); err != nil {
		t.Fatalf("seed UpsertPR: %v", err)
	}

	if err := db.SetHidden(ctx, "o/r", 1, true, "noisy CI churn"); err != nil {
		t.Fatalf("SetHidden: %v", err)
	}
	if err := db.SetWIP(ctx, "o/r", 1, true); err != nil {
		t.Fatalf("SetWIP: %v", err)
	}

	got, err := db.GetPR(ctx, "o/r", 1)
	if err != nil || got == nil {
		t.Fatalf("GetPR: %v %v", got, err)
	}
	if !got.UserHidden {
		t.Errorf("UserHidden = false, want true")
	}
	if got.UserHiddenReason != "noisy CI churn" {
		t.Errorf("UserHiddenReason = %q, want %q", got.UserHiddenReason, "noisy CI churn")
	}
	if !got.WIP {
		t.Errorf("WIP = false, want true")
	}
}

// TestUserState_NoClobber is the core invariant this leaf exists to prove: a
// plain UpsertPR call — exactly what a sync tick does — must never touch
// user_hidden/user_hidden_reason/wip.
func TestUserState_NoClobber(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)

	base := PullRequest{Repo: "o/r", Number: 5, Ownership: "mine", Author: "me", State: "open", Branch: "b"}
	if _, err := db.UpsertPR(ctx, base); err != nil {
		t.Fatalf("seed UpsertPR: %v", err)
	}
	if err := db.SetHidden(ctx, "o/r", 5, true, "reason"); err != nil {
		t.Fatalf("SetHidden: %v", err)
	}
	if err := db.SetWIP(ctx, "o/r", 5, true); err != nil {
		t.Fatalf("SetWIP: %v", err)
	}

	// A subsequent plain UpsertPR (as the lifecycle emit / sync tick / ingest
	// does) MUST NOT clobber the user-owned columns.
	base.HeadSHA = "newsha"
	if _, err := db.UpsertPR(ctx, base); err != nil {
		t.Fatalf("re-UpsertPR: %v", err)
	}

	got, err := db.GetPR(ctx, "o/r", 5)
	if err != nil || got == nil {
		t.Fatalf("GetPR: %v %v", got, err)
	}
	if !got.UserHidden || got.UserHiddenReason != "reason" || !got.WIP {
		t.Fatalf("UpsertPR clobbered user state: %+v", got)
	}
	if got.HeadSHA != "newsha" {
		t.Fatalf("UpsertPR did not apply its own update: %+v", got)
	}
}

// TestUserState_DefaultsOnFreshUpsert asserts a PR created by UpsertPR alone
// reads not-hidden / not-WIP / empty reason — never NULL, since the columns
// are NOT NULL DEFAULT.
func TestUserState_DefaultsOnFreshUpsert(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)

	if _, err := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 2, Ownership: "team", State: "open"}); err != nil {
		t.Fatalf("seed UpsertPR: %v", err)
	}
	got, err := db.GetPR(ctx, "o/r", 2)
	if err != nil || got == nil {
		t.Fatalf("GetPR: %v %v", got, err)
	}
	if got.UserHidden {
		t.Errorf("UserHidden default = true, want false")
	}
	if got.UserHiddenReason != "" {
		t.Errorf("UserHiddenReason default = %q, want empty", got.UserHiddenReason)
	}
	if got.WIP {
		t.Errorf("WIP default = true, want false")
	}
}

// TestUserState_IdempotentReapplication asserts re-applying the same state
// twice is a no-op: no error, and no side effect on any other column.
func TestUserState_IdempotentReapplication(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)

	if _, err := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 3, Ownership: "mine", State: "open", HeadSHA: "h1"}); err != nil {
		t.Fatalf("seed UpsertPR: %v", err)
	}

	// hidden=true applied twice.
	if err := db.SetHidden(ctx, "o/r", 3, true, "why"); err != nil {
		t.Fatalf("SetHidden #1: %v", err)
	}
	if err := db.SetHidden(ctx, "o/r", 3, true, "why"); err != nil {
		t.Fatalf("SetHidden #2: %v", err)
	}
	// wip=true applied twice.
	if err := db.SetWIP(ctx, "o/r", 3, true); err != nil {
		t.Fatalf("SetWIP #1: %v", err)
	}
	if err := db.SetWIP(ctx, "o/r", 3, true); err != nil {
		t.Fatalf("SetWIP #2: %v", err)
	}

	got, err := db.GetPR(ctx, "o/r", 3)
	if err != nil || got == nil {
		t.Fatalf("GetPR: %v %v", got, err)
	}
	if !got.UserHidden || got.UserHiddenReason != "why" || !got.WIP {
		t.Fatalf("repeated set changed state unexpectedly: %+v", got)
	}
	if got.HeadSHA != "h1" || got.Ownership != "mine" || got.State != "open" {
		t.Fatalf("repeated set touched an unrelated column: %+v", got)
	}

	// unhide of an already-visible PR, and WIP-off of a non-WIP PR, are also
	// no-ops with no error.
	if _, err := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 4, Ownership: "team", State: "open"}); err != nil {
		t.Fatalf("seed second UpsertPR: %v", err)
	}
	if err := db.SetHidden(ctx, "o/r", 4, false, ""); err != nil {
		t.Fatalf("unhide of already-visible PR: %v", err)
	}
	if err := db.SetWIP(ctx, "o/r", 4, false); err != nil {
		t.Fatalf("WIP-off of a non-WIP PR: %v", err)
	}
	got2, err := db.GetPR(ctx, "o/r", 4)
	if err != nil || got2 == nil {
		t.Fatalf("GetPR(4): %v %v", got2, err)
	}
	if got2.UserHidden || got2.WIP || got2.UserHiddenReason != "" {
		t.Fatalf("no-op set changed state: %+v", got2)
	}
}

// TestUserState_UnhideClearsReason pins the operator's fork #5 ruling:
// unhiding ALWAYS clears the recorded reason, even when a caller supplies a
// (nonsensical) reason alongside hidden=false.
func TestUserState_UnhideClearsReason(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)

	if _, err := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 6, Ownership: "mine", State: "open"}); err != nil {
		t.Fatalf("seed UpsertPR: %v", err)
	}
	if err := db.SetHidden(ctx, "o/r", 6, true, "original reason"); err != nil {
		t.Fatalf("SetHidden: %v", err)
	}

	// Unhide, deliberately passing a non-empty reason: it MUST be discarded.
	if err := db.SetHidden(ctx, "o/r", 6, false, "this must not stick"); err != nil {
		t.Fatalf("SetHidden(unhide): %v", err)
	}

	got, err := db.GetPR(ctx, "o/r", 6)
	if err != nil || got == nil {
		t.Fatalf("GetPR: %v %v", got, err)
	}
	if got.UserHidden {
		t.Errorf("UserHidden = true after unhide, want false")
	}
	if got.UserHiddenReason != "" {
		t.Errorf("UserHiddenReason = %q after unhide, want cleared to empty", got.UserHiddenReason)
	}
}

// TestUserState_MissingRowErrors pins the operator's fork #6 ruling: both
// setters ERROR against an unknown (repo, number), matching SetDisposition's
// pattern rather than SetEnrichment's silent no-op.
func TestUserState_MissingRowErrors(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)

	err := db.SetHidden(ctx, "o/r", 999, true, "reason")
	if err == nil {
		t.Fatal("SetHidden against a missing row: want error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("SetHidden error = %q, want it to mention \"not found\"", err.Error())
	}

	err = db.SetWIP(ctx, "o/r", 999, true)
	if err == nil {
		t.Fatal("SetWIP against a missing row: want error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("SetWIP error = %q, want it to mention \"not found\"", err.Error())
	}
}

// TestUserState_SurvivesMergeAndClose: nothing in this module ever deletes a
// pull_request row, and a plain UpsertPR (the only writer a closed/merged PR
// gets from sync) never touches these columns — so a hidden-then-merged PR
// must keep its flag, and a WIP-then-closed PR must keep its flag too.
func TestUserState_SurvivesMergeAndClose(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)

	pr := PullRequest{Repo: "o/r", Number: 8, Ownership: "mine", State: "open"}
	if _, err := db.UpsertPR(ctx, pr); err != nil {
		t.Fatalf("seed UpsertPR: %v", err)
	}
	if err := db.SetHidden(ctx, "o/r", 8, true, "still relevant later"); err != nil {
		t.Fatalf("SetHidden: %v", err)
	}
	if err := db.SetWIP(ctx, "o/r", 8, true); err != nil {
		t.Fatalf("SetWIP: %v", err)
	}

	// Simulate sync observing the PR merged: a plain UpsertPR with state
	// transitioned, exactly like the sync write path.
	pr.State = "merged"
	if _, err := db.UpsertPR(ctx, pr); err != nil {
		t.Fatalf("UpsertPR(merged): %v", err)
	}

	got, err := db.GetPR(ctx, "o/r", 8)
	if err != nil || got == nil {
		t.Fatalf("GetPR: %v %v", got, err)
	}
	if got.State != "merged" {
		t.Fatalf("State = %q, want merged", got.State)
	}
	if !got.UserHidden || got.UserHiddenReason != "still relevant later" {
		t.Fatalf("hidden flag/reason lost across merge: %+v", got)
	}
	if !got.WIP {
		t.Fatalf("WIP flag lost across merge: %+v", got)
	}
}

// TestUserState_ExecErrorIsWrapped proves both setters propagate a genuine
// ExecContext failure as a wrapped error rather than swallowing it. Closing
// the underlying handle first forces database/sql to return sql.ErrConnDone
// from ExecContext, exercising the "err != nil" branch in SetHidden and
// SetWIP (user_state.go) that the happy-path tests above never take.
func TestUserState_ExecErrorIsWrapped(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := db.SetHidden(ctx, "o/r", 1, true, "reason"); err == nil {
		t.Fatal("SetHidden against a closed db: want error, got nil")
	}
	if err := db.SetWIP(ctx, "o/r", 1, true); err == nil {
		t.Fatal("SetWIP against a closed db: want error, got nil")
	}
}

// TestUserState_ConcurrentHideAndUpsert proves the existing WAL +
// busy_timeout posture (store.go) is sufficient for a single-column
// user-state write racing a concurrent UpsertPR from a second handle on the
// SAME file — the shape of an ad-hoc `pg-pr pr hide` running while the
// daemon's sync tick is in flight. This is NOT a new cross-process lock (a
// separate bead's subject); it only proves today's posture already handles
// this case with no "database is locked" error.
func TestUserState_ConcurrentHideAndUpsert(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := dir + "/concurrent.db"

	dbA, err := Open(path)
	if err != nil {
		t.Fatalf("open handle A: %v", err)
	}
	t.Cleanup(func() { _ = dbA.Close() })

	// Seed the row via handle A before opening handle B / racing writers, so
	// both writers race on an UPDATE against the SAME existing row rather
	// than one of them contending with row creation too.
	if _, err := dbA.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 9, Ownership: "mine", State: "open", HeadSHA: "h0"}); err != nil {
		t.Fatalf("seed via handle A: %v", err)
	}

	dbB, err := Open(path)
	if err != nil {
		t.Fatalf("open handle B: %v", err)
	}
	t.Cleanup(func() { _ = dbB.Close() })

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := dbA.SetHidden(ctx, "o/r", 9, true, "racing hide"); err != nil {
			errs <- err
		}
	}()
	go func() {
		defer wg.Done()
		if _, err := dbB.UpsertPR(ctx, PullRequest{
			Repo: "o/r", Number: 9, Ownership: "mine", State: "open", HeadSHA: "h1",
		}); err != nil {
			errs <- err
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write failed (want no \"database is locked\" error): %v", err)
	}

	for name, db := range map[string]*DB{"A": dbA, "B": dbB} {
		got, err := db.GetPR(ctx, "o/r", 9)
		if err != nil || got == nil {
			t.Fatalf("handle %s GetPR: %v %v", name, got, err)
		}
		if !got.UserHidden {
			t.Errorf("handle %s: UserHidden = false, want true after concurrent hide+upsert", name)
		}
	}
}

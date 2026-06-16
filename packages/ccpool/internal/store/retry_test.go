package store

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/clock"
)

// TestRetryState_defaultsZero confirms migration 005 adds the columns with a 0
// default and that a freshly inserted row reads back zero retry state.
func TestRetryState_defaultsZero(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.Insert(ctx, Session{ExternalID: "ext-r", State: Working}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.GetByExternalID(ctx, "ext-r")
	if err != nil || !ok {
		t.Fatalf("GetByExternalID: ok=%v err=%v", ok, err)
	}
	if got.RetryCount != 0 || got.RetryWindowStartedAt != 0 {
		t.Errorf("fresh row retry state = (%d, %d), want (0, 0)", got.RetryCount, got.RetryWindowStartedAt)
	}
}

// TestBumpRetry_firstAnchorsWindow proves the first BumpRetry increments the
// count and anchors the window to the clock; subsequent bumps increment the
// count but leave the window start fixed.
func TestBumpRetry_firstAnchorsWindow(t *testing.T) {
	clk := &clock.Fake{T: time.Unix(5000, 0).UTC()}
	st, err := Open(":memory:", clk)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.Insert(ctx, Session{ExternalID: "ext-r", State: Errored}); err != nil {
		t.Fatal(err)
	}

	if err := st.BumpRetry(ctx, "ext-r"); err != nil {
		t.Fatalf("BumpRetry #1: %v", err)
	}
	got, _, _ := st.GetByExternalID(ctx, "ext-r")
	if got.RetryCount != 1 {
		t.Errorf("after bump #1 RetryCount = %d, want 1", got.RetryCount)
	}
	if got.RetryWindowStartedAt != 5000 {
		t.Errorf("after bump #1 RetryWindowStartedAt = %d, want 5000 (anchored to clock)", got.RetryWindowStartedAt)
	}

	clk.Advance(10 * time.Second) // clock moves to 5010
	if err := st.BumpRetry(ctx, "ext-r"); err != nil {
		t.Fatalf("BumpRetry #2: %v", err)
	}
	got, _, _ = st.GetByExternalID(ctx, "ext-r")
	if got.RetryCount != 2 {
		t.Errorf("after bump #2 RetryCount = %d, want 2", got.RetryCount)
	}
	if got.RetryWindowStartedAt != 5000 {
		t.Errorf("after bump #2 RetryWindowStartedAt = %d, want 5000 (window unchanged)", got.RetryWindowStartedAt)
	}
}

// TestResetRetry_zeroesBudget proves ResetRetry clears both columns so a later
// unrelated transient error gets a fresh budget.
func TestResetRetry_zeroesBudget(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.Insert(ctx, Session{ExternalID: "ext-r", State: Errored, RetryCount: 3, RetryWindowStartedAt: 1234}); err != nil {
		t.Fatal(err)
	}
	if err := st.ResetRetry(ctx, "ext-r"); err != nil {
		t.Fatalf("ResetRetry: %v", err)
	}
	got, _, _ := st.GetByExternalID(ctx, "ext-r")
	if got.RetryCount != 0 || got.RetryWindowStartedAt != 0 {
		t.Errorf("after reset retry state = (%d, %d), want (0, 0)", got.RetryCount, got.RetryWindowStartedAt)
	}
}

package notice

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
)

// stale is what a refresh that fetched before the write holds.
func stale() groups.SummaryContent { return summaryOf(keyA, "stale title") }

// requireUntouched fails when the stored summary or the change log moved.
func requireUntouched(t *testing.T, r *rig, head int64, title string) {
	t.Helper()
	if got := r.storedTitle(keyA); got != title {
		t.Fatalf("stored title = %q, want %q (older content was stored)", got, title)
	}
	if got := r.changeHead(); got != head {
		t.Fatalf("change head = %d, want %d (a change row was appended)", got, head)
	}
}

// A refresh that took its token and began its fetch BEFORE write_begin loses its
// commit even when it is persisted AFTER invalidate released the hold.
func TestRefreshBegunBeforeWriteBeginLosesAfterInvalidateReleased(t *testing.T) {
	r := newRig(t)
	head := r.seed(keyA, oldText)
	tok := r.begin(keyA) // the refresh starts, its fetch is slow

	r.mustCall("write_begin", `{"id":"`+keyA+`"}`)
	r.mustCall("invalidate", `{"id":"`+keyA+`"}`)
	if r.table.Held(keyA) {
		t.Fatal("still held")
	}

	if st := r.commit(tok, stale()); !st.FreshUntil.IsZero() || st.Prev != nil || st.First {
		t.Fatalf("the stale refresh stored: %+v", st)
	}
	requireUntouched(t, r, head, oldText)
}

// The same refresh persisted while the hold is in force also stores nothing.
func TestRefreshBegunBeforeWriteBeginLosesWhileHeld(t *testing.T) {
	r := newRig(t)
	head := r.seed(keyA, oldText)
	tok := r.begin(keyA)
	r.mustCall("write_begin", `{"id":"`+keyA+`"}`)
	r.commit(tok, stale())
	requireUntouched(t, r, head, oldText)
}

// A refresh begun after write_begin and committed while the entity is held stores
// nothing (the guard store reports stored false), and the same refresh still
// loses after invalidate released the hold, because invalidate bumped again.
func TestRefreshBegunDuringTheHoldStoresNothingNowOrLater(t *testing.T) {
	r := newRig(t)
	head := r.seed(keyA, oldText)
	r.mustCall("write_begin", `{"id":"`+keyA+`"}`)
	tok := r.begin(keyA)

	r.commit(tok, stale())
	requireUntouched(t, r, head, oldText)

	r.mustCall("invalidate", `{"id":"`+keyA+`"}`)
	r.commit(tok, stale())
	requireUntouched(t, r, head, oldText)
}

// After invalidate exactly one re-pull is queued per group and its content is
// stored: the control that also shows a stored refresh does append a change row.
func TestRePullAfterInvalidateStoresTheNewContent(t *testing.T) {
	r := newRig(t)
	head := r.seed(keyA, oldText)
	r.origin.set(keyA, newText)
	r.mustCall("write_begin", `{"id":"`+keyA+`"}`)
	r.mustCall("invalidate", `{"id":"`+keyA+`"}`)
	if r.origin.count() != 0 {
		t.Fatal("a notice called the origin")
	}

	r.sched.Drain(context.Background())

	if got := r.storedTitle(keyA); got != newText {
		t.Fatalf("stored title = %q, want %q", got, newText)
	}
	if r.changeHead() <= head {
		t.Fatal("the stored re-pull appended no change row")
	}
	if n := r.origin.count(); n != 2 {
		t.Fatalf("origin calls = %d, want 2 (one summary re-pull, one detail re-pull)", n)
	}
	if g, ok := r.group(keyA, groups.Detail); !ok || len(g.Content) == 0 {
		t.Fatal("the detail re-pull stored nothing")
	}
}

// With invalidate never delivered the hold ends at the lease, one re-pull is
// queued and no older content was stored in between.
func TestDroppedInvalidateTheHoldEndsAtTheLease(t *testing.T) {
	r := newRig(t)
	head := r.seed(keyA, oldText)
	before := r.begin(keyA) // begun before the write
	r.mustCall("write_begin", `{"id":"`+keyA+`"}`)
	during := r.begin(keyA) // begun during the write

	r.commit(before, stale())
	r.commit(during, stale())
	requireUntouched(t, r, head, oldText)

	r.clk.Advance(lease)
	r.timers.fire(r.timers.last())
	if r.table.Held(keyA) {
		t.Fatal("the hold outlived the lease")
	}
	if _, ok := r.queued(keyA, groups.Summary); !ok {
		t.Fatal("no re-pull queued at the lease's end")
	}

	// Neither refresh can store after the hold ended either.
	r.commit(before, stale())
	r.commit(during, stale())
	requireUntouched(t, r, head, oldText)

	// A refresh begun after the lease ended stores.
	r.origin.set(keyA, newText)
	r.sched.Drain(context.Background())
	if got := r.storedTitle(keyA); got != newText {
		t.Fatalf("stored title = %q, want %q", got, newText)
	}
}

// A write to one issue does not hold another's refresh.
func TestWriteDoesNotHoldAnotherIssue(t *testing.T) {
	r := newRig(t)
	r.seed(keyA, oldText)
	r.seed("B-2", oldText)
	r.mustCall("write_begin", `{"id":"`+keyA+`"}`)
	if st := r.persist(summaryOf("B-2", newText)); st.FreshUntil.IsZero() {
		t.Fatal("a refresh of another issue was held")
	}
	if r.storedTitle("B-2") != newText {
		t.Fatal("the other issue did not store")
	}
}
